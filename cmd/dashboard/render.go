package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"strings"
)

// mermaidJS is the vendored, version-pinned mermaid.js bundle, inlined verbatim
// into the emitted HTML so the dashboard has zero external fetches (spec #52).
// The dist build assigns globalThis.mermaid from a plain <script> tag, so no
// module loader is needed.
//
//go:embed vendor/mermaid-11.4.1.min.js
var mermaidJS string

// DashboardData is the complete view model for one dashboard build: the whole
// AFK graph re-derived from the GitHub API (spec #48), shaped so the pure
// renderer never touches the network. Each field feeds exactly one of the six
// panels; the caller (the gh-CLI fetch in main) is the only place with I/O.
type DashboardData struct {
	// GeneratedAt is a human-readable build timestamp shown in the header. The
	// caller supplies it so render stays pure and deterministic under test.
	GeneratedAt string
	// Repo is the "owner/name" slug shown in the header.
	Repo string

	// Graph is the dependency graph drawn as a hand-drawn Mermaid flowchart.
	Graph DependencyGraph
	// NeedsReview is the needs-review queue: runs that finished successfully and
	// are waiting for a human (spec #27).
	NeedsReview []IssueRow
	// InFlight is the in-flight panel: issues currently claimed as afk:running
	// (spec #12).
	InFlight []RunningRow
	// Failures is the failures panel, carrying the full D7 hand-back fields for
	// each afk:failed issue (spec #31, #51).
	Failures []FailureRow
	// RecentRuns is the recent-runs panel: the tail of the AFK workflow run list.
	RecentRuns []RunRow
	// OpenPRs is the open-AFK-PRs panel, each carrying its D3 outcome comment
	// (spec #25, #51).
	OpenPRs []PRRow
}

// DependencyGraph is the node/edge set behind the dependency-graph panel. Nodes
// are AFK issues; an edge points from a blocker to the issue it blocks, so the
// arrow reads "must finish before".
type DependencyGraph struct {
	Nodes []GraphNode
	Edges []GraphEdge
}

// GraphNode is one issue in the dependency graph.
type GraphNode struct {
	Number int
	Title  string
	// State is the AFK label state (afk, afk:running, needs-review, afk:failed);
	// it selects the node's colour class in the graph.
	State string
	URL   string
}

// GraphEdge is a blocking-dependency edge: Blocker must close before Blocked can
// run (spec #10). Rendered as `Blocker --> Blocked`.
type GraphEdge struct {
	Blocker int
	Blocked int
}

// IssueRow is a plain issue line used by the needs-review queue.
type IssueRow struct {
	Number int
	Title  string
	URL    string
	// Skill is the derived skill (/implement or /research).
	Skill string
	// Branch is the resolved working branch.
	Branch string
}

// RunningRow is an in-flight run: an issue plus a link to its live Actions run.
type RunningRow struct {
	Number int
	Title  string
	URL    string
	Skill  string
	Branch string
	// RunURL links to the in-progress workflow run.
	RunURL string
}

// FailureRow carries the full D7 hand-back context for one failed run so the
// failures panel is actionable without opening the Actions logs (spec #31).
type FailureRow struct {
	Number int
	Title  string
	URL    string
	// Phase names which of the three phases died (activation / agent / finalize).
	Phase string
	// RunURL is the run-logs link.
	RunURL string
	Skill  string
	Branch string
	// Artifacts are partial-artifact links (e.g. a pushed branch or conflict
	// ref). When empty the panel shows "nothing pushed" (spec #31).
	Artifacts []Link
}

// Link is a labelled hyperlink used where a row carries a list of links.
type Link struct {
	Label string
	URL   string
}

// RunRow is one entry in the recent-runs panel.
type RunRow struct {
	// Title names the run (workflow name or the issue it targeted).
	Title string
	URL   string
	// Status is the run status (e.g. completed, in_progress).
	Status string
	// Conclusion is the terminal outcome (success, failure, cancelled); empty
	// while a run is still going.
	Conclusion string
	// When is a human-readable timestamp for the run.
	When string
}

// PRRow is one open AFK pull request, carrying the D3 outcome comment so a run
// can be triaged from the dashboard alone (spec #25).
type PRRow struct {
	Number int
	Title  string
	URL    string
	Branch string
	// Issue is the source issue linked via `Part of #<n>` (spec #24).
	Issue int
	// Outcome is the in-agent typecheck / test / code-review outcome comment
	// posted on the PR (spec #25).
	Outcome string
}

// render turns a DashboardData into a single self-contained HTML page. It is
// pure — no network, no clock, no filesystem beyond the embedded, compile-time
// mermaid bundle — so the six panels are fixture-testable (spec #64).
func render(d DashboardData) (string, error) {
	var buf bytes.Buffer
	data := struct {
		DashboardData
		MermaidJS    template.JS
		MermaidGraph string
	}{
		DashboardData: d,
		// template.JS marks the vendored bundle as trusted script so html/template
		// inlines it verbatim instead of escaping it into a broken string.
		MermaidJS:    template.JS(mermaidJS),
		MermaidGraph: mermaidSource(d.Graph),
	}
	if err := dashboardTmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("rendering dashboard: %w", err)
	}
	return buf.String(), nil
}

// mermaidSource builds the Mermaid flowchart definition for the dependency
// graph. The `look: handDrawn` config frontmatter gives the hand-drawn style
// (spec #52); the source is emitted into a <pre class="mermaid"> block whose
// textContent the browser decodes, so html/template's HTML-escaping of this
// string is both XSS-safe and mermaid-correct.
func mermaidSource(g DependencyGraph) string {
	if len(g.Nodes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("---\nconfig:\n  look: handDrawn\n---\nflowchart TD\n")
	for _, n := range g.Nodes {
		b.WriteString(fmt.Sprintf("  n%d[\"#%d: %s\"]\n", n.Number, n.Number, mermaidLabel(n.Title)))
	}
	for _, e := range g.Edges {
		b.WriteString(fmt.Sprintf("  n%d --> n%d\n", e.Blocker, e.Blocked))
	}
	// One class per AFK state colours the nodes; classDef names must exist before
	// they are applied.
	b.WriteString("  classDef afk fill:#e8eefc,stroke:#3b5bdb;\n")
	b.WriteString("  classDef running fill:#fff3bf,stroke:#e67700;\n")
	b.WriteString("  classDef review fill:#d3f9d8,stroke:#2b8a3e;\n")
	b.WriteString("  classDef failed fill:#ffe3e3,stroke:#c92a2a;\n")
	for _, n := range g.Nodes {
		if c := stateClass(n.State); c != "" {
			b.WriteString(fmt.Sprintf("  class n%d %s;\n", n.Number, c))
		}
	}
	return b.String()
}

// mermaidLabel sanitises an issue title for use inside a quoted Mermaid node
// label: double quotes would close the label early, so they become single
// quotes. HTML-escaping of the rest is handled downstream by html/template.
func mermaidLabel(title string) string {
	return strings.ReplaceAll(title, `"`, `'`)
}

// stateClass maps an AFK label state to its Mermaid node class.
func stateClass(state string) string {
	switch state {
	case "afk:running":
		return "running"
	case "needs-review":
		return "review"
	case "afk:failed":
		return "failed"
	case "afk":
		return "afk"
	default:
		return ""
	}
}

//go:embed dashboard.html.tmpl
var dashboardTmplSrc string

var dashboardTmpl = template.Must(template.New("dashboard").Parse(dashboardTmplSrc))
