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
- **Engine-agnostic by construction.** v1 ships on `engine: copilot` in **BYOK**
  mode against an OpenAI-compatible endpoint (Anthropic direct API keys are not
  provisionable under the maintainer's corporate account, so Copilot BYOK is the
  simplest engine that runs on available credentials). The secrets and
  skill-install paths are structured so the same workflow runs under a **second
  engine** — validated under `engine: opencode` (GitHub-routed Copilot models via
  `COPILOT_GITHUB_TOKEN`) as the terminal ticket, a genuinely distinct auth path.
  The seam is the engine-neutral secret pair `LLM_API_KEY` / `LLM_BASE_URL`,
  mapped per-engine in a small `engine.env` block (copilot →
  `COPILOT_PROVIDER_API_KEY` / `COPILOT_PROVIDER_BASE_URL` + `COPILOT_PROVIDER_TYPE: openai`).
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

**Production** (v1, `engine: copilot` BYOK):

- [x] **`LLM_API_KEY`** — the API key for the **OpenAI-compatible endpoint**.
      Mapped to `COPILOT_PROVIDER_API_KEY` in the workflow's `engine.env` block.
      Injected into the **agent job only**, so a leak's blast radius is one
      read-only job — and under BYOK gh-aw isolates the real credential in its
      API-proxy sidecar, so the agent process never sees it.
- [x] **`LLM_BASE_URL`** — the provider base URL. Mapped to
      `COPILOT_PROVIDER_BASE_URL`; its host must also appear in `network.allowed`.
      It lives as a secret so the endpoint can be swapped without editing the
      workflow.
- [x] **`LLM_MODEL`** — a repository **Actions variable** (not a secret;
      Variables tab), the model the endpoint serves. Mapped to `COPILOT_MODEL` in
      `engine.env` as `${{ vars.LLM_MODEL }}` (required for most BYOK providers).
      A variable, not a literal, so switching model is a Settings change that
      resolves at runtime — no `.lock.yml` recompile. Set it with
      `gh variable set LLM_MODEL --body "<model>"` (non-secret, safe on the command
      line). `COPILOT_PROVIDER_TYPE: openai` stays a literal. This keeps the whole
      per-engine binding to the neutral `LLM_*` names in a ~4-line `engine.env`.

**opencode validation** (terminal engine-agnostic ticket — a second, genuinely
distinct auth path keeps the throwaway validation run fully isolated from
production):

- [ ] **`COPILOT_GITHUB_TOKEN`** — a GitHub token with Copilot access (org
      Copilot subscription), consumed by `engine: opencode` with
      `model: copilot/<model>`. No OpenAI-compatible endpoint secret is involved,
      so validation exercises a different credential path than production. (May
      instead be satisfied by the `copilot-requests: write` permission on the
      throwaway validation workflow — resolved when #87 is worked.)

Set a secret with:

```pwsh
$LLM_API_KEY | gh secret set LLM_API_KEY
```

(`gh` reads the value from stdin and trims the trailing newline. Never paste a
key onto the command line, where it would land in shell history.)

### Repo settings

- [x] **Enable "Allow GitHub Actions to create and approve pull requests."**
      Settings → Actions → General → Workflow permissions. This is **currently
      off**. While off, an `/implement` run cannot open a PR and silently
      degrades to posting an issue comment instead — the run looks like it
      "worked" but produces nothing reviewable. This box **must** be checked for
      the PR path to function.

### Branch protection

- [x] **Protect `main` so no change lands without a human merge.** This is the
      no-self-merge gate: AFK PRs are draft-only and are never merged or
      self-approved by the agent, so an enforced rule on `main` is what guarantees
      no autonomous change reaches the default branch. Without it, the draft-only
      convention is the *only* thing standing between an agent and `main` — make
      it an enforced rule, not a convention.

  **How to create the rule** (GitHub **Rulesets** — the current mechanism;
  Settings → Rules → Rulesets → **New ruleset → New branch ruleset**):

  1. **Name** it (e.g. `protect-main`) and set **Enforcement status: Active**.
  2. **Target branches → Add target → Include default branch** (resolves to
     `main`).
  3. Under **Rules**, tick **Require a pull request before merging.** This alone
     blocks direct pushes to `main`, so every change — including an AFK draft PR —
     can only reach `main` through a merge that *you* click. Combined with the
     three-job token split (the finalize job has no merge capability and PRs are
     draft), the agent structurally cannot merge itself.
  4. **Required approvals — read this before setting it.** GitHub does **not**
     let you approve your *own* PR. As the **sole maintainer** you author the
     merge, so setting required approvals ≥ 1 would also lock *you* out of merging
     AFK PRs (there is no one else to approve). So:
       - **Solo maintainer:** leave **Required approvals: 0**. The gate is the
         mandatory-PR rule plus your manual **Merge** click — the agent never gets
         that far because it only ever opens a *draft*.
       - **If you ever add collaborators:** raise to **1** (and optionally enable
         *Dismiss stale approvals* / *Require review from Code Owners*) so a second
         human must approve.
  5. Also tick **Block force pushes.**
  6. **Bypass list: leave it empty.** Do **not** add the Actions/AFK workflow
     token as a bypass actor — that would defeat the gate.
  7. **Create.**

  (The classic **Settings → Branches → Add branch protection rule** for `main`
  works too — same knobs: *Require a pull request before merging*, approvals per
  the caveat above, *Do not allow bypassing*. Rulesets are GitHub's newer,
  recommended path.)

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

## Dispatch logic

How a single issue is routed to a run. The decision is a **pure function** —
`afk.Decide` in [`internal/afk`](../../internal/afk/dispatch.go) — over data the
activation job has already fetched from the GitHub API (labels, body, the parent
spec, the remote's branch list, the open blockers). No network, no `gh` calls
inside the unit; the thin [`cmd/afk-dispatch`](../../cmd/afk-dispatch/main.go)
wrapper marshals JSON in and the decision out. This keeps the highest-risk
correctness surface unit-tested rather than trapped in fragile injected-value
bash (user story 63).

`Decide` returns one decision:

```
{ skill, branch, concurrencyGroup, action, reason }
```

- **`action`** is `run`, `skip`, or `refuse`.
- **`concurrencyGroup` always equals `branch`** — per-spec-branch serialisation,
  so two agents heading for the same PR branch never race (user story 13).
- On a **refusal**, `skill` and `branch` are empty (there is no run). On a
  **skip** or **run**, both report what the run is (or would be), so the
  dashboard can show a skipped issue's intended routing.

Precedence is **refuse → skip → run**: a HITL-only ticket is refused even when
it is also blocked, and a blocked-but-runnable ticket skips rather than runs.

### Skill derivation

The skill comes from the issue's single `wayfinder:*` label:

| `wayfinder:*` label           | Outcome                             |
| ----------------------------- | ----------------------------------- |
| `wayfinder:research`          | `run` → `/research`                 |
| `wayfinder:task`              | `run` → `/implement`                |
| _(no wayfinder label)_        | `run` → `/implement` (the default)  |
| `wayfinder:grilling`          | `refuse` — HITL-only                |
| `wayfinder:prototype`         | `refuse` — HITL-only                |
| `wayfinder:map`               | `refuse` — HITL-only                |
| any other `wayfinder:<value>` | `refuse` — unknown label            |
| two or more `wayfinder:*`      | `refuse` — ambiguous               |

`grilling` / `prototype` / `map` are human-in-the-loop work and must never run
headless (user story 7). An unknown or ambiguous label is refused for the same
reason — the system routes only what it understands.

### Branch-resolution ladder

The branch a run works on is resolved by a four-rung ladder, first match wins
(user story 19). The intent is **one PR per spec**: tickets under one spec
converge onto one branch.

1. **Parent spec's `Branch:` line.** If the ticket has a parent spec (via its
   `Part of #<n>` linkage) and that spec's body contains a `Branch: <name>`
   line, use `<name>`. The explicit declaration is authoritative — it wins even
   when a shared branch already exists.
2. **Shared spec branch.** Otherwise, if the parent-derived branch
   `afk/<parent-n>-<parent-slug>` already exists on the remote — a sibling
   ticket opened it first — reuse it, so siblings accumulate on one PR.
3. **Ticket's own `Branch:` line.** Otherwise, if the ticket's own body contains
   a `Branch: <name>` line, use `<name>`.
4. **Derive from `main`.** Otherwise derive `afk/<n>-<slug>` from the ticket's
   own number and title, cut from `main`, and let the finalize phase open a
   draft PR against `main`.

A `Branch:` line is matched anywhere in the body, case-insensitively, tolerating
markdown bold markers and backticks around the value (`` **Branch:** `x` ``
reads as `x`).

### Slug derivation

The `<slug>` in a derived branch (`afk/<n>-<slug>`) comes from the issue title:

- lowercased;
- every run of non-alphanumeric characters (spaces, punctuation, emoji,
  accented letters) collapsed to a single `-`;
- leading and trailing `-` trimmed;
- truncated to 50 characters, with no trailing `-`.

Non-ASCII letters are **dropped**, not transliterated, keeping the result a safe
git ref. When a title slugifies to nothing (e.g. emoji-only), the branch falls
back to `afk/<n>` with no trailing hyphen.

Examples:

| Title                                 | Derived branch (issue #79)                |
| ------------------------------------- | ----------------------------------------- |
| `AFK: dispatch helper (internal/afk)` | `afk/79-afk-dispatch-helper-internal-afk` |
| `📋 Spec: Graph Engineering System`   | `afk/79-spec-graph-engineering-system`    |
| `🎉🎉🎉`                              | `afk/79`                                  |

### Blocker gate

If any blocking-dependency edge points at a still-open issue, the decision is
`skip`: the `afk` label is **left in place** and nothing runs this pass. There
is no queue and no retry in v1 — the next `workflow_dispatch` sweep or `labeled`
event re-checks the gate (user stories 10, 11). The skipped decision still
reports the resolved skill and branch, and its `reason` names the open
blocker(s), so the state is visible rather than silent.

The activation job supplies the open-blocker numbers; the unit only decides on
them, keeping the gate itself testable.

## Dashboard: data model & rendering

The private maintainer view of the whole AFK graph (user stories 45–52). Like
the dispatch logic, the risky part is pushed down into a **pure, fixture-testable
function** so the six panels are verified without a live GitHub API or a browser
(user story 64). The generator is [`cmd/dashboard`](../../cmd/dashboard), split
in two along that seam:

- A **thin GitHub-API fetch** — [`main.go`](../../cmd/dashboard/main.go), all the
  I/O — shells out to the `gh` CLI to pull open AFK issues, open `afk` PRs (with
  their comments), and the recent run list, then assembles a `DashboardData`.
  It is the untested shell; it holds every `gh` call and the clock.
- A **pure `render(DashboardData) → html`** — [`render.go`](../../cmd/dashboard/render.go) —
  that turns that data into one self-contained `index.html` via `html/template`.
  No network, no clock, no filesystem beyond the compile-time-embedded Mermaid
  bundle, so it is deterministic under test.

The dashboard is **rebuilt statelessly from the API each time** (graph, PRs, and
run list re-derived on every build), so it can never drift out of sync with
reality (user story 48). It is driven by a decoupled, read-only `dashboard.yml`
that can never conflict with an AFK run (user story 49).

### `DashboardData`

`DashboardData` shapes the dependency graph plus one field per panel — the fetch
fills it, the renderer only reads it:

```
DashboardData{
  GeneratedAt, Repo string      // header chrome (timestamp supplied by the caller)
  Graph       DependencyGraph   // Nodes []GraphNode + Edges []GraphEdge
  NeedsReview []IssueRow        // the needs-review queue
  InFlight    []RunningRow      // claimed afk:running issues + live-run link
  Failures    []FailureRow      // afk:failed issues with the D7 hand-back fields
  RecentRuns  []RunRow          // the tail of the AFK run list
  OpenPRs     []PRRow           // open [afk] PRs with the D3 outcome comment
}
```

A `GraphEdge` is a blocking-dependency edge `{Blocker, Blocked}`, rendered as
`Blocker --> Blocked` so the arrow reads "must finish before". A `GraphNode`
carries its AFK `State`, which colours the node.

### The six panels and their states

| Panel                | Source field  | Populated state                                                            | Empty state              |
| -------------------- | ------------- | -------------------------------------------------------------------------- | ------------------------ |
| **Dependency graph** | `Graph`       | A hand-drawn Mermaid flowchart, one node per AFK issue, coloured by state. | "No AFK issues in the graph." (no Mermaid block emitted) |
| **Needs review**     | `NeedsReview` | Issue · derived skill · resolved branch.                                   | "Nothing waiting for review." |
| **In flight**        | `InFlight`    | Issue · skill · branch · link to the live run.                             | "No runs in flight."     |
| **Failures**         | `Failures`    | The full D7 hand-back: which **phase** died, **run-logs** link, derived **skill** + **branch**, and partial-artifact links — or **"nothing pushed"** when nothing was pushed. | "No failed runs." |
| **Recent runs**      | `RecentRuns`  | Run · status · terminal result (success/failure) · when.                   | "No recent runs."        |
| **Open AFK PRs**     | `OpenPRs`     | PR · branch · `Part of #<n>` linkage · the **D3 outcome comment** (in-agent typecheck / test / code-review). | "No open AFK PRs."       |

Node state colours: `afk` (blue), `afk:running` (amber), `needs-review` (green),
`afk:failed` (red) — the same vocabulary as the [dispatch](#dispatch-logic)
state machine.

### Vendored Mermaid & the self-contained guarantee

The dependency graph uses Mermaid's **`look: handDrawn`** style, set via the
diagram's config frontmatter. To keep the HTML self-contained with **zero
external fetches** (user story 52), the full mermaid.js bundle is **vendored and
inlined**: [`cmd/dashboard/vendor/mermaid-11.4.1.min.js`](../../cmd/dashboard/vendor)
is `go:embed`ed and written verbatim into a plain `<script>` tag (as
`template.JS`, so `html/template` inlines rather than escapes it). The dist build
assigns `globalThis.mermaid`, so no module loader or CDN is involved.

The Mermaid source is emitted into a `<pre class="mermaid">` block. Because the
browser decodes an element's `textContent`, `html/template`'s HTML-escaping of
that block is **both XSS-safe and Mermaid-correct** — `-->` survives as `-->`,
and a double quote in an issue title is down-quoted so it can't break a node
label. Styles are inlined in a `<style>` block; the favicon is a `data:` URI. The
only external references are the `<a href>` navigation links into GitHub, which
are user-initiated clicks, not auto-fetched resources.

A test (`render_test.go`) asserts this guarantee directly: no `src=` or `<link …
href=>` points at an external URL, no `<script src>` exists, and the inlined
bundle's `globalThis.mermaid` export marker is present — the same
rendered-output discipline as `internal/server/view_test.go` and
`preview_test.go`.

## Workflow topology & the `/implement` path

The production workflow is a [gh-aw](https://github.github.com/gh-aw/) agentic
workflow: a Markdown file with YAML frontmatter,
[`.github/workflows/afk.md`](../../.github/workflows/afk.md), that `gh aw compile`
turns into a hardened, SHA-pinned `.github/workflows/afk.lock.yml`. **Both files
are committed**, and recompilation is a cheap always-on check — if the `.md` and
`.lock.yml` drift, the run fails a lock-file staleness gate. Compile it with:

```pwsh
gh aw compile .github/workflows/afk.md
```

One `afk` label on a ready task issue drives the whole happy path, from the label
to a **draft PR against `main` waiting for review**.

### The three phases and the token split

The run is three phases, and the defining invariant is that **the LLM provider
key and a repo-write token never live in the same job** (spec #77, user stories
36–37). gh-aw's native activation → agent → safe-outputs architecture gives this
for free; the AFK-specific logic slots into it:

| Phase | Compiled job(s) | Token scope | What it does |
| ----- | --------------- | ----------- | ------------ |
| **Dispatch** (activation) | custom `dispatch` job | `issues: write` (no LLM key) | Role-gated to maintainers. Derives skill + branch + concurrency group via [`internal/afk`](../../internal/afk/dispatch.go) (shelled out through [`cmd/afk-dispatch`](../../cmd/afk-dispatch/main.go)), claims the issue (`afk` → `afk:running`), and clears stale `afk:failed` / `needs-review`. |
| **Agent** | `agent` job | **read-only** (`contents`/`issues`/`pull-requests: read`) + the LLM key | Checks out the resolved branch, loads the ticket with `gh issue view <n> --comments`, runs the inlined `/implement` method headlessly, and emits results **only** through safe-outputs. |
| **Finalize** | generated `safe_outputs` job | `contents`/`issues`/`pull-requests: write` (no LLM key) | Executes the agent's safe-output requests: opens/updates the draft PR, posts the outcome comment, lands the issue in `needs-review`. |

The **maintainer role-gate** is enforced in gh-aw's `pre_activation` job
(`required-roles: admin, maintainer`); the `dispatch` job is gated on its
`activated` output, so a non-maintainer's drive-by `afk` label **mutates no state
and incurs no AI spend** — it is refused before the first `issues: write` call.
The `agent` job is additionally gated on `needs.dispatch.outputs.action == 'run'`,
so a `skip` (blocked) or `refuse` (HITL-only) decision from `internal/afk` never
reaches the engine.

The token split is verifiable straight from the `.lock.yml`: the
`COPILOT_PROVIDER_API_KEY` / `COPILOT_PROVIDER_BASE_URL` bindings appear only in
the `agent` job (and gh-aw's own `detection` scan), never in `dispatch` or
`safe_outputs`; and `contents: write` appears only in `safe_outputs`.

### The engine-neutral secret seam

The `engine:` block is the **only** part that changes when swapping engines
(spec #77, user stories 54–56). v1 ships `engine: copilot` in **BYOK** mode
against an OpenAI-compatible endpoint, with a ~4-line `engine.env` that maps the
engine-neutral secrets to the copilot provider vars:

```yaml
engine:
  id: copilot
  max-turns: 30
  env:
    COPILOT_PROVIDER_BASE_URL: ${{ secrets.LLM_BASE_URL }}   # activates BYOK
    COPILOT_PROVIDER_API_KEY: ${{ secrets.LLM_API_KEY }}     # sidecar-isolated
    COPILOT_PROVIDER_TYPE: openai                            # OpenAI-compatible
    COPILOT_MODEL: ${{ vars.LLM_MODEL }}                     # a repo *variable*
```

`LLM_MODEL` is a repo **Actions variable**, not a secret, so the model swaps via
Settings **without a `.lock.yml` recompile**. Under BYOK, gh-aw isolates the real
credential in its API-proxy sidecar, so the agent process never sees the key.

The provider **host must be listed as a literal** in `network.allowed`
(`forge.plainconcepts.com`) — this is load-bearing, not optional. `network.allowed`
is a compile-time, reviewer-auditable security artifact and **rejects every
`${{ }}` expression**, secret *or* variable (the compiler errors with
`domain pattern contains invalid character '$'`). And gh-aw does **not**
auto-extract the host when the base URL comes from a secret — verified in the
`.lock.yml`, where the firewall `allowDomains` omits any host it can only learn at
runtime. Without the literal, the firewall blocks the BYOK api-proxy's upstream
call and the run fails. The host is not a credential (only `LLM_API_KEY` is), so
listing it is safe; the full URL still lives in the `LLM_BASE_URL` secret that
feeds `COPILOT_PROVIDER_BASE_URL`. The egress allowlist is therefore `defaults` +
the `go` ecosystem + the provider host. The invariant `/implement` prompt lives in
**one shared import**,
[`shared/afk-implement-method.md`](../../.github/workflows/shared/afk-implement-method.md),
`{{#runtime-import}}`ed into the body — the single source of truth for agent
behavior, shared verbatim by any second-engine validation copy.

Skills load from the checked-out repo's own `.claude/skills` (claude
auto-discovery) and `.agents/skills` (Copilot local-install) copies — there is
**no `skills:` frontmatter** pulling from upstream `mattpocock/skills`.

> **Toolchain note.** The spec inherited a "runs `npm ci` in setup" line from the
> generic sandcastle reference, but life-ledger is a **Go** module (no
> `package.json`). The inlined method uses the Go toolchain instead: `go build` /
> `go vet` for the typecheck, `go test ./...` for the suite, `gofmt -l .` for the
> format gate — mirroring [`ci-cd.yml`](../../.github/workflows/ci-cd.yml).

### Skill invocation — inlined method, referenced sub-skills

`/implement` is `disable-model-invocation: true`, so a headless engine cannot fire
it by name (see [`docs/research/sandcastle-reference.md`](../research/sandcastle-reference.md)).
Its method is therefore **inlined** into the shared prompt, which references the
model-invocable `/tdd` (red → green → refactor at pre-agreed seams) and
`/code-review` (Standards + Spec axes) **by name** so the agent loads them from
the checkout. The agent commits only; it pushes nothing and edits no labels —
every side effect is a safe-output request the finalize job executes.

> **Recorded limitation (not a gate).** Under a headless engine, `/code-review`'s
> parallel sub-agents degrade to a **single inline pass**; the outcome comment
> says so. This is a documented v1 limitation, consistent across the Copilot
> production engine and the opencode validation engine.

### Branch resolution in practice, and the PR create-vs-push rule

The `dispatch` phase resolves the branch by the
[branch-resolution ladder](#branch-resolution-ladder) and hands it to the agent as
`needs.dispatch.outputs.branch`. Before the engine starts, an agent-job step puts
the workspace on that branch — **tracking the remote branch when it already
exists** (a sibling ticket on a shared spec branch, rung 2), else **creating it
from `main`**. That agent-job step runs, on the Linux Actions runner (an excerpt
of the compiled workflow, not a command the reader runs):

```
if git ls-remote --exit-code --heads origin "$BRANCH"; then
  git fetch origin "$BRANCH"; git checkout -B "$BRANCH" "origin/$BRANCH"
else
  git checkout -B "$BRANCH"
fi
```

Finalize forks on whether a PR already exists for that head branch — **one rule,
two shapes** (spec #77, D3):

- **No open PR for the branch** → the agent emits **`create-pull-request`**: a
  **draft** PR to `main`, title prefixed `[afk] `, labelled `afk`, its body
  linking the issue with **`Part of #<n>`** (never `Fixes`/`Closes`, so nothing
  auto-closes — `auto-close-issue: false`).
- **An open PR already exists** → the agent emits
  **`push-to-pull-request-branch`**, accumulating commits onto that one PR
  ("one PR per spec branch" is emergent, not enforced).

Either way the PR is **draft-only, never merged or self-approved**: the gate to
`main` is a human review plus branch protection. An **empty diff** is a valid
success sub-shape (`if-no-changes: warn`) — the outcome comment says nothing
changed and the issue still lands in `needs-review`.

Concurrency is controlled at two levels. A **repo-wide cap** on concurrent AFK
agent runs is a static `engine.concurrency` group (`gh-aw-afk-agent`) — because a
GitHub concurrency group is a mutex, this serialises agent execution to one run at
a time across the workflow, the simplest safe spend guardrail (spec #77 D4).
**Per-spec-branch serialization** is applied separately, where the race actually
is — the finalize push — via `safe-outputs.concurrency-group` keyed on the
resolved branch, so two agents heading for the same PR branch never race on the
push (user stories 13, 20). gh-aw's default per-issue workflow group additionally
prevents the same issue being dispatched twice.

> **v1 scope notes.** The `workflow_dispatch` entry point takes a single
> `issue_number` (a manual re-check / test trigger); the batch sweep over all
> open, unblocked `afk` issues (spec #77, user story 9) is a later addition. And
> because a GitHub concurrency group is a mutex, the repo-wide cap is effectively
> **one** run — excess runs **queue** rather than skip-and-stay-`afk`; a true
> N-slot cap is future work.

