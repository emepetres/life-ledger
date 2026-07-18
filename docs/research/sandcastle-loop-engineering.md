# Sandcastle "loop engineering" — primary-source research

Source repo: https://github.com/mattpocock/sandcastle (default branch: `main`, confirmed via
`GET https://api.github.com/repos/mattpocock/sandcastle` → `"default_branch":"main"`).

All paths below are relative to the repo root. All content was fetched directly from
`raw.githubusercontent.com/mattpocock/sandcastle/main/...` and the GitHub Trees/Contents APIs
(`/repos/mattpocock/sandcastle/git/trees/main?recursive=1`, `/repos/mattpocock/sandcastle/contents/.claude`),
not from blog posts or summaries.

**Important framing correction**: `mattpocock/sandcastle` is not "a repo with GitHub Actions
loop-engineering workflows" as a generic template — it is the source repository for the
`@ai-hero/sandcastle` **npm package** (`package.json`, name `@ai-hero/sandcastle`, version `0.12.0`,
description "CLI for orchestrating AI agents in isolated sandbox environments", full `src/` tree
present with `src/AgentProvider.ts`, `src/run.ts`, `src/cli.ts`, `src/sandboxes/*`, etc.). The repo
*also* dogfoods its own package inside its own `.github/workflows/` to triage/implement/review its
own GitHub issues. So what you're seeing is simultaneously (a) the library's source and (b) a live
example consumer of that library. This matters for the port: life-ledger cannot just copy the
workflow YAML, because the YAML shells out to `npx tsx .sandcastle/agent-workflows/**/*.ts` scripts
that `import * as sandcastle from "@ai-hero/sandcastle"` as an npm dependency — that package would
need to be installed (or the pattern reimplemented without it) in life-ledger.

## 1. GitHub Actions workflows

Full list from `.github/workflows/` (confirmed via Trees API):

| File | Trigger (`on:`, quoted) | Purpose |
|---|---|---|
| `.github/workflows/agent-explore.yml` | `on: issues: types: [labeled]`, gated `if: github.event.label.name == 'agent:explore'` | Read-only exploration of a labeled issue; posts a scoping/analysis comment back to the issue. No code changes, no PR. |
| `.github/workflows/agent-implement.yml` | `on: issues: types: [labeled]`, gated `if: github.event.label.name == 'agent:implement'` | Implements a labeled issue: validates issue shape, creates a branch, runs the implementation agent, opens a draft PR, and auto-labels that PR `agent:review`. |
| `.github/workflows/agent-implement-pr.yml` | `on: pull_request_target: types: [labeled]`, gated `if: github.event.label.name == 'agent:implement'` | Same "implement" agent, but targeting an **existing PR** (addressing review feedback) instead of a fresh issue. |
| `.github/workflows/agent-review.yml` | `on: pull_request_target: types: [labeled]`, gated `if: github.event.label.name == 'agent:review'` | Reviews a PR's diff, can push code fixes, posts a formal GitHub PR review + inline/thread reply comments, marks the PR ready for human review. |
| `.github/workflows/agent-update-branch.yml` | `on: pull_request_target: types: [labeled]`, gated `if: github.event.label.name == 'agent:update-branch'` | Merges the PR's base branch into the PR branch; clean merge just pushes, conflicts invoke the agent to resolve them, then pushes. |
| `.github/workflows/ci.yml` | `on: push: branches: [main]` | Plain CI: `npm ci && npm run build && npm test`. Not agent-related. |
| `.github/workflows/release.yml` | `on: push: branches: [main]` | Changesets-based release/publish workflow (`changesets/action@v1`, `npx changeset publish`). Not agent-related. |

Shared shape across all four agent workflows: `timeout-minutes: 60`; a `concurrency:` group keyed
by issue/PR number with `cancel-in-progress: false`, e.g. `.github/workflows/agent-explore.yml:12-15`
(`group: agent-explore-issue-${{ github.event.issue.number }}`); an unconditional label-transition
step (strip trigger label + `agent:blocked`, add `agent:in-progress`); Node 22 setup + `npm ci` +
`npm run build`; `npm install -g @anthropic-ai/claude-code`; one `npx tsx .sandcastle/agent-workflows/<name>/<name>.ts`
step; success-path push/comment/review steps; a `failure()` step that adds `agent:blocked` and
comments the reason; and an `always()` step stripping `agent:in-progress`.

Three of the four PR-targeting workflows use `pull_request_target` rather than `pull_request`.
`.github/workflows/agent-update-branch.yml:6-11` has an explicit inline comment explaining why:
> "pull_request_target rather than pull_request: the standard pull_request trigger depends on a
> generated merge commit, which GitHub fails to produce when the PR is out-of-date or
> conflicting — exactly when this workflow needs to run. pull_request_target runs in the base
> context and does not need the merge commit, so the labeled event fires reliably."

Nothing found in the tree applies the *first* label (`agent:explore` / `agent:implement`) to a new
issue automatically — there is no `issues: types: [opened]` (or similar) workflow. Labeling the
initial issue is a manual/human step; `docs/agents/triage.md` documents label *vocabulary* only,
not an automated labeler.

## 2. How each workflow invokes Claude Code / an agent

No workflow uses a marketplace action like `anthropics/claude-code-action`. Every agent workflow
instead:

1. Runs `npm install -g @anthropic-ai/claude-code` (installs the Claude Code CLI globally on the
   runner) — e.g. step "Install Claude Code" in every one of the five agent workflow files.
2. Runs `npx tsx .sandcastle/agent-workflows/<name>/<name>.ts` with
   `CLAUDE_CODE_OAUTH_TOKEN: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}` and
   `OUTPUT_DIR: ${{ runner.temp }}` as env vars. Quoted from `.github/workflows/agent-implement.yml`,
   step "Run implementation agent":
   ```yaml
   env:
     CLAUDE_CODE_OAUTH_TOKEN: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}
     BRANCH: ${{ steps.branch.outputs.name }}
     OUTPUT_DIR: ${{ runner.temp }}
   run: npx tsx .sandcastle/agent-workflows/implement/implement.ts
   ```
3. That script calls into `@ai-hero/sandcastle`, which builds the actual `claude` CLI invocation.
   `src/AgentProvider.ts` (the `claudeCode(...)` provider factory) constructs:
   ```
   command: `claude --print --verbose${permissionFlag} --output-format stream-json --model ${shellEscape(model)}${effortFlag}${resumeFlag}${forkFlag} -p -`
   ```
   — `--print` (non-interactive), `--verbose`, `--output-format stream-json`, `--model <name>`,
   prompt piped via stdin (`-p -`). When no explicit `permissionMode` is passed, the provider
   defaults to `--dangerously-skip-permissions` — this is what makes unattended/CI operation
   possible at all (no interactive permission prompts). Session continuity uses Claude Code's own
   `--resume <sessionId>` and `--fork-session` flags.

4. Each script builds its `claudeAgent()` from the shared helper in
   `.sandcastle/agent-workflows/shared/common.ts`:
   ```typescript
   export const claudeAgent = () =>
     sandcastle.claudeCode("claude-opus-4-8", {
       env: {
         CLAUDE_CODE_OAUTH_TOKEN: required("CLAUDE_CODE_OAUTH_TOKEN"),
       },
     });
   ```
5. Every script also passes `sandbox: noSandbox()` — the agent runs directly on the GitHub Actions
   runner's checked-out filesystem, not inside a container (sandbox/isolation details are
   otherwise out of scope per the task instructions).

Per-workflow scripts (all under `.sandcastle/agent-workflows/`):
- `explore/explore.ts` — `promptFile: explore/prompt.md`, `promptArgs: { ISSUE_NUMBER, ISSUE_TITLE, ISSUE_CONTEXT }`,
  uses `runWithExtraction(...)` for a structured `{ comment: string }` result, writes it to
  `comment.md` in `OUTPUT_DIR`; the workflow's "Post exploration comment" step then runs
  `gh issue comment "$ISSUE_NUMBER" --body-file "$COMMENT"`.
- `implement/implement.ts` — plain `sandcastle.run(...)` (no extraction needed), `promptFile: implement/prompt.md`,
  `promptArgs: { ISSUE_NUMBER, ISSUE_TITLE, BRANCH, ISSUE_CONTEXT }`; after the run it checks
  `git rev-list --count main..HEAD` and calls `fail(...)` if zero commits were made.
- `review/review.ts` — produces `review_payload.json`, `replies.json`, and a `verdict.txt` of
  `"improved"` or `"clean"`; the workflow posts the review via
  `gh api --method POST "repos/{owner}/{repo}/pulls/${PR_NUMBER}/reviews" --input "$PAYLOAD"` and
  then `gh pr ready "$PR_NUMBER"`.
- `update-branch/update-branch.ts` — first tries a plain `git merge origin/<base>`; only invokes
  `claudeAgent()` (via `runWithExtraction`) if the merge produces conflicts, using
  `update-branch/prompt.md`; verifies conflicts are actually resolved
  (`git diff --name-only --diff-filter=U` must be empty) before writing `should_push.txt=true`.
- `implement-pr/implement-pr.ts` exists for revising an existing PR (used by `agent-implement-pr.yml`);
  confirmed present with its own `prompt.md`/`extraction.md`, following the same
  `runWithExtraction`/`promptFile` shape as the others, but its full body was not read line-by-line
  in this pass (see Open Questions).

Prompt content is plain markdown with `{{PLACEHOLDER}}` interpolation, quoted in full from
`.sandcastle/agent-workflows/implement/prompt.md`:
```md
# TASK
Implement issue #{{ISSUE_NUMBER}}: {{ISSUE_TITLE}}
You are on branch `{{BRANCH}}`, already created from `main`.
...
Run `npm run typecheck` before committing. Run focused tests where relevant.
# COMMIT
Make one or more commits on `{{BRANCH}}` with conventional commit messages.
Do not push the branch.
Do not close the issue.
Do not edit labels.
Do not create or edit PRs.
When complete, output `<promise>COMPLETE</promise>`.
```
The `<promise>COMPLETE</promise>` marker and the explicit "do not push/label/PR" rules are how the
prompt keeps the agent's write-scope narrow — the workflow's own bash steps own git-push,
labeling, and GitHub API calls, not the agent.

Structured-output extraction (`.sandcastle/agent-workflows/shared/run-with-extraction.ts`) is a
two-pass pattern: run the agent normally against `promptFile`, capture the resulting
`sessionId` from the last iteration, then issue a **second** `run()` with
`resumeSession: sessionId` (same Claude Code session resumed) and a separate `extractionPrompt`
(each workflow's `extraction.md`) plus `sandcastle.Output.object({ tag, schema })`, so the agent
emits a schema-validated `<output>...</output>` tag; validation failures are retried up to
`maxRetries` (default `2`, i.e. 3 attempts total) by resuming the session again and feeding back
the error.

## 3. How "skills" are wired into the agent invocations

Two *different* things are called "skills" in this repo — they must not be conflated:

**A. Claude Code Skills (the `.claude/skills` feature).** Confirmed via
`GET /repos/mattpocock/sandcastle/contents/.claude`:
```json
{
  "name": "skills",
  "path": ".claude/skills",
  "type": "symlink",
  ...
}
```
Fetching `.claude/skills` raw returns the symlink target text `../.agents/skills`. So
`.claude/skills` **is a symlink to `.agents/skills/`**, not a real directory of its own. Under
`.agents/skills/` there is (at least) `.agents/skills/pre-release/SKILL.md` — a skill about
validating changesets before a release (per its content: "Each changeset should be about a
user-facing feature"; internal refactors excluded; related changes consolidate into one entry).

There is **no explicit CLI flag, mount step, or copy step** anywhere in the workflow YAML or the
`agent-workflows/*.ts` scripts that references `.claude/skills` or `SKILL.md`. The mechanism is
entirely implicit: because every agent invocation runs with `noSandbox()` (the `claude` process
runs directly in the checked-out repo's working directory on the runner), and because Claude Code
auto-discovers skills from `.claude/skills/*/SKILL.md` relative to its cwd, the symlinked
`.agents/skills/*/SKILL.md` files are simply *available* to the process by virtue of the checkout
— exactly as they would be for a developer running Claude Code locally in that repo. Nothing in the
sandcastle-specific orchestration layer is skills-aware; it never passes a `--skill` flag or
mentions skills in a prompt.

**B. "Agent skills" as a documentation heading — a different, unrelated meaning.** The root
`CLAUDE.md` (fetched raw in full) has a section literally titled `## Agent skills` listing three
plain documentation pointers, not files under `.claude/skills`:
```
## Agent skills
### Issue tracker
Issues live as GitHub issues in `mattpocock/sandcastle`; ... See `docs/agents/issue-tracker.md`.
### Triage labels
Default canonical labels. Agent provider support is detailed here. See `docs/agents/triage.md`.
### Domain docs
Single-context layout: `CONTEXT.md` + `docs/adr/` at the repo root. See `docs/agents/domain.md`.
```
This is prose guidance for whatever agent reads `CLAUDE.md` at session start; it has nothing to do
with the `.claude/skills/SKILL.md` mechanism in (A). Anyone porting this pattern should be careful
not to conflate "Agent skills" (a `CLAUDE.md` heading) with Claude Code Skills (the product
feature).

`docs/agents/triage.md` is a **label-vocabulary mapping table** (confirmed raw content): it maps
canonical `mattpocock/skills`-package roles (`needs-triage`, `needs-info`, `ready-for-agent`,
`ready-for-human`, `wontfix`) to this repo's actual GitHub label strings. It is read by
humans/agents doing triage, but — as noted in section 1 — no workflow automates issue labeling
based on it.

## 4. Relationships between workflows (the loop)

What actually chains automatically, confirmed at the code level (not inferred):

- `.github/workflows/agent-implement.yml`, step "Request automated review", runs after a
  successful draft PR is opened:
  ```bash
  if [ -n "$AGENT_PAT" ]; then
    GH_TOKEN="$AGENT_PAT" gh pr edit "$PR_NUMBER" --add-label "agent:review"
    ...
  fi
  GH_TOKEN="$GITHUB_TOKEN_FALLBACK" gh pr edit "$PR_NUMBER" --add-label "agent:review"
  ```
  Adding the `agent:review` label to the newly-created PR is what fires
  `.github/workflows/agent-review.yml` (`pull_request_target: labeled`, `label.name == 'agent:review'`).
  **This is the one confirmed, code-level "loop" link**: issue label → implement workflow → PR
  created → PR auto-labeled → review workflow fires automatically, no human in the loop for that
  hop.
- The `AGENT_PAT`-preferred-over-`GITHUB_TOKEN` pattern (also used for checkout in
  `agent-implement.yml`: `token: ${{ secrets.AGENT_PAT || secrets.GITHUB_TOKEN }}`) matters because
  GitHub Actions deliberately suppresses a workflow run's ability to trigger *another* workflow via
  label/push events when the actor is the default `github-actions[bot]` token — a real PAT (or
  GitHub App token) is the standard workaround. The repo's code implies this reliance (via the
  explicit fallback logic) but does not document the underlying GitHub restriction anywhere
  fetched.
- **What does *not* loop automatically**: `.sandcastle/agent-workflows/review/review.ts` contains
  no label-adding logic (confirmed by reading the script — it only writes `review_payload.json`,
  `replies.json`, `verdict.txt`), and `agent-review.yml`'s own steps only call
  `gh api .../reviews`, `gh pr ready`, and post thread replies — no `gh pr edit --add-label`
  anywhere in that file. So after a review, the PR is simply marked ready for human review;
  nothing automatically re-triggers `agent:implement` or `agent:update-branch`. A human must
  re-apply `agent:implement` (→ `agent-implement-pr.yml`) or `agent:update-branch` (→
  `agent-update-branch.yml`) to continue the cycle.
- **What starts the loop**: a human (or an external, not-found-in-this-repo process) applying
  `agent:explore` or `agent:implement` to a GitHub issue. No `issues: types: [opened]` workflow
  exists in this repo.
- **What stops the loop / marks it blocked**: every agent workflow has a `failure()` step that adds
  `agent:blocked` and posts a comment with the failure reason, read from
  `${RUNNER_TEMP}/failure_reason.txt` (written by the shared `fail()` helper in
  `.sandcastle/agent-workflows/shared/common.ts`). Every workflow also has an `always()` step
  removing the transient `agent:in-progress` label. There is no automatic retry — `agent:blocked`
  is terminal until a human re-adds the triggering label.
- `agent-implement.yml` additionally **refuses to run** (rather than looping) in three cases,
  checked in its "Detect issue shape" / "Refuse sub-issue" / "Refuse PRD-shaped issue" / "Refuse
  existing PR" steps: (1) the issue is a sub-issue of a parent, (2) the issue itself has sub-issues
  (PRD-shaped — "this MVP only supports standalone issues"), or (3) an open PR from a repo
  collaborator already targets this issue via "closes/fixes/resolves #N" in its body. These are
  guardrails against duplicate/conflicting iterations, not additional loop stages.
- The **only** genuinely unbounded, self-iterating loop found anywhere in this repo is
  `.sandcastle/run.ts`, and it is **not wired to any GitHub Actions workflow at all** — no workflow
  YAML references `run.ts`. It's a local script with its own iteration loop (Planner → parallel
  Implementer+Reviewer → Merger phases, bounded by a max-iterations config, using Docker sandboxes)
  intended for interactive/local use, not CI. Its full internals were not read in depth since the
  task explicitly deprioritizes sandbox/isolation mechanics and this script is local-only (see
  section 7).

## 5. Agent/subagent definition files vs. skills

No `.claude/agents/` directory exists anywhere in the file tree (confirmed absent from the full
recursive Trees API listing — the only `.claude/` contents are `settings.json` and the `skills`
symlink). There are no Claude Code subagent definition files in this repo.

The closest analog to "agent definitions" is `.sandcastle/agent-workflows/<name>/` — but these are
not Claude Code subagents in the product-feature sense; they are plain TypeScript entrypoints
(`explore.ts`, `implement.ts`, `implement-pr.ts`, `review.ts`, `update-branch.ts`), each pairing a
`prompt.md` (the actual task instructions given to one Claude Code CLI invocation) with an optional
`extraction.md` (structured-output instructions for the second, resumed-session call). There is no
subagent routing/delegation between them: each GitHub Actions workflow calls exactly one `.ts`
script, which calls the Claude Code CLI once or twice (main run + optional extraction run), all
using the identical `claudeAgent()` factory (same model `claude-opus-4-8`, same
`.claude/settings.json` permission allowlist, same symlinked skills directory). Task specialization
comes entirely from which `prompt.md`/`extraction.md` pair is loaded and which output schema is
requested — not from separate Claude Code agent/subagent configuration.

`.claude/settings.json` (confirmed raw content, 406 bytes) only configures a `permissions.allow`
list of specific `Bash(...)` patterns:
```json
{
  "permissions": {
    "allow": [
      "Bash(gh issue create *)", "Bash(npx vitest run *)", "Bash(gh repo view *)",
      "Bash(gh issue view *)", "Bash(git add:*)", "Bash(git status:*)",
      "Bash(git commit:*)", "Bash(npx tsgo:*)", "Bash(npm test:*)",
      "Bash(npm check:*)", "Bash(npm run typecheck *)", "Bash(git merge sandcastle/*)"
    ]
  }
}
```
This is Claude Code's permission-allowlist mechanism, unrelated to skills or agents.

## 6. Sandboxing / isolation

Explicitly skipped per instructions. Noted only insofar as it's needed elsewhere: every
GitHub-Actions-triggered script in `.sandcastle/agent-workflows/` passes `sandbox: noSandbox()`,
meaning the agent runs directly on the runner (no container/VM layer). The Docker/Podman/Vercel/
Daytona sandbox-provider code under `src/sandboxes/` exists in the package but is not exercised by
any of the five GitHub Actions workflows investigated here.

## 7. Local-only vs. CI-portable parts

**CI-native and directly portable (pattern-wise):**
- The label-driven `on: issues: types: [labeled]` / `on: pull_request_target: types: [labeled]`
  trigger pattern, the label-based state machine (`agent:explore` / `agent:implement` /
  `agent:review` / `agent:update-branch` / `agent:in-progress` / `agent:blocked`), and the
  `concurrency:` group-per-issue/PR pattern are pure GitHub Actions constructs with no local
  dependency.
- `npm install -g @anthropic-ai/claude-code` followed by `claude --print ... -p -` is CI-native;
  nothing about it requires a developer's machine.
- The `--dangerously-skip-permissions` default inside `@ai-hero/sandcastle`'s `claudeCode()`
  provider is specifically what makes unattended CI operation possible. Porting the *pattern*
  without the `@ai-hero/sandcastle` package means life-ledger's own invocation would need to supply
  an equivalent flag itself.
- `.claude/skills` being auto-discovered by `claude` because the process cwd is the checked-out
  repo is inherently CI-compatible, and is **good news for the port**: life-ledger already has
  skills installed under `.claude/skills` (confirmed present in this working tree: files like
  `.agents/skills/pre-release`-equivalents — e.g. `.agents/skills/teach/SKILL.md`,
  `.agents/skills/triage/SKILL.md`, `.agents/skills/code-review/SKILL.md`, etc. — plus a
  `skills-lock.json`, mirroring sandcastle's own `.agents/skills/` + `.claude/skills` symlink
  layout almost exactly). If life-ledger's workflow checkouts include `.claude/skills` (or a
  symlink to `.agents/skills`) in the working directory, a `claude --print -p -` invocation in that
  job would pick the skills up automatically with zero extra wiring — exactly like sandcastle does.

**Tied to the `@ai-hero/sandcastle` package specifically (not something life-ledger has installed):**
- Every one of the five agent workflows depends on `npx tsx .sandcastle/agent-workflows/**.ts`,
  which imports `@ai-hero/sandcastle` for `run()`, `claudeCode()`, `noSandbox()`,
  `Output.object()`, and (via the shared helper) `runWithExtraction`. This package also provides
  prompt templating (`{{KEY}}` substitution, `` !`command` `` shell expansion, per
  `docs/content/docs/configuration.mdx`), the `<promise>COMPLETE</promise>` completion-signal
  convention, and idle/completion timeout handling (documented in `docs/adr/0019-completion-timeout-for-hanging-process.md`,
  filename only — not fetched in full). None of this is "just GitHub Actions YAML" — it's a real
  npm dependency with its own CLI (`sandcastle init`, `sandcastle docker build-image`, etc.,
  per `README.md`). Porting the *loop-engineering pattern* (label → agent run → PR → auto-label →
  next agent run) does not strictly require this package, but reproducing the specific mechanics
  above (two-call structured-output extraction, completion-signal detection, timeouts) means either
  installing `@ai-hero/sandcastle` in life-ledger or reimplementing thin equivalents directly in
  the workflow steps.
- `.factory/` (`implement-prompt.md`, `implement-task.ts`, `review-prompt.md`, `run-daemon.sh`)
  appears to be a **second, parallel automation harness** living alongside `.sandcastle/`
  (possibly for a different agent runner/product, given the "Factory" naming). It was not fetched
  in detail — out of the requested scope (`.github/workflows/`, `.claude/`, and orchestration
  scripts under `.sandcastle/`/`scripts/`) — but its existence is worth flagging: this repo has two
  agent-orchestration surfaces, not one, and `run-daemon.sh` in particular sounds like a
  long-running local process, not a GitHub Actions step.
- `.sandcastle/run.ts`, `src/interactive.ts`, `src/main.ts`, `src/cli.ts`, and the CLI's
  `sandcastle init` / `sandcastle docker build-image` commands are explicitly developer-machine /
  interactive-terminal features (TUI mode, scaffolding, the Planner/Implementer/Reviewer/Merger
  loop) — none of them are used by any of the five GitHub Actions agent workflows, which all call
  the library's programmatic `run()`/`claudeCode()` API directly from `.ts` scripts rather than
  through the CLI or the interactive loop.

## Open questions / gaps for life-ledger port

- **`@ai-hero/sandcastle` dependency decision**: install this npm package in life-ledger to reuse
  `run()`, `claudeCode()`, `runWithExtraction`, prompt templating, and timeout/completion-signal
  handling as-is, or reimplement a minimal equivalent directly in workflow steps (a shell/Node
  snippet doing `claude --print --dangerously-skip-permissions --output-format stream-json -p - < prompt.txt`)?
  The sandcastle repo's own workflows lean on the package for real plumbing (structured-output
  extraction via session resume, `<promise>COMPLETE</promise>` detection, idle/completion
  timeouts) that would need an explicit decision either way.
- **Who applies the first label?** No automated issue-triage/labeling workflow exists in this repo.
  If life-ledger wants a fully hands-off loop (issue creation → autonomous fix → PR), the "step
  zero" of auto-labeling `agent:explore`/`agent:implement` on new issues has no reference
  implementation here and would need to be designed from scratch.
- **`AGENT_PAT` requirement**: the `agent:review` auto-label step in `agent-implement.yml` needs a
  real PAT (`secrets.AGENT_PAT`) to reliably re-trigger the next workflow, because GitHub suppresses
  workflow-triggering side effects from the default `GITHUB_TOKEN`. This secret/PAT (or an
  equivalent GitHub App installation token) would need to be provisioned in life-ledger's repo/org
  settings for the implement→review auto-chain to actually fire; the sandcastle repo doesn't
  document PAT scopes/setup anywhere fetched.
- **The loop does not close itself after review**: `agent-review.yml` never re-labels the PR to
  trigger further iterations. If life-ledger wants a truly autonomous multi-round loop (implement →
  review → re-implement based on feedback → re-review → …) rather than "one implement pass + one
  review pass then hand to a human," that re-labeling logic does not exist in sandcastle today and
  would need to be added on the life-ledger side.
- **Two competing "skills" meanings**: confirm which sense of "skills" is actually wanted —
  Claude Code's `.claude/skills/SKILL.md` auto-discovery mechanism (which life-ledger already has
  installed, confirmed via `.agents/skills/*/SKILL.md` files and `skills-lock.json` present in this
  working tree), versus the prose "Agent skills" documentation-link heading in sandcastle's root
  `CLAUDE.md` (an unrelated convention that happens to share the word "skills").
- **`.factory/` harness not investigated**: its `run-daemon.sh` suggests a second, possibly
  long-running/local automation path in the same repo; if any of its ideas are relevant to the
  port they'd need a dedicated follow-up fetch (out of scope here).
- **`implement-pr.ts` not read line-by-line**: confirmed to exist with the same
  `runWithExtraction`/`promptFile` shape as the other scripts (via directory listing and its paired
  `prompt.md`/`extraction.md`), but its full source was not fetched in this pass — worth a direct
  read before porting the "revise an existing PR" flow specifically.
- **Not every `extraction.md` was fetched**: only the general two-pass mechanism (via
  `run-with-extraction.ts`) was confirmed for all workflows, plus the full text for
  `explore`/`update-branch`'s use of it. If the port needs exact extraction-prompt wording, fetch
  `.sandcastle/agent-workflows/{review,implement-pr}/extraction.md` directly (not done in this
  pass since the core mechanism was already clear from the shared helper).
- **`shared/review-context.ts`, `shared/review-output.ts`, `shared/diff-lines.ts`** are imported by
  `review.ts`/`implement-pr.ts` (confirmed present in the file tree) but were not fetched in
  detail — they likely implement diff-line validation for inline PR comments; worth reading before
  porting the review workflow's comment-posting logic exactly.
