// Command dashboard builds the private AFK dashboard: a single self-contained
// index.html showing the six panels of AFK state (spec #45–#52). It is split in
// two — a thin GitHub-API fetch (this file, via the gh CLI) and the pure
// render(DashboardData) in render.go — so the rendering is fixture-testable
// without a live API or a browser (spec #64). This file holds all the I/O; it
// is the untested shell around the tested core.
//
// The skill and branch a run would use are not re-derived here: this file reuses
// the canonical decision core in internal/afk (spec user story 63), so the
// dashboard can never drift from what the activation job actually decides.
//
// Usage:
//
//	dashboard [output.html]   # default: index.html
//
// It relies on an authenticated `gh` and the repo resolved from the
// GITHUB_REPOSITORY env var (set in Actions) or `gh repo view`.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emepetres/life-ledger/internal/afk"
)

func main() {
	out := "index.html"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}

	data, err := fetch()
	if err != nil {
		fmt.Fprintf(os.Stderr, "dashboard: %v\n", err)
		os.Exit(1)
	}

	html, err := render(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dashboard: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, []byte(html), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "dashboard: writing %s: %v\n", out, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "dashboard: wrote %s\n", out)
}

// The AFK-family labels. An issue carrying any of them is a graph node.
const (
	labelAFK         = "afk"
	labelRunning     = "afk:running"
	labelFailed      = "afk:failed"
	labelNeedsReview = "needs-review"
)

var afkLabels = map[string]bool{
	labelAFK: true, labelRunning: true, labelFailed: true, labelNeedsReview: true,
}

// fetch pulls the whole AFK state from the GitHub API and assembles the pure
// DashboardData. Everything network lives here.
func fetch() (DashboardData, error) {
	repo := os.Getenv("GITHUB_REPOSITORY")
	if repo == "" {
		if b, err := gh("repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner"); err == nil {
			repo = strings.TrimSpace(string(b))
		}
	}

	issues, err := fetchIssues()
	if err != nil {
		return DashboardData{}, err
	}
	prs, err := fetchPRs()
	if err != nil {
		return DashboardData{}, err
	}
	runs, err := fetchRuns()
	if err != nil {
		return DashboardData{}, err
	}

	d := DashboardData{
		GeneratedAt: time.Now().UTC().Format("2006-01-02 15:04 MST"),
		Repo:        repo,
		RecentRuns:  runRows(runs),
		OpenPRs:     prs,
	}

	// The graph nodes are every AFK-labelled issue; edges are blocking references
	// in the body that point at another node.
	nodeSet := map[int]bool{}
	for _, is := range issues {
		nodeSet[is.Number] = true
	}
	for _, is := range issues {
		labels := is.labelNames()
		d.Graph.Nodes = append(d.Graph.Nodes, GraphNode{
			Number: is.Number, Title: is.Title, State: afkState(labels), URL: is.URL,
		})
		for _, blocker := range blockedBy(is.Body) {
			if nodeSet[blocker] {
				d.Graph.Edges = append(d.Graph.Edges, GraphEdge{Blocker: blocker, Blocked: is.Number})
			}
		}

		// The skill and branch come from the canonical decision core, not a local
		// copy, so the dashboard shows exactly what a run would use.
		dec := afk.Decide(afk.Input{Number: is.Number, Title: is.Title, Labels: labels, Body: is.Body})
		skill, branch := string(dec.Skill), dec.Branch

		switch {
		case has(labels, labelFailed):
			d.Failures = append(d.Failures, failureRow(is, skill, branch))
		case has(labels, labelRunning):
			d.InFlight = append(d.InFlight, RunningRow{
				Number: is.Number, Title: is.Title, URL: is.URL,
				Skill: skill, Branch: branch, RunURL: runURLForIssue(runs, is.Number),
			})
		case has(labels, labelNeedsReview):
			d.NeedsReview = append(d.NeedsReview, IssueRow{
				Number: is.Number, Title: is.Title, URL: is.URL,
				Skill: skill, Branch: branch,
			})
		}
	}

	// Stable order: ascending in the graph, newest issue first in the queues.
	sort.Slice(d.Graph.Nodes, func(i, j int) bool { return d.Graph.Nodes[i].Number < d.Graph.Nodes[j].Number })
	sort.Slice(d.NeedsReview, func(i, j int) bool { return d.NeedsReview[i].Number > d.NeedsReview[j].Number })
	sort.Slice(d.InFlight, func(i, j int) bool { return d.InFlight[i].Number > d.InFlight[j].Number })
	sort.Slice(d.Failures, func(i, j int) bool { return d.Failures[i].Number > d.Failures[j].Number })

	return d, nil
}

// ghIssue mirrors the `gh issue list --json` shape we consume.
type ghIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Body   string `json:"body"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (i ghIssue) labelNames() []string {
	names := make([]string, len(i.Labels))
	for k, l := range i.Labels {
		names[k] = l.Name
	}
	return names
}

func fetchIssues() ([]ghIssue, error) {
	b, err := gh("issue", "list", "--state", "open", "--limit", "200",
		"--json", "number,title,url,body,labels")
	if err != nil {
		return nil, err
	}
	var all []ghIssue
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, fmt.Errorf("parsing issues: %w", err)
	}
	// Keep only AFK-family issues.
	var afkIssues []ghIssue
	for _, is := range all {
		for _, l := range is.labelNames() {
			if afkLabels[l] {
				afkIssues = append(afkIssues, is)
				break
			}
		}
	}
	return afkIssues, nil
}

func fetchPRs() ([]PRRow, error) {
	b, err := gh("pr", "list", "--state", "open", "--label", labelAFK, "--limit", "100",
		"--json", "number,title,url,headRefName,body")
	if err != nil {
		return nil, err
	}
	var prs []struct {
		Number      int    `json:"number"`
		Title       string `json:"title"`
		URL         string `json:"url"`
		HeadRefName string `json:"headRefName"`
		Body        string `json:"body"`
	}
	if err := json.Unmarshal(b, &prs); err != nil {
		return nil, fmt.Errorf("parsing PRs: %w", err)
	}
	rows := make([]PRRow, 0, len(prs))
	for _, p := range prs {
		issue, _ := afk.ParentRef(p.Body) // the `Part of #<n>` linkage (spec #24)
		rows = append(rows, PRRow{
			Number: p.Number, Title: p.Title, URL: p.URL, Branch: p.HeadRefName,
			Issue:   issue,
			Outcome: outcomeComment(p.Number),
		})
	}
	return rows, nil
}

// ghRun mirrors the `gh run list --json` shape we consume.
type ghRun struct {
	DisplayTitle string `json:"displayTitle"`
	URL          string `json:"url"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	CreatedAt    string `json:"createdAt"`
}

func fetchRuns() ([]ghRun, error) {
	b, err := gh("run", "list", "--limit", "15",
		"--json", "displayTitle,url,status,conclusion,createdAt")
	if err != nil {
		// Runs are best-effort: a repo with no AFK workflow yet just shows none.
		return nil, nil
	}
	var runs []ghRun
	if err := json.Unmarshal(b, &runs); err != nil {
		return nil, fmt.Errorf("parsing runs: %w", err)
	}
	return runs, nil
}

// runRows projects the fetched runs onto the recent-runs panel view.
func runRows(runs []ghRun) []RunRow {
	rows := make([]RunRow, 0, len(runs))
	for _, r := range runs {
		rows = append(rows, RunRow{
			Title: r.DisplayTitle, URL: r.URL, Status: r.Status,
			Conclusion: r.Conclusion, When: humanWhen(r.CreatedAt),
		})
	}
	return rows
}

// runURLForIssue returns the newest run whose display title references the
// issue (AFK runs are titled per-issue, e.g. "afk #80"), or "" if none — the
// best link the API offers for an in-flight or failed run. The "#<n>" tag is
// matched as a whole token so "#8" does not match "#80".
func runURLForIssue(runs []ghRun, number int) string {
	tag := regexp.MustCompile(`#` + strconv.Itoa(number) + `\b`)
	for _, r := range runs { // gh returns newest first
		if tag.MatchString(r.DisplayTitle) {
			return r.URL
		}
	}
	return ""
}

// failureRow assembles a failure row, enriching it with the D7 hand-back fields
// (which phase died, the run-logs link, and the partial-artifact links) parsed
// from the issue's hand-back comment (spec #31). When no hand-back comment is
// present yet, the fields stay empty and the panel shows "nothing pushed".
func failureRow(is ghIssue, skill, branch string) FailureRow {
	row := FailureRow{
		Number: is.Number, Title: is.Title, URL: is.URL,
		Skill: skill, Branch: branch,
	}
	if hb := handBack(is.Number); hb != nil {
		row.Phase = hb.phase
		row.RunURL = hb.runURL
		row.Artifacts = hb.artifacts
	}
	return row
}

// outcomeComment returns the D3 typecheck/test/code-review outcome comment for a
// PR, identified by its content, or "" if none is posted yet.
func outcomeComment(pr int) string {
	comments, err := comments("pr", pr)
	if err != nil {
		return ""
	}
	// The finalize step posts the outcome; take the most recent matching comment.
	for i := len(comments) - 1; i >= 0; i-- {
		body := comments[i]
		if strings.Contains(body, "typecheck") || strings.Contains(strings.ToLower(body), "code-review") {
			return body
		}
	}
	return ""
}

// handBackInfo is the D7 hand-back fields parsed from a failure comment.
type handBackInfo struct {
	phase     string
	runURL    string
	artifacts []Link
}

var (
	runLogRe = regexp.MustCompile(`https://github\.com/[^/\s]+/[^/\s]+/actions/runs/\d+`)
	phaseRe  = regexp.MustCompile(`(?i)phase[:\s]+\*{0,2}(activation|agent|finalize)`)
	refURLRe = regexp.MustCompile(`https://github\.com/[^/\s]+/[^/\s]+/(?:tree|compare|pull)/[^\s)]+`)
)

// handBack parses the most recent D7 hand-back comment on a failed issue for the
// phase that died, the run-logs link, and any partial-artifact links (spec #31).
// Returns nil when no hand-back comment is found.
func handBack(issue int) *handBackInfo {
	comments, err := comments("issue", issue)
	if err != nil {
		return nil
	}
	for i := len(comments) - 1; i >= 0; i-- {
		body := comments[i]
		run := runLogRe.FindString(body)
		phase := ""
		if m := phaseRe.FindStringSubmatch(body); m != nil {
			phase = strings.ToLower(m[1])
		}
		if run == "" && phase == "" {
			continue // not a hand-back comment
		}
		hb := &handBackInfo{phase: phase, runURL: run}
		for _, u := range refURLRe.FindAllString(body, -1) {
			hb.artifacts = append(hb.artifacts, Link{Label: shortRef(u), URL: u})
		}
		return hb
	}
	return nil
}

// comments returns the bodies of the comments on an issue or PR, oldest first.
func comments(kind string, number int) ([]string, error) {
	b, err := gh(kind, "view", strconv.Itoa(number), "--json", "comments")
	if err != nil {
		return nil, err
	}
	var v struct {
		Comments []struct {
			Body string `json:"body"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	out := make([]string, len(v.Comments))
	for i, c := range v.Comments {
		out[i] = c.Body
	}
	return out, nil
}

// --- pure helpers over fetched text -----------------------------------------

// afkState picks the most specific AFK state label present, in priority order.
func afkState(labels []string) string {
	switch {
	case has(labels, labelFailed):
		return labelFailed
	case has(labels, labelRunning):
		return labelRunning
	case has(labels, labelNeedsReview):
		return labelNeedsReview
	default:
		return labelAFK
	}
}

var (
	blockedByRe = regexp.MustCompile(`(?mi)blocked by[^\n]*?#\d+`)
	hashRe      = regexp.MustCompile(`#(\d+)`)
)

// blockedBy extracts the issue numbers this issue is blocked by, from a
// "Blocked by #N, #M" line in the body — the edges the dependency graph draws.
func blockedBy(body string) []int {
	var out []int
	for _, line := range strings.Split(body, "\n") {
		if !blockedByRe.MatchString(line) {
			continue
		}
		for _, m := range hashRe.FindAllStringSubmatch(line, -1) {
			if n, err := strconv.Atoi(m[1]); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

// shortRef trims a GitHub tree/compare/pull URL to its trailing identifier for a
// compact artifact label.
func shortRef(u string) string {
	if i := strings.LastIndexByte(u, '/'); i >= 0 && i < len(u)-1 {
		return u[i+1:]
	}
	return u
}

func humanWhen(iso string) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
	}
	return t.UTC().Format("2006-01-02 15:04")
}

func has(labels []string, name string) bool {
	for _, l := range labels {
		if l == name {
			return true
		}
	}
	return false
}

// gh runs the gh CLI and returns stdout, surfacing stderr on failure.
func gh(args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
