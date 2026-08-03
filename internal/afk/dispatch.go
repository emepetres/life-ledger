// Package afk holds the pure decision core of the reactive AFK dispatch system
// (spec #77). Its single citizen is Decide, which turns one issue's inputs —
// labels, body, the existing branch list, the parent spec, and the blocker
// state — into the one routing decision that drives a run: which skill to run,
// which branch to run it on, the concurrency group that serialises it, and
// whether to run, skip, or refuse.
//
// It is pure and I/O-free: no network, no gh calls. The activation job supplies
// all GitHub-API data (the parent spec's body, the remote's branch list, the
// open blockers) and shells out to cmd/afk-dispatch, which is a thin wrapper
// over Decide. Keeping the logic here — rather than in fragile injected-value
// bash inside a workflow — is what makes the highest-risk correctness surface
// unit-testable (spec #77, user story 63).
package afk

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Action is the single verb the activation job acts on for a dispatched issue.
type Action string

const (
	// ActionRun: dispatch the derived skill on the resolved branch.
	ActionRun Action = "run"
	// ActionSkip: leave the afk label on and do nothing this pass — a blocking
	// dependency is still open. The next sweep or label event re-checks (no
	// queue, no retry in v1).
	ActionSkip Action = "skip"
	// ActionRefuse: decline the issue and strip afk — the work is HITL-only, or
	// the wayfinder label is unknown/ambiguous, so it must never run headless.
	ActionRefuse Action = "refuse"
)

// Skill is the slash-command the agent runs for a dispatched issue.
type Skill string

// The two skills the system can dispatch. A refused decision carries neither.
const (
	SkillImplement Skill = "/implement"
	SkillResearch  Skill = "/research"
)

// wayfinderPrefix is the sole label namespace the skill is derived from.
const wayfinderPrefix = "wayfinder:"

// ParentSpec is the spec issue a ticket hangs off (via its "Part of #<n>"
// linkage), resolved by the caller from the GitHub API. It feeds the top two
// rungs of the branch-resolution ladder; nil when the ticket names no parent.
type ParentSpec struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

// Input is everything Decide needs about one issue. Every field is data the
// activation job has already fetched — Decide performs no I/O of its own.
type Input struct {
	Number int      `json:"number"`
	Title  string   `json:"title"`
	Labels []string `json:"labels"`
	Body   string   `json:"body"`

	// Parent is the resolved parent spec (nil when the ticket has no "Part of
	// #<n>" linkage). Drives rungs 1–2 of the branch ladder.
	Parent *ParentSpec `json:"parent,omitempty"`

	// ExistingBranches is the set of branch names currently on the remote, used
	// to detect an already-created shared spec branch (rung 2).
	ExistingBranches []string `json:"existingBranches"`

	// OpenBlockers is the numbers of still-open blocking-dependency issues,
	// resolved by the caller from the issue's dependency edges. A non-empty
	// slice gates the run to a skip.
	OpenBlockers []int `json:"openBlockers"`
}

// Decision is the one routing result Decide returns — the shape cmd/afk-dispatch
// emits for the activation job to consume. On a refusal Skill/Branch are empty;
// on a skip or run they report what the run is (or would be), and
// ConcurrencyGroup always equals Branch (per-spec-branch serialisation, spec
// #77 user story 13).
type Decision struct {
	Skill            Skill  `json:"skill"`
	Branch           string `json:"branch"`
	ConcurrencyGroup string `json:"concurrencyGroup"`
	Action           Action `json:"action"`
	Reason           string `json:"reason"`
}

// Decide is the pure decision core. It derives the skill from the wayfinder
// label, resolves the branch by the ladder, and applies the blocker gate,
// returning the single decision that routes the run. Precedence is
// refuse → skip → run: a HITL-only ticket is refused even when blocked, and a
// blocked runnable ticket skips rather than runs.
func Decide(in Input) Decision {
	skill, refuseReason := deriveSkill(in.Labels)
	if skill == "" {
		// HITL-only, unknown, or ambiguous wayfinder label: never run headless.
		return Decision{Action: ActionRefuse, Reason: refuseReason}
	}

	branch, branchReason := resolveBranch(in)

	// Blocker gate: any open blocking dependency leaves the issue afk for a
	// later re-check. The resolved skill/branch are still reported so a
	// dashboard can show what the run would have been.
	if len(in.OpenBlockers) > 0 {
		return Decision{
			Skill:            skill,
			Branch:           branch,
			ConcurrencyGroup: branch,
			Action:           ActionSkip,
			Reason:           fmt.Sprintf("blocked by open dependency %s — leaving afk for a later re-check", formatBlockers(in.OpenBlockers)),
		}
	}

	return Decision{
		Skill:            skill,
		Branch:           branch,
		ConcurrencyGroup: branch,
		Action:           ActionRun,
		Reason:           fmt.Sprintf("%s; %s", skillReason(in.Labels, skill), branchReason),
	}
}

// deriveSkill maps the issue's wayfinder:* label to a skill. Exactly one
// wayfinder label is expected: research → /research; task → /implement; a
// missing label defaults to /implement. grilling/prototype/map are HITL-only
// and refused; an unknown value or two competing labels are refused as well
// (skill "", with the refusal reason). This is the sole place the label
// vocabulary is interpreted.
func deriveSkill(labels []string) (skill Skill, refuseReason string) {
	var wayfinders []string
	for _, l := range labels {
		if suffix, ok := strings.CutPrefix(l, wayfinderPrefix); ok {
			wayfinders = append(wayfinders, suffix)
		}
	}

	switch len(wayfinders) {
	case 0:
		return SkillImplement, ""
	case 1:
		switch w := wayfinders[0]; w {
		case "research":
			return SkillResearch, ""
		case "task":
			return SkillImplement, ""
		case "grilling", "prototype", "map":
			return "", fmt.Sprintf("wayfinder:%s is HITL-only — refusing headless run", w)
		default:
			return "", fmt.Sprintf("unknown wayfinder label wayfinder:%s — refusing", w)
		}
	default:
		return "", fmt.Sprintf("ambiguous: %d wayfinder labels (%s) — refusing", len(wayfinders), strings.Join(wayfinders, ", "))
	}
}

// skillReason explains the run's skill derivation for the Decision.Reason.
func skillReason(labels []string, skill Skill) string {
	for _, l := range labels {
		if strings.HasPrefix(l, wayfinderPrefix) {
			return fmt.Sprintf("%s → %s", l, skill)
		}
	}
	return "no wayfinder label → " + string(skill)
}

// resolveBranch walks the branch-resolution ladder (spec #77 user story 19),
// returning the resolved branch and the reason for the rung that matched:
//
//  1. the parent spec's explicit "Branch:" line;
//  2. an already-created shared spec branch (the parent-derived afk/<pn>-<slug>
//     present on the remote — a sibling opened it first, one PR per spec);
//  3. the ticket's own "Branch:" line;
//  4. otherwise derive afk/<n>-<slug> from the ticket, cut from main.
func resolveBranch(in Input) (branch, reason string) {
	if in.Parent != nil {
		if b := parseBranchLine(in.Parent.Body); b != "" {
			return b, fmt.Sprintf("parent spec #%d declares Branch: %s", in.Parent.Number, b)
		}
		shared := deriveBranch(in.Parent.Number, in.Parent.Title)
		if containsBranch(in.ExistingBranches, shared) {
			return shared, fmt.Sprintf("reusing shared spec branch %s (already exists for parent spec #%d)", shared, in.Parent.Number)
		}
	}
	if b := parseBranchLine(in.Body); b != "" {
		return b, fmt.Sprintf("ticket declares Branch: %s", b)
	}
	b := deriveBranch(in.Number, in.Title)
	return b, fmt.Sprintf("derived %s from main", b)
}

// branchLineRe matches a "Branch:" line anywhere in an issue body, tolerating
// leading whitespace and markdown bold markers around the key. Group 1 is the
// branch name (backticks and surrounding whitespace are stripped afterwards).
var branchLineRe = regexp.MustCompile(`(?mi)^\s*\*{0,2}\s*branch:\s*\*{0,2}\s*(.+?)\s*$`)

// parseBranchLine extracts the branch name from a "Branch: <name>" line, empty
// when the body has none.
func parseBranchLine(body string) string {
	m := branchLineRe.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return strings.Trim(strings.TrimSpace(m[1]), "`")
}

// partOfRe matches the "Part of #<n>" linkage a ticket uses to name its parent
// spec. Case-insensitive; the number is group 1.
var partOfRe = regexp.MustCompile(`(?i)part of #(\d+)`)

// ParentRef extracts the parent spec's issue number from a ticket body's
// "Part of #<n>" linkage. The activation job calls it to locate the parent
// before fetching its data and handing it to Decide as Input.Parent. ok is
// false when the body names no parent.
func ParentRef(body string) (n int, ok bool) {
	m := partOfRe.FindStringSubmatch(body)
	if m == nil {
		return 0, false
	}
	n, _ = strconv.Atoi(m[1])
	return n, true
}

// deriveBranch builds the canonical afk branch name for an issue: afk/<n>-<slug>,
// falling back to afk/<n> when the title slugifies to nothing (so the name never
// ends in a stray hyphen).
func deriveBranch(number int, title string) string {
	if slug := Slug(title); slug != "" {
		return fmt.Sprintf("afk/%d-%s", number, slug)
	}
	return fmt.Sprintf("afk/%d", number)
}

// slugMaxLen caps a derived slug so branch names stay short and readable.
const slugMaxLen = 50

// Slug turns an issue title into a branch-safe slug: lowercase ASCII, with every
// run of non-alphanumeric characters (spaces, punctuation, emoji, accents)
// collapsed to a single hyphen, the edges trimmed, and the whole truncated to
// slugMaxLen with no trailing hyphen. Non-ASCII letters are dropped rather than
// transliterated, keeping the result git-ref-safe.
func Slug(title string) string {
	var b strings.Builder
	prevHyphen := true // leading state: suppress a leading hyphen
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		case !prevHyphen:
			b.WriteByte('-')
			prevHyphen = true
		}
	}
	slug := strings.TrimRight(b.String(), "-")
	if len(slug) > slugMaxLen {
		slug = strings.TrimRight(slug[:slugMaxLen], "-")
	}
	return slug
}

// containsBranch reports whether name is in the branch list.
func containsBranch(branches []string, name string) bool {
	for _, b := range branches {
		if b == name {
			return true
		}
	}
	return false
}

// formatBlockers renders the open-blocker numbers as "#a, #b" for a reason line.
func formatBlockers(nums []int) string {
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = fmt.Sprintf("#%d", n)
	}
	return strings.Join(parts, ", ")
}
