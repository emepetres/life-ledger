# Graph Engineering System

The living system map for life-ledger's **away-from-keyboard (AFK)** dispatch —
the machinery that turns a single label on a ready issue into an autonomous
agent run, with a draft PR (or committed research findings) waiting for review
when it finishes. It links out to the parent spec
([#77](https://github.com/emepetres/life-ledger/issues/77)) for the *why* and
the full decision record; this document is the *what and how*, and every later
ticket appends its own section here as it lands.

> This is the hub doc. The v1 build is split across implementation tickets under
> #77; each one grows this file rather than starting a new one.

## Premise

I am the sole maintainer. I have adopted the Matt Pocock skills workflow (triage
→ wayfinder → grill/prototype → to-spec → to-tickets → implement/research). The
human-in-the-loop (HITL) parts of that loop genuinely need me. The AFK parts —
running `/implement` on a specified, unblocked ticket, or `/research` on a
research ticket — do not: the way has already been cleared, so all that remains
is mechanical execution I can delegate.

The system's promise, in one line:

> **One persistent `afk` label on a ready issue is the only human action. An
> agent wakes on the label, runs the right skill headlessly inside GitHub
> Actions, and hands the result back in a reviewable state.**

I apply the label — from my machine or my phone — and walk away. I come back to a
draft PR to review and merge, or a research findings file to read. Closure and
merge stay human.

## v1 scope: reactive, per-issue

v1 is deliberately the simplest thing that works:

- **Reactive, per-issue.** One label event (or one manual sweep) dispatches one
  agent for one ready, unblocked issue. There is **no** autonomous graph
  traversal — newly-unblocked tickets are not auto-advanced. That is an
  explicit later feature.
- **Engine-agnostic by construction.** v1 ships on `engine: claude` with a real
  Anthropic key, but the secrets and skill-install paths are structured so the
  same workflow runs under GitHub Copilot (BYOK to any OpenAI-compatible
  endpoint), validated as the terminal ticket. The seam is the engine-neutral
  secret pair `LLM_API_KEY` / `LLM_BASE_URL`, mapped per-engine in a small
  `engine.env` block.
- **Human gates preserved.** PRs are draft-only, prefixed `[afk] `, linked to
  their issue with `Part of #<n>` (never `Fixes`, so nothing auto-closes). The
  gate to `main` is always a human review plus branch protection.

The one-label entry point resolves, per issue, to: **which skill** (`/implement`
for task tickets, `/research` for research tickets — derived from the
`wayfinder:*` label), **which branch** to work on, and **whether a blocking
dependency is still open** (if so, skip silently and leave `afk` for the next
sweep). HITL-only wayfinder kinds (`grilling`, `prototype`, `map`) are refused:
the agent declines and the `afk` label is stripped.

The full state machine, three-phase token split, result handling, failure
hand-back, dashboard, and the two testable Go modules are specified in
[#77](https://github.com/emepetres/life-ledger/issues/77) and will be documented
here section-by-section as each ticket lands.

## Provisioning & prerequisites

Everything below is **maintainer-only** and cannot be automated by the workflow
itself — it is repo configuration and secrets that must exist before any AFK
ticket can run. Treat this as a tick-box exercise; the system has a clean
foundation once every box is checked.

Shell snippets are **PowerShell Core (`pwsh`)** — the maintainer's runtime is
Windows.

### Repo Actions secrets

Set as **repository** Actions secrets (Settings → Secrets and variables →
Actions), not environment or organization secrets. The base URLs are stored as
secrets too, so the whole provider binding lives in one place and switching
engines is a config change, not a code change.

**Production** (v1, `engine: claude`):

- [ ] **`LLM_API_KEY`** — the Anthropic API key. Mapped to `ANTHROPIC_API_KEY`
      in the workflow's `engine.env` block. Injected into the **agent job only**,
      so a leak's blast radius is one read-only job.
- [ ] **`LLM_BASE_URL`** — the provider base URL. For direct Anthropic this is
      `https://api.anthropic.com`; it exists as a secret so a proxy or an
      OpenAI-compatible endpoint can be swapped in without editing the workflow.

**Copilot validation** (terminal engine-agnostic ticket — distinct secrets keep
the throwaway validation run fully isolated from production):

- [ ] **`LLM_VALIDATION_API_KEY`** — the API key for the OpenAI-compatible
      endpoint used to validate the Copilot + BYOK path.
- [ ] **`LLM_VALIDATION_BASE_URL`** — the base URL of that validation endpoint.

Set a secret with:

```pwsh
$LLM_API_KEY | gh secret set LLM_API_KEY
```

(`gh` reads the value from stdin and trims the trailing newline. Never paste a
key onto the command line, where it would land in shell history.)

### Repo settings

- [ ] **Enable "Allow GitHub Actions to create and approve pull requests."**
      Settings → Actions → General → Workflow permissions. This is **currently
      off**. While off, an `/implement` run cannot open a PR and silently
      degrades to posting an issue comment instead — the run looks like it
      "worked" but produces nothing reviewable. This box **must** be checked for
      the PR path to function.

### Branch protection

- [ ] **Confirm `main` branch protection requires human review.** Settings →
      Branches → branch protection rule for `main`. This is the no-self-merge
      gate: AFK PRs are draft-only and are never merged or self-approved by the
      agent, so a required human review on `main` is what guarantees no
      autonomous change reaches the default branch. Without it, the draft-only
      convention is the *only* thing standing between an agent and `main` —
      make it an enforced rule, not a convention.

### Labels

- [x] **`afk:failed`** — created, colour `#b60205` (dark red), distinct from
      `needs-review` (`#d93f0b`). Marks a run that failed at some phase and is
      awaiting a human re-trigger.

The other four AFK labels already exist in the repo and need no action:

| Label         | Colour    | Meaning                                                        |
| ------------- | --------- | -------------------------------------------------------------- |
| `afk`         | `#0e8a16` | Cleared for autonomous execution — the one-label entry point.  |
| `afk:running` | `#fbca04` | Run in progress; the claim marker (`afk` → `afk:running`).     |
| `needs-review`| `#d93f0b` | Run finished successfully — awaiting human validation.         |
| `wayfinder:*` | (various) | The wayfinder set the skill is derived from (`task`, `research`, `grilling`, `prototype`, `map`). |

### Dashboard config knobs (later ticket)

Recorded here so provisioning is complete; wired when the dashboard ticket
lands. Both default to the zero-infra, no-repo-write path:

- [ ] **Pages-enable flag** — default **off**. Only meaningful on a public repo.
      When off, the dashboard is an Actions job summary plus a downloadable HTML
      artifact. When on, the same HTML is published to GitHub Pages.
- [ ] **Dashboard cron line** — the refresh cadence for the read-only
      `dashboard.yml` (in addition to its event triggers). Default daily.
