package main

import (
	"regexp"
	"strings"
	"testing"
)

// fullData is a fixture that puts every panel into its populated state: a
// multi-node dependency graph, a needs-review row, an in-flight run, a failure
// row with all D7 hand-back fields, recent runs, and an open PR with a D3
// outcome comment. Each panel test draws from it (or from an empty
// DashboardData) so the states stay meaningful (spec #64 acceptance criteria).
func fullData() DashboardData {
	return DashboardData{
		GeneratedAt: "2026-08-03 17:00 UTC",
		Repo:        "emepetres/life-ledger",
		Graph: DependencyGraph{
			Nodes: []GraphNode{
				{Number: 77, Title: "Spec: Graph Engineering", State: "afk", URL: "https://github.com/emepetres/life-ledger/issues/77"},
				{Number: 79, Title: "Dispatch helper", State: "needs-review", URL: "https://github.com/emepetres/life-ledger/issues/79"},
				{Number: 80, Title: `Dashboard renderer "core"`, State: "afk:running", URL: "https://github.com/emepetres/life-ledger/issues/80"},
			},
			Edges: []GraphEdge{
				{Blocker: 77, Blocked: 79},
				{Blocker: 77, Blocked: 80},
			},
		},
		NeedsReview: []IssueRow{
			{Number: 79, Title: "Dispatch helper", URL: "https://github.com/emepetres/life-ledger/issues/79", Skill: "/implement", Branch: "afk/79-dispatch"},
		},
		InFlight: []RunningRow{
			{Number: 80, Title: "Dashboard renderer", URL: "https://github.com/emepetres/life-ledger/issues/80", Skill: "/implement", Branch: "feature/graph-engineering", RunURL: "https://github.com/emepetres/life-ledger/actions/runs/123"},
		},
		Failures: []FailureRow{
			{
				Number: 81, Title: "Copilot validation",
				URL:   "https://github.com/emepetres/life-ledger/issues/81",
				Phase: "agent", RunURL: "https://github.com/emepetres/life-ledger/actions/runs/456",
				Skill: "/research", Branch: "afk/81-copilot",
				Artifacts: []Link{{Label: "afk/81-copilot", URL: "https://github.com/emepetres/life-ledger/tree/afk/81-copilot"}},
			},
		},
		RecentRuns: []RunRow{
			{Title: "afk #79", URL: "https://github.com/emepetres/life-ledger/actions/runs/100", Status: "completed", Conclusion: "success", When: "2h ago"},
			{Title: "afk #81", URL: "https://github.com/emepetres/life-ledger/actions/runs/456", Status: "completed", Conclusion: "failure", When: "1h ago"},
		},
		OpenPRs: []PRRow{
			{Number: 82, Title: "[afk] Dispatch helper", URL: "https://github.com/emepetres/life-ledger/pull/82", Branch: "afk/79-dispatch", Issue: 79, Outcome: "typecheck: ok\ntest: 42 passed\ncode-review: clean"},
		},
	}
}

// mustRender renders and fails the test on any error.
func mustRender(t *testing.T, d DashboardData) string {
	t.Helper()
	html, err := render(d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return html
}

// section returns the HTML slice for the panel with the given id, so an
// assertion about one panel can't be satisfied by text from another.
func section(t *testing.T, html, id string) string {
	t.Helper()
	start := strings.Index(html, `id="`+id+`"`)
	if start < 0 {
		t.Fatalf("panel %q not found in rendered HTML", id)
	}
	rest := html[start:]
	if end := strings.Index(rest[1:], "<section"); end >= 0 {
		return rest[:end]
	}
	return rest
}

// AC: the dependency-graph panel renders a multi-node hand-drawn Mermaid graph
// with every node present.
func TestGraphPanelRendersAllNodesHandDrawn(t *testing.T) {
	html := mustRender(t, fullData())
	graph := section(t, html, "graph")

	if !strings.Contains(graph, `class="mermaid"`) {
		t.Errorf("graph panel should hold a mermaid block; got:\n%s", graph)
	}
	// look: handDrawn is the whole point of spec #52; html/template escapes the
	// ':' as-is inside the <pre>, so it appears verbatim.
	if !strings.Contains(graph, "look: handDrawn") {
		t.Errorf("graph should request the hand-drawn look; got:\n%s", graph)
	}
	// Every node id and its title must appear, and the two edges.
	for _, want := range []string{"n77", "n79", "n80", "Spec: Graph Engineering", "Dispatch helper", "n77 --&gt; n79", "n77 --&gt; n80"} {
		if !strings.Contains(graph, want) {
			t.Errorf("graph missing %q; got:\n%s", want, graph)
		}
	}
	// A title with a double quote must not break the quoted node label: it is
	// down-quoted, so no stray unescaped '"' survives inside the label token.
	if strings.Contains(graph, `renderer "core"`) {
		t.Errorf("double-quoted title should be down-quoted in the label; got:\n%s", graph)
	}
}

// AC: the graph panel shows an empty state when there are no AFK issues, and
// emits no mermaid block (nothing to draw).
func TestGraphPanelEmpty(t *testing.T) {
	html := mustRender(t, DashboardData{})
	graph := section(t, html, "graph")
	if strings.Contains(graph, `class="mermaid"`) {
		t.Errorf("empty graph should not emit a mermaid block; got:\n%s", graph)
	}
	if !strings.Contains(graph, "No AFK issues") {
		t.Errorf("empty graph should show an empty state; got:\n%s", graph)
	}
}

// AC: the needs-review queue lists each waiting issue with its derived skill and
// resolved branch, and shows an empty state otherwise.
func TestNeedsReviewPanel(t *testing.T) {
	html := mustRender(t, fullData())
	panel := section(t, html, "needs-review")
	for _, want := range []string{"#79", "Dispatch helper", "/implement", "afk/79-dispatch"} {
		if !strings.Contains(panel, want) {
			t.Errorf("needs-review missing %q; got:\n%s", want, panel)
		}
	}

	empty := section(t, mustRender(t, DashboardData{}), "needs-review")
	if !strings.Contains(empty, "Nothing waiting for review") {
		t.Errorf("empty needs-review should show an empty state; got:\n%s", empty)
	}
}

// AC: the in-flight panel lists claimed (afk:running) issues with a link to the
// live run, and an empty state otherwise.
func TestInFlightPanel(t *testing.T) {
	html := mustRender(t, fullData())
	panel := section(t, html, "in-flight")
	for _, want := range []string{"#80", "feature/graph-engineering", "actions/runs/123", "running"} {
		if !strings.Contains(panel, want) {
			t.Errorf("in-flight missing %q; got:\n%s", want, panel)
		}
	}

	empty := section(t, mustRender(t, DashboardData{}), "in-flight")
	if !strings.Contains(empty, "No runs in flight") {
		t.Errorf("empty in-flight should show an empty state; got:\n%s", empty)
	}
}

// AC: a failure row carries all D7 hand-back fields — which phase died, the
// run-logs link, the derived skill + branch, and the partial-artifact links.
func TestFailuresPanelHandBackFields(t *testing.T) {
	html := mustRender(t, fullData())
	panel := section(t, html, "failures")
	for _, want := range []string{
		"#81",              // the failed issue
		"agent",            // which phase died
		"actions/runs/456", // run-logs link
		"/research",        // derived skill
		"afk/81-copilot",   // derived branch + partial-artifact label
	} {
		if !strings.Contains(panel, want) {
			t.Errorf("failure row missing hand-back field %q; got:\n%s", want, panel)
		}
	}

	empty := section(t, mustRender(t, DashboardData{}), "failures")
	if !strings.Contains(empty, "No failed runs") {
		t.Errorf("empty failures should show an empty state; got:\n%s", empty)
	}
}

// AC: a failure with no pushed work shows "nothing pushed" rather than an empty
// artifact cell (spec #31).
func TestFailuresPanelNothingPushed(t *testing.T) {
	d := DashboardData{Failures: []FailureRow{
		{Number: 90, Title: "died before push", Phase: "activation", Skill: "/implement", Branch: "afk/90-x"},
	}}
	panel := section(t, mustRender(t, d), "failures")
	if !strings.Contains(panel, "nothing pushed") {
		t.Errorf("a failure with no artifacts should say 'nothing pushed'; got:\n%s", panel)
	}
}

// AC: the recent-runs panel lists runs with their status and terminal outcome,
// distinguishing success from failure, and an empty state otherwise.
func TestRecentRunsPanel(t *testing.T) {
	html := mustRender(t, fullData())
	panel := section(t, html, "recent-runs")
	for _, want := range []string{"afk #79", "success", "afk #81", "failure"} {
		if !strings.Contains(panel, want) {
			t.Errorf("recent-runs missing %q; got:\n%s", want, panel)
		}
	}

	empty := section(t, mustRender(t, DashboardData{}), "recent-runs")
	if !strings.Contains(empty, "No recent runs") {
		t.Errorf("empty recent-runs should show an empty state; got:\n%s", empty)
	}
}

// AC: the open-AFK-PRs panel lists each PR with its branch, the `Part of #<n>`
// linkage, and the D3 outcome comment; empty state otherwise.
func TestOpenPRsPanelOutcomeComment(t *testing.T) {
	html := mustRender(t, fullData())
	panel := section(t, html, "open-prs")
	for _, want := range []string{
		"#82", "afk/79-dispatch", "Part of #79",
		"typecheck: ok", "test: 42 passed", "code-review: clean", // the D3 outcome comment
	} {
		if !strings.Contains(panel, want) {
			t.Errorf("open-PRs missing %q; got:\n%s", want, panel)
		}
	}

	empty := section(t, mustRender(t, DashboardData{}), "open-prs")
	if !strings.Contains(empty, "No open AFK PRs") {
		t.Errorf("empty open-PRs should show an empty state; got:\n%s", empty)
	}
}

// externalRef matches a src= or a <link ... href= whose value is an external
// (absolute or protocol-relative) URL — exactly the auto-fetched resources a
// self-contained page must not have. Anchor (<a href>) navigation to GitHub is
// deliberately allowed and not matched here.
var (
	srcRef  = regexp.MustCompile(`(?i)\ssrc\s*=\s*["'](https?:)?//`)
	linkRef = regexp.MustCompile(`(?i)<link\b[^>]*\bhref\s*=\s*["'](https?:)?//`)
)

// AC: the emitted HTML is self-contained — no external src/href fetches, and
// mermaid.js is inlined (spec #52; prior art view_test.go / preview_test.go).
func TestSelfContainedNoExternalFetches(t *testing.T) {
	html := mustRender(t, fullData())

	if loc := srcRef.FindString(html); loc != "" {
		t.Errorf("HTML must not auto-fetch an external src; found: %q", loc)
	}
	if loc := linkRef.FindString(html); loc != "" {
		t.Errorf("HTML must not link an external stylesheet/resource; found: %q", loc)
	}
	// Mermaid must be inlined, not loaded from a CDN: no <script src>, and the
	// bundle's global-export marker is present in the page.
	if regexp.MustCompile(`(?i)<script\b[^>]*\bsrc\s*=`).MatchString(html) {
		t.Errorf("mermaid must be inlined, not loaded via <script src>")
	}
	if !strings.Contains(html, "globalThis.mermaid") {
		t.Errorf("expected the vendored mermaid bundle to be inlined (its global-export marker is missing)")
	}
	// A sanity check that the inlined bundle is substantial, not a stub.
	if len(mermaidJS) < 500_000 {
		t.Errorf("vendored mermaid bundle looks too small (%d bytes) — is it the real dist?", len(mermaidJS))
	}
}

// AC: the anchor links into GitHub survive (the dashboard's whole purpose is to
// click through), even though external auto-fetches are forbidden.
func TestGitHubAnchorsPreserved(t *testing.T) {
	html := mustRender(t, fullData())
	if !strings.Contains(html, `href="https://github.com/emepetres/life-ledger/issues/79"`) {
		t.Errorf("issue anchor links to GitHub should be preserved")
	}
}
