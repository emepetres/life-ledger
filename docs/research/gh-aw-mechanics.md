# gh-aw Mechanics Research

Research for wayfinder ticket #66. Investigates GitHub Agentic Workflows (gh-aw) mechanics
for a **label-triggered, Claude-Code, PR-producing** agentic workflow.

All facts below are drawn from primary sources: the official docs
([github.github.com/gh-aw](https://github.github.com/gh-aw/)), the source repo
([github.com/github/gh-aw](https://github.com/github/gh-aw) — note the repo moved from
`githubnext/gh-aw` to `github/gh-aw`), and real example workflows in
[github.com/githubnext/agentics/tree/main/workflows](https://github.com/githubnext/agentics/tree/main/workflows).
Verbatim YAML is quoted from the docs source markdown and the example `.md` files.

## Question

How does gh-aw work for the specific shape we need: a workflow triggered when a **specific
issue label** (e.g. `afk`) is applied, running **Claude Code** as the engine, receiving the
issue content as its task, and **autonomously producing a pull request** — including the
security model, secrets, and operational knobs?

---

## 1. Workflow file format, location, and compilation

- Workflows are **Markdown files with a YAML frontmatter block** (delimited by `---`). The
  frontmatter is configuration; the Markdown body is the natural-language prompt/instructions
  for the agent.
  ([overview](https://github.github.com/gh-aw/))
- Source `.md` files live under **`.github/workflows/`** in the repo, alongside the compiled
  output. ([quick-start](https://github.github.com/gh-aw/setup/quick-start/))
- The `gh aw` CLI (a `gh` extension) **compiles** each `.md` into a hardened, SHA-pinned
  `.lock.yml` standard GitHub Actions workflow. **Both files are committed** — the `.md` source
  and the generated `.lock.yml`.
- Setup / lifecycle commands (quick-start):
  ```bash
  gh extension install github/gh-aw          # install the CLI extension
  gh aw add githubnext/agentics/<name> --engine claude   # add a workflow (wizard variant: add-wizard)
  gh aw compile                              # regenerate .lock.yml after editing frontmatter
  gh secret set ANTHROPIC_API_KEY            # add engine credential (or via GitHub UI)
  gh aw run <name>                           # trigger a run
  ```
  Requires **GitHub Actions enabled** in the repo (Settings → Actions).

---

## 2. Triggering on a specific issue label

`on:` accepts **standard GitHub Actions triggers** plus gh-aw enhancements. `labeled` is a
supported issue/PR activity type. Source:
[triggers.md](https://raw.githubusercontent.com/github/gh-aw/main/docs/src/content/docs/reference/triggers.md).

There are **three** documented ways to filter to a specific label such as `afk`:

**(a) `names:` filter on a `labeled` trigger** (label stays on the item; runs every time labeled):
```yaml
on:
  issues:
    types: [labeled, unlabeled]
    names: [bug, critical, security]
```
> "Filter issue and pull request triggers by label names using the `names:` field. Unlike
> `label_command`, the label stays on the item after the workflow runs." (triggers.md §Filtering
> with Labels)

**(b) Natural-language shorthand** (compiles to the above, and auto-adds `workflow_dispatch`):
```yaml
on: issue labeled bug
on: issue labeled bug, enhancement, priority-high  # Multiple labels
on: pull_request labeled needs-review, ready-to-merge
```

**(c) `label_command:` — one-shot command semantics** (auto-removes the label so it can be
re-applied to re-trigger):
```yaml
on:
  label_command: deploy
# or, with options:
on:
  label_command:
    name: deploy
    events: [pull_request]     # default: issues, pull_request, discussion
    remove_label: false        # default true; keep label = persistent state
```
> "The `label_command:` trigger activates a workflow when a specific label is applied … and
> **automatically removes that label** … This treats a label as a one-shot command rather than a
> persistent state marker." The compiler generates `types: [labeled]` events, adds a
> `workflow_dispatch` with `item_number` for manual testing, and exposes the matched label as
> `needs.activation.outputs.label_command`. (triggers.md §Label Command Trigger)

**Implication for `afk`:** `on: issue labeled afk` (or `names: [afk]`) if the label should
persist; `label_command: afk` if it should behave as a fire-once command that clears itself.
Note `remove_label: true` (the `label_command` default) needs `issues: write` /
`pull-requests: write` for the removal step; `remove_label: false` does not.

**Other filters available:** `skip-if-match:` / `skip-if-no-match:` (GitHub search queries),
`on.roles:` / `on.skip-roles:` (repo access roles), and custom `on.steps:` pre-activation
gates. Reactions/status feedback via `reaction: "eyes"` and `status-comment: true` (both default
**on** for `slash_command` and `label_command`).

---

## 3. Running Claude Code as the engine

- Select the engine in frontmatter: `engine: claude`.
  ([engines](https://github.github.com/gh-aw/reference/engines/))
- Object form for pinning/tuning:
  ```yaml
  engine:
    id: claude
    version: "2.1.70"      # pin a Claude Code release
    model: claude-opus-4   # override default model
    max-turns: 20          # cap agent iterations (Claude-specific)
  ```
- **Required secret: `ANTHROPIC_API_KEY`** (repository secret). Docs list the auth options as
  `"ANTHROPIC_API_KEY" (standard) or "engine.auth" Anthropic WIF (keyless)`.

**How the agent receives the triggering issue as its task:** the **Markdown body is the prompt**,
and GitHub Actions expressions are interpolated into it. Concrete example from triggers.md:
```markdown
Triage bug report: "${{ github.event.issue.title }}" and add-comment with a summary of the next steps.
```
So issue fields are injected via `${{ github.event.issue.title }}`, `${{ github.event.issue.number }}`,
`${{ github.event.issue.body }}`, `${{ github.repository }}`, etc. For `slash_command` /
`label_command` triggers, sanitized free-text context is exposed as an output (the `issue-triage`/
`pr-fix` examples read `${{ github.event.issue.number }}` and command "context text" such as
`${{ steps.sanitized.outputs.text }}` / `needs.activation.outputs.text`). Untrusted content is
**sanitized before reaching the agent** (see §5).

---

## 4. Producing a PR autonomously (safe-outputs)

The agent runs **read-only** and cannot push directly. It requests writes via **`safe-outputs`**,
which a separate permission-scoped job executes. To create PRs, declare
`safe-outputs.create-pull-request`.
([safe-outputs-pull-requests.md](https://raw.githubusercontent.com/github/gh-aw/main/docs/src/content/docs/reference/safe-outputs-pull-requests.md))

**Verbatim option surface** (docs source):
```yaml
safe-outputs:
  create-pull-request:
    title-prefix: "[ai] "         # prefix for titles
    labels: [automation]          # labels to attach
    reviewers: [user1, copilot]   # reviewers (use 'copilot' for bot)
    team-reviewers: [platform-reviewers]
    assignees: [user1]
    draft: true                   # create as draft — enforced as POLICY (default: true)
    max: 3                        # max PRs per run (default: 1)
    expires: 14                   # auto-close after N days (also 2h, 7d, 2w, 1m, 1y)
    if-no-changes: "warn"         # "warn" (default), "error", or "ignore"
    base-branch: "vnext"          # PR target (default: github.base_ref || github.ref_name)
    allowed-base-branches: [main, "release/*"]
    allowed-branches: ["feature/*", "release/*"]
    fallback-as-issue: false      # default true: falls back to an issue if PR creation is blocked
    auto-close-issue: false       # default true: appends "Fixes #N" when triggered from an issue
    preserve-branch-name: true    # omit random salt suffix from branch name (default false)
    protected-files: fallback-to-issue
    signed-commits: true          # default true (GraphQL signed commits)
```

Key behavioral facts (docs §How it works / §Other notes):
- **Mechanism:** the agent's commits are packaged as a **git bundle**, uploaded as an Actions
  artifact; a separate permission-controlled **`safe_outputs` job** checks out the base branch,
  applies the bundle, and creates the PR via the GitHub GraphQL API. The agent never holds write
  creds.
- When `create-pull-request` is configured, **git commands are auto-enabled**
  (`checkout, branch, switch, add, rm, commit, merge`).
- **`draft` is a policy** the agent cannot override at runtime (default `draft: true`).
- **`auto-close-issue` (default true)** appends `Fixes #N` to the PR body when triggered from an
  issue — so a label-triggered PR auto-links its source issue.
- **Fallback:** if PR creation is blocked (e.g. org settings), it opens an issue instead unless
  `fallback-as-issue: false`.
- PRs **do not trigger CI by default** (see docs "Triggering CI").
- Can be disabled org-wide at runtime via the `GH_AW_POLICY_ALLOW_CREATE_PULL_REQUEST` variable.

**Related PR-write safe outputs:** `push-to-pull-request-branch` (push commits to an existing PR
branch — used by `pr-fix`/`perf-improver`), `update-pull-request`, `add-reviewer`,
`create-pull-request-review-comment`, `merge-pull-request` (experimental).

**`add-comment`** (post to the triggering issue/PR/discussion):
```yaml
safe-outputs:
  add-comment:
    max: 3
    target: "*"                 # "triggering" (default), "*" (any), or a number
    required-labels: [bot, automated]
    required-title-prefix: "[bot] "
    hide-older-comments: true
```

### Real example: `create-pull-request` in the wild

From `githubnext/agentics/workflows/doc-updater.md` (verbatim frontmatter fragment):
```yaml
permissions:
  contents: read
  issues: read
  pull-requests: read
tools:
  github:
    toolsets: [default]
  edit:
  bash: true
safe-outputs:
  create-pull-request:
    expires: 2d
    title-prefix: "[docs] "
    labels: [documentation, automation]
    draft: false
    protected-files: fallback-to-issue
```

From `githubnext/agentics/workflows/perf-improver.md` (verbatim):
```yaml
safe-outputs:
  add-comment:
    max: 10
    target: "*"
    hide-older-comments: true
  create-pull-request:
    draft: true
    title-prefix: "[perf-improver] "
    labels: [automation, performance]
    max: 4
    protected-files: fallback-to-issue
  push-to-pull-request-branch:
    target: "*"
    required-title-prefix: "[perf-improver] "
    max: 4
```

From `githubnext/agentics/workflows/issue-triage.md` — a real **issue-triggered** workflow
(verbatim):
```yaml
on:
  issues:
    types: [opened, reopened]
  reaction: eyes
permissions: read-all
safe-outputs:
  add-labels:
    max: 5
  add-comment:
  set-issue-type:
    max: 1
  close-issue:
    target: "triggering"
    state-reason: "not_planned"
    max: 1
tools:
  web-fetch:
  github:
    toolsets: [issues, labels]
    min-integrity: none
timeout-minutes: 10
```

---

## 5. Security model, permissions, and operational knobs

**Two-job read-only-token model** (architecture docs):
- **Agent job** runs with **minimal read-only permissions** and cannot modify repo state.
- **Safe-outputs job(s)** run separately with **scoped write permissions**, validating and
  executing the agent's structured requests.
- > "the agent job runs with minimal read-only permissions, while write operations are deferred to
  > separate jobs." Even a fully-compromised agent lacks write creds.

**Content sanitization:** user-generated content (issue titles/bodies/comments) is sanitized
before reaching the agent — @mention neutralization, `<script>`→`(script)`, HTTPS-only URL
filtering from trusted domains, untrusted domains → `(redacted)`, and length limits.

**`permissions:`** — GitHub-Actions-style read scopes for the agent job. Common patterns seen in
examples: `permissions: read-all` (issue-triage, plan, pr-fix uses `read-all`) or explicit:
```yaml
permissions:
  contents: read
  issues: read
  pull-requests: read
```

**`network:`** — allowlist egress by ecosystem/domain:
```yaml
network:
  allowed:
    - defaults
    - python
    - "api.example.com"
```

**`tools:`** — declare allowed capabilities (GitHub toolsets, `bash`, `edit`, `web-fetch`, MCP
servers). Example: `tools: { github: { toolsets: [default] }, edit:, bash: true }`.

**Cost knobs** (frontmatter reference):
```yaml
max-ai-credits: 500          # budget; supports K/M suffixes (100K, 50M); -1 disables enforcement
max-daily-ai-credits: 10000  # daily per-workflow cap; -1 (default) = disabled
timeout-minutes: 30
```

**Concurrency:** gh-aw auto-generates a concurrency policy; can be set explicitly:
```yaml
concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true
```

**Human-in-the-loop / approval:** `on.manual-approval:` / an `environment:` on the activation job
gates execution behind GitHub Environment protection rules.

**Org/repo prerequisites:** `gh aw` extension installed; GitHub Actions enabled;
`ANTHROPIC_API_KEY` secret set; both `.md` and `.lock.yml` committed. PR creation additionally
depends on the repo/org **allowing Actions to create pull requests** (otherwise `create-pull-request`
hits its issue fallback) — see Confidence notes.

---

## A minimal composed example for our use case (afk-labeled → Claude → PR)

Synthesized from the verified primitives above (not a verbatim quote — each element is sourced in
the sections above):
```yaml
---
on: issue labeled afk          # §2(b); label persists. Use `label_command: afk` for one-shot.
engine: claude                 # §3; requires ANTHROPIC_API_KEY secret
permissions:
  contents: read
  issues: read
network:
  allowed: [defaults]
tools:
  github:
    toolsets: [default]
  edit:
  bash: true
safe-outputs:
  create-pull-request:
    draft: true
    title-prefix: "[afk] "
    labels: [automation]
  add-comment:
timeout-minutes: 30
max-ai-credits: 1000
---
# Body = the prompt
Work on issue #${{ github.event.issue.number }}: "${{ github.event.issue.title }}".
${{ github.event.issue.body }}
Make the change and open a pull request describing what you did.
```

---

## Confidence & open questions

**High confidence** (verified against docs source and/or real example files):
- Markdown+frontmatter format, `.github/workflows/` location, `gh aw compile` → `.lock.yml`,
  commit both. (quick-start; multiple examples)
- Label filtering — all three forms (`names:`, `on: issue labeled X`, `label_command:`) are
  verbatim from `triggers.md` source. **Directly answers the trigger design question.**
- `engine: claude` + `ANTHROPIC_API_KEY`. (engines docs)
- `create-pull-request` / `add-comment` / `push-to-pull-request-branch` option surfaces are
  verbatim from `safe-outputs-pull-requests.md` and real agentics workflows.
- Two-job read-only-token security model and sanitization. (architecture docs)
- The Markdown body is the prompt with `${{ github.event.* }}` interpolation. (triggers.md example)

**Medium / to confirm before locking downstream design:**
- **D1/D2 (trigger choice):** whether we want the `afk` label to persist (`names:`/`issue labeled`)
  or self-clear (`label_command:`). Both are supported; this is a design decision, not a blocker.
  If `label_command` with default `remove_label: true`, the activation job needs
  `issues: write` for label removal — confirm that does not conflict with the read-only agent
  posture (removal happens in the activation/safe job, not the agent job).
- **D3 (prompt plumbing):** exact variable/output name for sanitized command context text differs
  across examples (`steps.sanitized.outputs.text` vs `needs.activation.outputs.text`). For a plain
  `labeled` trigger we can rely on `${{ github.event.issue.* }}` directly; the sanitized-context
  output matters mainly for `slash_command`/`label_command`. Verify the current output name in the
  generated `.lock.yml` after `gh aw compile`.
- **D4 (PR autonomy):** `create-pull-request` **falls back to opening an issue** if the org/repo
  disallows Actions creating PRs (Settings → Actions → "Allow GitHub Actions to create and approve
  pull requests"). This repo setting must be enabled for true autonomous PRs — **confirm it is on
  for emepetres/life-ledger** or the workflow will silently degrade to issue-creation. Also note
  PRs created this way **do not trigger CI by default**.
- **D6 (cost/governance):** `max-ai-credits` semantics/units ("AI credits") are engine/plan
  dependent; confirm how they map to Anthropic API spend for `engine: claude`, and whether
  `max-daily-ai-credits` needs to be set (default disabled).

**Unresolved / not fully verified:**
- Some docs sub-page URLs on the published site 404'd (e.g. `.../safe-outputs/create-pull-request/`,
  `.../safe-outputs/pull-requests/`); the authoritative content was retrieved from the **repo docs
  source** instead (`github/gh-aw` `docs/src/content/docs/reference/*.md`). Published-site paths may
  have reorganized — prefer the source markdown when in doubt.
- The repo canonical location is now **`github/gh-aw`** (moved from `githubnext/gh-aw`); the
  `githubnext/gh-aw` links redirect. Example workflows remain under **`githubnext/agentics`**.
- Exact default agent-job `GITHUB_TOKEN` scope when `permissions:` is omitted was not verified
  verbatim; examples always set it explicitly (`read-all` or scoped read). Recommend always
  declaring `permissions:` explicitly.

## Sources

- Docs overview: https://github.github.com/gh-aw/
- Quick start: https://github.github.com/gh-aw/setup/quick-start/
- Engines: https://github.github.com/gh-aw/reference/engines/
- Frontmatter reference: https://github.github.com/gh-aw/reference/frontmatter/
- Architecture / security: https://github.github.com/gh-aw/introduction/architecture/
- Triggers (source): https://raw.githubusercontent.com/github/gh-aw/main/docs/src/content/docs/reference/triggers.md
- Safe outputs — PRs (source): https://raw.githubusercontent.com/github/gh-aw/main/docs/src/content/docs/reference/safe-outputs-pull-requests.md
- Safe outputs (site): https://github.github.com/gh-aw/reference/safe-outputs/
- Source repo README: https://raw.githubusercontent.com/githubnext/gh-aw/main/README.md
- Example workflows: https://github.com/githubnext/agentics/tree/main/workflows
  (read in full: `issue-triage.md`, `pr-fix.md`, `plan.md`, `doc-updater.md`, `perf-improver.md`)
