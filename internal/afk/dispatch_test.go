package afk_test

import (
	"strings"
	"testing"

	"github.com/emepetres/life-ledger/internal/afk"
)

// TestDecideSkillDerivation pins the wayfinder:* → skill mapping and the
// refusal of HITL-only / unknown / ambiguous wayfinder labels. Branch details
// are exercised separately; here only the Skill and Action matter.
func TestDecideSkillDerivation(t *testing.T) {
	tests := []struct {
		name       string
		labels     []string
		wantSkill  afk.Skill
		wantAction afk.Action
	}{
		{
			name:       "research → /research",
			labels:     []string{"afk", "wayfinder:research"},
			wantSkill:  afk.SkillResearch,
			wantAction: afk.ActionRun,
		},
		{
			name:       "task → /implement",
			labels:     []string{"afk", "wayfinder:task"},
			wantSkill:  afk.SkillImplement,
			wantAction: afk.ActionRun,
		},
		{
			name:       "no wayfinder label → /implement",
			labels:     []string{"afk", "ready-for-agent"},
			wantSkill:  afk.SkillImplement,
			wantAction: afk.ActionRun,
		},
		{
			name:       "grilling → refuse",
			labels:     []string{"afk", "wayfinder:grilling"},
			wantSkill:  "",
			wantAction: afk.ActionRefuse,
		},
		{
			name:       "prototype → refuse",
			labels:     []string{"afk", "wayfinder:prototype"},
			wantSkill:  "",
			wantAction: afk.ActionRefuse,
		},
		{
			name:       "map → refuse",
			labels:     []string{"afk", "wayfinder:map"},
			wantSkill:  "",
			wantAction: afk.ActionRefuse,
		},
		{
			name:       "unknown wayfinder value → refuse",
			labels:     []string{"afk", "wayfinder:mystery"},
			wantSkill:  "",
			wantAction: afk.ActionRefuse,
		},
		{
			name:       "two wayfinder labels are ambiguous → refuse",
			labels:     []string{"afk", "wayfinder:task", "wayfinder:research"},
			wantSkill:  "",
			wantAction: afk.ActionRefuse,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := afk.Decide(afk.Input{
				Number: 42,
				Title:  "some ticket",
				Labels: tt.labels,
			})
			if got.Skill != tt.wantSkill {
				t.Errorf("Skill = %q, want %q", got.Skill, tt.wantSkill)
			}
			if got.Action != tt.wantAction {
				t.Errorf("Action = %q, want %q", got.Action, tt.wantAction)
			}
			if got.Reason == "" {
				t.Error("Reason should never be empty")
			}
			// A refusal must never carry a branch to run on.
			if tt.wantAction == afk.ActionRefuse && got.Branch != "" {
				t.Errorf("refused decision carries Branch %q, want empty", got.Branch)
			}
		})
	}
}

// TestDecideBranchLadder walks each rung of the branch-resolution ladder,
// asserting the resolved branch and that the concurrency group always equals
// it.
func TestDecideBranchLadder(t *testing.T) {
	tests := []struct {
		name       string
		in         afk.Input
		wantBranch string
	}{
		{
			name: "rung 1: parent spec declares Branch:",
			in: afk.Input{
				Number: 79,
				Title:  "dispatch helper",
				Labels: []string{"wayfinder:task"},
				Parent: &afk.ParentSpec{
					Number: 77,
					Title:  "Graph Engineering System",
					Body:   "Some spec.\n\nBranch: feature/graph-engineering\n\nMore text.",
				},
				ExistingBranches: []string{"main", "feature/graph-engineering"},
			},
			wantBranch: "feature/graph-engineering",
		},
		{
			name: "rung 1 wins over an existing shared branch",
			in: afk.Input{
				Number: 79,
				Title:  "dispatch helper",
				Labels: []string{"wayfinder:task"},
				Parent: &afk.ParentSpec{
					Number: 77,
					Title:  "Graph Engineering System",
					Body:   "Branch: feature/graph-engineering",
				},
				// The parent-derived shared branch also exists, but the explicit
				// parent Branch: must take precedence.
				ExistingBranches: []string{"main", "afk/77-graph-engineering-system", "feature/graph-engineering"},
			},
			wantBranch: "feature/graph-engineering",
		},
		{
			name: "rung 2: shared spec branch already exists for the parent",
			in: afk.Input{
				Number: 80,
				Title:  "sibling ticket",
				Labels: []string{"wayfinder:task"},
				Parent: &afk.ParentSpec{
					Number: 77,
					Title:  "Graph Engineering System",
					Body:   "No explicit branch here.",
				},
				ExistingBranches: []string{"main", "afk/77-graph-engineering-system"},
			},
			wantBranch: "afk/77-graph-engineering-system",
		},
		{
			name: "rung 3: ticket declares its own Branch: when no parent branch applies",
			in: afk.Input{
				Number: 81,
				Title:  "standalone with own branch",
				Labels: []string{"wayfinder:task"},
				Body:   "Details.\nBranch: `afk/custom-thing`\n",
				// No parent, so rungs 1 & 2 do not apply.
				ExistingBranches: []string{"main"},
			},
			wantBranch: "afk/custom-thing",
		},
		{
			name: "rung 3: ticket's own Branch beats the derived fallback even under a parent without a shared branch",
			in: afk.Input{
				Number: 82,
				Title:  "child names its own branch",
				Labels: []string{"wayfinder:task"},
				Body:   "Branch: afk/child-explicit",
				Parent: &afk.ParentSpec{
					Number: 77,
					Title:  "Graph Engineering System",
					Body:   "No branch, no shared branch created yet.",
				},
				ExistingBranches: []string{"main"},
			},
			wantBranch: "afk/child-explicit",
		},
		{
			name: "rung 4: derive afk/<n>-<slug> from main",
			in: afk.Input{
				Number:           79,
				Title:            "AFK: dispatch helper (internal/afk)",
				Labels:           []string{"wayfinder:task"},
				ExistingBranches: []string{"main"},
			},
			wantBranch: "afk/79-afk-dispatch-helper-internal-afk",
		},
		{
			name: "rung 4: derive from ticket even when parent exists but offers no branch",
			in: afk.Input{
				Number: 90,
				Title:  "orphan-ish child",
				Labels: []string{"wayfinder:task"},
				Parent: &afk.ParentSpec{
					Number: 77,
					Title:  "Graph Engineering System",
					Body:   "no branch",
				},
				ExistingBranches: []string{"main"},
			},
			wantBranch: "afk/90-orphan-ish-child",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := afk.Decide(tt.in)
			if got.Branch != tt.wantBranch {
				t.Errorf("Branch = %q, want %q", got.Branch, tt.wantBranch)
			}
			if got.ConcurrencyGroup != got.Branch {
				t.Errorf("ConcurrencyGroup = %q, want it to equal Branch %q", got.ConcurrencyGroup, got.Branch)
			}
			if got.Action != afk.ActionRun {
				t.Errorf("Action = %q, want run", got.Action)
			}
		})
	}
}

// TestDecideBlockerGate pins the blocker gate: any open blocker forces a skip
// (leaving afk on for a later re-check), yet the resolved skill and branch are
// still reported so a dashboard can show what the run would have been.
func TestDecideBlockerGate(t *testing.T) {
	base := afk.Input{
		Number:           79,
		Title:            "dispatch helper",
		Labels:           []string{"wayfinder:task"},
		ExistingBranches: []string{"main"},
	}

	t.Run("open blocker → skip", func(t *testing.T) {
		in := base
		in.OpenBlockers = []int{70}
		got := afk.Decide(in)
		if got.Action != afk.ActionSkip {
			t.Errorf("Action = %q, want skip", got.Action)
		}
		if got.Skill != afk.SkillImplement {
			t.Errorf("Skill = %q, want %q (still reported on skip)", got.Skill, afk.SkillImplement)
		}
		if got.Branch != "afk/79-dispatch-helper" {
			t.Errorf("Branch = %q, want the resolved branch reported on skip", got.Branch)
		}
		if got.ConcurrencyGroup != got.Branch {
			t.Errorf("ConcurrencyGroup = %q, want %q", got.ConcurrencyGroup, got.Branch)
		}
		if !strings.Contains(got.Reason, "70") {
			t.Errorf("Reason %q should name the open blocker #70", got.Reason)
		}
	})

	t.Run("no open blocker → run", func(t *testing.T) {
		in := base
		in.OpenBlockers = nil
		got := afk.Decide(in)
		if got.Action != afk.ActionRun {
			t.Errorf("Action = %q, want run", got.Action)
		}
	})

	t.Run("refuse takes precedence over an open blocker", func(t *testing.T) {
		in := base
		in.Labels = []string{"wayfinder:grilling"}
		in.OpenBlockers = []int{70}
		got := afk.Decide(in)
		if got.Action != afk.ActionRefuse {
			t.Errorf("Action = %q, want refuse (HITL never runs, blocked or not)", got.Action)
		}
	})
}

// TestSlug pins the slug derivation used for the derived branch name: lowercase,
// non-alphanumeric runs collapse to a single hyphen, edges trimmed, emoji and
// punctuation dropped, and the result truncated without a trailing hyphen.
func TestSlug(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"dispatch helper", "dispatch-helper"},
		{"AFK: dispatch helper (internal/afk)", "afk-dispatch-helper-internal-afk"},
		{"📋 Spec: Graph Engineering System", "spec-graph-engineering-system"},
		{"  leading and trailing  ", "leading-and-trailing"},
		{"multiple   spaces---and__symbols!!!", "multiple-spaces-and-symbols"},
		{"UPPER Case Words", "upper-case-words"},
		{"emoji ✨ only around 🐛 text", "emoji-only-around-text"},
		{"!!!", ""},
		{"a very long title that keeps going and going well beyond the fifty character truncation limit", "a-very-long-title-that-keeps-going-and-going-well"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := afk.Slug(tt.in)
			if got != tt.want {
				t.Errorf("Slug(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
				t.Errorf("Slug(%q) = %q has a leading/trailing hyphen", tt.in, got)
			}
		})
	}
}

// TestSlugEmptyTitleBranchFallback verifies the derived branch stays well-formed
// when the title slugifies to nothing: afk/<n> with no trailing hyphen.
func TestSlugEmptyTitleBranchFallback(t *testing.T) {
	got := afk.Decide(afk.Input{
		Number:           123,
		Title:            "🎉🎉🎉",
		Labels:           []string{"wayfinder:task"},
		ExistingBranches: []string{"main"},
	})
	if got.Branch != "afk/123" {
		t.Errorf("Branch = %q, want %q", got.Branch, "afk/123")
	}
}

// TestParentRef pins the parsing of the "Part of #<n>" linkage the caller uses
// to locate the parent spec before handing its data to Decide.
func TestParentRef(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		wantN  int
		wantOK bool
	}{
		{"simple", "Part of #77.", 77, true},
		{"case-insensitive", "part of #123", 123, true},
		{"mid-body on its own line", "## Parent\n\nPart of #48\n\nmore", 48, true},
		{"no reference", "Just a plain body with #not-a-ref", 0, false},
		{"empty", "", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, ok := afk.ParentRef(tt.body)
			if ok != tt.wantOK || n != tt.wantN {
				t.Errorf("ParentRef(%q) = (%d, %v), want (%d, %v)", tt.body, n, ok, tt.wantN, tt.wantOK)
			}
		})
	}
}

// TestDecideIsPure verifies Decide does not mutate its input slices and is
// deterministic — the same Input yields an equal Decision on repeated calls.
func TestDecideIsPure(t *testing.T) {
	in := afk.Input{
		Number:           79,
		Title:            "dispatch helper",
		Labels:           []string{"afk", "wayfinder:task"},
		ExistingBranches: []string{"main", "afk/77-graph-engineering-system"},
		OpenBlockers:     []int{},
	}
	labelsBefore := strings.Join(in.Labels, ",")
	first := afk.Decide(in)
	second := afk.Decide(in)
	if first != second {
		t.Errorf("Decide not deterministic:\n first  = %+v\n second = %+v", first, second)
	}
	if strings.Join(in.Labels, ",") != labelsBefore {
		t.Error("Decide mutated the input Labels slice")
	}
}
