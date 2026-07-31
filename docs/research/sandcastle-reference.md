# Sandcastle Reference — AFK Agents on Issues (for GitHub Actions)

## Research question

Study Matt Pocock's [**sandcastle**](https://github.com/mattpocock/sandcastle) as the reference
implementation of "Claude Code + Matt's skills, running AFK (away-from-keyboard) on an issue."
life-ledger uses Matt's skills workflow (`.claude/skills/`, mirrored to `.agents/skills/`) and wants
to run its AFK parts (`/implement`, `/research`) autonomously in **GitHub Actions** (ephemeral runner,
no local machine, no long-lived daemon). Extract concretely: how it selects/claims an issue, how it
invokes the right skill, how it produces output and reports back, its concurrency/failure/guardrail
handling, and what is directly reusable vs. what must change for GitHub Actions.

**Headline finding:** Sandcastle ships **two distinct architectures**. The one that matches life-ledger
is the **GitHub Actions, label-triggered path** under `.github/workflows/agent-*.yml` +
`.sandcastle/agent-workflows/*`, which runs the agent **directly on the ephemeral runner with
`noSandbox()`** — no Docker, no daemon, no queue. Crucially, it does **NOT** invoke Matt's skills as
slash commands. It feeds a plain **`promptFile` with the method inlined** to `sandcastle.run()`, because
the `implement` skill is `disable-model-invocation: true` (user-invoked only) and cannot be auto-fired
by a headless agent.

Sources are GitHub raw file contents from `mattpocock/sandcastle@main` and `mattpocock/skills@main`,
fetched via `gh api ... -H "Accept: application/vnd.github.raw"` on 2026-07-31.

---

## 1) What sandcastle is (architecture overview)

Sandcastle is a TypeScript library/CLI (`@ai-hero/sandcastle`) for **orchestrating AI coding agents in
isolated sandboxes**. The README summarises it:

> A TypeScript library for orchestrating AI coding agents in isolated sandboxes:
> 1. You invoke agents with a single `sandcastle.run()`.
> 2. Sandcastle handles sandboxing the agent with a configurable branch strategy.
> 3. The commits made on the branches get merged back.

Source: [README.md](https://github.com/mattpocock/sandcastle/blob/main/README.md).

The core primitive is `run()`, which takes an **agent provider** (`claudeCode("claude-opus-4-8")`), a
**sandbox provider** (`docker()`, `podman()`, `vercel()`, or **`noSandbox()`**), and a **prompt**
(`prompt` inline or `promptFile` with `{{KEY}}` substitution). Branch strategies (`head`,
`merge-to-head`, `branch`) control where commits land.

There are **two ways sandcastle is driven**, and they matter a lot for us:

| Path | Entry point | Sandbox | Issue selection | Relevant to us? |
|------|-------------|---------|-----------------|-----------------|
| **Local daemon / template** | `npx tsx .sandcastle/main.ts` (templates: `simple-loop`, `parallel-planner`, …) | Docker/Podman/Vercel | The `main.mts` loop lists issues and "Picks issues one by one" | **No** — needs a long-lived host + container runtime |
| **GitHub Actions, per-issue** | `.github/workflows/agent-*.yml` → `npx tsx .sandcastle/agent-workflows/<name>/<name>.ts` | **`noSandbox()`** (runs on the runner) | **Human/agent adds a label** to one issue; the workflow reacts | **Yes — this is the blueprint** |

The templates ("simple-loop picks issues one by one and closes them", planner templates, etc.) are the
*local daemon* story. Source: [README.md — Templates](https://github.com/mattpocock/sandcastle/blob/main/README.md).
For life-ledger we mirror the **GitHub Actions path** below, which is what the sandcastle repo itself
dogfoods.

---

## 2) How it SELECTS / CLAIMS an issue, and feeds context to the agent

### Claim mechanism: label-triggered GitHub Actions (reactive, not polling)

Every AFK workflow is triggered by a **label being added to an issue**, and gated to a specific label
name. There is **no cron, no queue, no autonomous backlog polling** in the GHA path — a human (or an
upstream agent) "claims" an issue for the agent by applying `agent:implement` / `agent:explore` / etc.

```yaml
# .github/workflows/agent-implement.yml
name: Agent Implement
on:
  issues:
    types: [labeled]
jobs:
  implement:
    if: github.event.label.name == 'agent:implement'
    runs-on: ubuntu-latest
    timeout-minutes: 60
    concurrency:
      group: agent-implement-issue-${{ github.event.issue.number }}
      cancel-in-progress: false
    permissions:
      contents: write
      issues: write
      pull-requests: write
    env:
      ISSUE_NUMBER: ${{ github.event.issue.number }}
      ISSUE_TITLE: ${{ github.event.issue.title }}
      GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
      GH_REPO: ${{ github.repository }}
```
Source: [.github/workflows/agent-implement.yml](https://github.com/mattpocock/sandcastle/blob/main/.github/workflows/agent-implement.yml).

The "claim" is made concrete by a **label state machine**. Once accepted, the workflow flips the trigger
label to `agent:in-progress` (so a second `labeled` event can't re-enter, and humans see it's taken):

```yaml
- name: Transition labels
  if: steps.shape.outputs.proceed == 'true' && steps.preflight.outputs.refused != 'true'
  run: |
    gh issue edit "$ISSUE_NUMBER" --remove-label "agent:implement" || true
    gh issue edit "$ISSUE_NUMBER" --remove-label "agent:blocked" || true
    gh issue edit "$ISSUE_NUMBER" --add-label "agent:in-progress"
```
Source: same workflow. The label lifecycle is: `agent:implement` (request) → `agent:in-progress`
(claimed/running) → removed on completion; `agent:blocked` on failure/refusal; `agent:review` added to
the resulting PR to hand off to the review workflow.

### Feeding issue context to the agent

Context is pulled at runtime with `gh issue view` **inside the workflow's TypeScript driver**, then
passed to the agent as a **prompt argument** (`{{ISSUE_CONTEXT}}`), not as a file the agent must go find:

```ts
// .sandcastle/agent-workflows/implement/implement.ts
const ISSUE_NUMBER = required("ISSUE_NUMBER");
const ISSUE_TITLE = required("ISSUE_TITLE");
const BRANCH = required("BRANCH");

const issueContext =
  safeSh(`gh issue view ${ISSUE_NUMBER} --comments`) ||
  `Issue #${ISSUE_NUMBER}: ${ISSUE_TITLE}`;

const result = await sandcastle.run({
  name: `implement-#${ISSUE_NUMBER}`,
  agent: claudeAgent(),
  sandbox: noSandbox(),
  logging: { type: "stdout" },
  promptFile: path.join(import.meta.dirname, "prompt.md"),
  promptArgs: { ISSUE_NUMBER, ISSUE_TITLE, BRANCH, ISSUE_CONTEXT: issueContext },
});
```
Source: [.sandcastle/agent-workflows/implement/implement.ts](https://github.com/mattpocock/sandcastle/blob/main/.sandcastle/agent-workflows/implement/implement.ts).

The issue-tracker convention doc confirms `gh issue view <number> --comments` is the canonical "fetch the
relevant ticket" operation. Source:
[docs/agents/issue-tracker.md](https://github.com/mattpocock/sandcastle/blob/main/docs/agents/issue-tracker.md).

---

## 3) How it INVOKES the right skill — **prompt file, not slash command**

This is the single most important finding for life-ledger's D2 ("how a workflow invokes a skill on an
issue"). Sandcastle **does not type `/implement` or `/research`.** The skill selection is expressed by
*which `promptFile` the workflow driver passes to `sandcastle.run()`*, and the prompt file **inlines the
method** the skill would otherwise carry. The invocation chain is:

```
GitHub Actions job  →  npx tsx .sandcastle/agent-workflows/implement/implement.ts
                    →  sandcastle.run({ agent: claudeCode(...), promptFile: "prompt.md", promptArgs })
                    →  (bundled provider) runs the `claude` CLI headless on the resolved prompt text
```

The implement prompt is a hand-written task brief — note it embeds red-green-refactor (the `/tdd`
method), a doc-reading step, commit rules, and a completion signal, but issues **no slash command**:

```md
# TASK
Implement issue #{{ISSUE_NUMBER}}: {{ISSUE_TITLE}}
You are on branch `{{BRANCH}}`, already created from `main`.

# ISSUE
{{ISSUE_CONTEXT}}

# CONTEXT
Read the project's domain and architecture docs before changing code:
- `CONTEXT.md`
- `docs/adr/` if relevant
- `.sandcastle/CODING_STANDARDS.md`

# EXECUTION
Where a test seam already exists ... do red-green-refactor:
1. RED: write a failing test
2. GREEN: implement the smallest correct change
3. REPEAT ...
4. REFACTOR
Run `npm run typecheck` before committing. Run focused tests where relevant.

# COMMIT
Make one or more commits on `{{BRANCH}}` with conventional commit messages.
Do not push the branch. Do not close the issue. Do not edit labels. Do not create or edit PRs.
When complete, output `<promise>COMPLETE</promise>`.
```
Source: [.sandcastle/agent-workflows/implement/prompt.md](https://github.com/mattpocock/sandcastle/blob/main/.sandcastle/agent-workflows/implement/prompt.md).

### Why it inlines instead of calling `/implement`

Matt's `implement` skill is **user-invoked only** — the model cannot auto-fire it:

```md
---
name: implement
description: "Implement a piece of work based on a spec or set of tickets."
disable-model-invocation: true
---
Implement the work described by the user in the spec or tickets.
Use /tdd where possible, at pre-agreed seams.
...
Once done, use /code-review to review the work.
Commit your work to the current branch.
```
Source: [mattpocock/skills — skills/engineering/implement/SKILL.md](https://github.com/mattpocock/skills/blob/main/skills/engineering/implement/SKILL.md).

The invocation-model doc spells out the rule:

> **User-invoked** — reachable **only by the human typing its name**. Set `disable-model-invocation: true`
> in the frontmatter (Claude Code) ... Each harness excludes a user-invoked skill from the model's reach
> in its own way, so nothing but the human can fire it.

Source: [mattpocock/skills — .agents/invocation.md](https://github.com/mattpocock/skills/blob/main/.agents/invocation.md).

So in a headless/AFK run there is no human to type `/implement`, and the skill is deliberately unreachable
by the model. Sandcastle's answer is to **not depend on the user-invoked skill at all** — it re-expresses
the workflow as a prompt file that the agent executes directly, while still leaning on *model-invocable*
sub-skills (the prompt's "red-green-refactor" is the `/tdd` method; the implement skill itself points at
`/tdd` and `/code-review`).

**Asymmetry that helps us:** the `research` skill is **model-invocable** (no `disable-model-invocation`):

```md
---
name: research
description: Investigate a question against high-trust primary sources and capture the findings as a Markdown file in the repo. ...
---
```
Source: [mattpocock/skills — skills/engineering/research/SKILL.md](https://github.com/mattpocock/skills/blob/main/skills/engineering/research/SKILL.md).

That means a headless agent *could* auto-reach `/research`, but sandcastle still prefers an explicit
prompt file (its `explore` workflow, below) rather than relying on autoload — giving deterministic,
inline control over the read-only contract and the structured output it wants back.

### CLI-level invocation (agent provider)

The concrete `claudeCode` provider is **bundled** in the published package; only the `AgentProvider.ts`
interface is in `src/` ([src/AgentProvider.ts](https://github.com/mattpocock/sandcastle/blob/main/src/AgentProvider.ts)),
so the exact `claude` argv is not directly source-readable in the repo. The README documents the flags the
provider uses: AFK runs default to **`--dangerously-skip-permissions`** (overridable via
`claudeCode(model, { permissionMode })`), resume uses `--resume <id>`, fork uses `--fork-session`. The
provider factory used everywhere is:

```ts
// .sandcastle/agent-workflows/shared/common.ts
export const claudeAgent = () =>
  sandcastle.claudeCode("claude-opus-4-8", {
    env: { CLAUDE_CODE_OAUTH_TOKEN: required("CLAUDE_CODE_OAUTH_TOKEN") },
  });
```
Sources: [common.ts](https://github.com/mattpocock/sandcastle/blob/main/.sandcastle/agent-workflows/shared/common.ts),
[README.md — ClaudeCodeOptions / Session resume](https://github.com/mattpocock/sandcastle/blob/main/README.md).

---

## 4) How it produces OUTPUT and reports back

Two shapes, depending on workflow. Both keep the agent narrowly scoped and let the **workflow YAML** own
all side effects (push, PR, comments, labels) — the agent only writes commits or a structured payload.

### implement → branch + commits → draft PR → hand off to review

The driver verifies the agent actually produced commits before the workflow proceeds:

```ts
const commitsAhead = Number(sh("git rev-list --count main..HEAD").trim());
if (!Number.isFinite(commitsAhead) || commitsAhead === 0) {
  fail("Agent finished but no commits were made on the branch.");
}
```
Source: [implement.ts](https://github.com/mattpocock/sandcastle/blob/main/.sandcastle/agent-workflows/implement/implement.ts).
(The agent is told "Do not push the branch. Do not create or edit PRs." — the workflow does it.)

The workflow then pushes, opens a **draft PR that closes the issue**, and labels the PR `agent:review` to
trigger the separate review workflow:

```yaml
- name: Push branch
  if: ... && success()
  run: git push --force origin "$BRANCH"

- name: Open draft PR
  if: ... && success()
  run: |
    title="Fix #${ISSUE_NUMBER}: ${ISSUE_TITLE}"
    { echo "Closes #${ISSUE_NUMBER}"; echo; echo "Implemented by the Sandcastle agent workflow."; } > "$body_file"
    pr_url=$(gh pr create --draft --base main --head "$BRANCH" --title "$title" --body-file "$body_file" | tail -n1)

- name: Request automated review
  if: ... && success()
  run: |
    ... gh pr edit "$PR_NUMBER" --add-label "agent:review" ...
```
Source: [.github/workflows/agent-implement.yml](https://github.com/mattpocock/sandcastle/blob/main/.github/workflows/agent-implement.yml).
Branch name is derived from the issue: `agent/issue-<N>-<slugified-title>`.

### explore (the read-only "research/triage" analog) → structured comment

The `explore` workflow is the closest analog to `/research`: read-only, produces a **comment**, not code.
It uses sandcastle's **structured-output** feature — the agent emits its answer inside an `<output>` tag
and the driver extracts a schema-validated object:

```ts
// .sandcastle/agent-workflows/explore/explore.ts
const result = await runWithExtraction({
  name: `explore-#${ISSUE_NUMBER}`,
  agent: claudeAgent(),
  sandbox: noSandbox(),
  promptFile: path.join(import.meta.dirname, "prompt.md"),
  promptArgs: { ISSUE_NUMBER, ISSUE_TITLE, ISSUE_CONTEXT: issueContext },
  output: sandcastle.Output.object({ tag: "output", schema: exploreOutputSchema }),
  extractionPrompt: fs.readFileSync(path.join(import.meta.dirname, "extraction.md"), "utf8"),
});
writeText("comment.md", result.output.comment);
```
The workflow then posts the file as an issue comment: `gh issue comment "$ISSUE_NUMBER" --body-file "$COMMENT"`.
The explore prompt hard-forbids side effects: "You MUST NOT: Edit files ... Create or edit PRs ... Edit
labels ... Post comments yourself -- the workflow posts your findings."
Sources: [explore.ts](https://github.com/mattpocock/sandcastle/blob/main/.sandcastle/agent-workflows/explore/explore.ts),
[explore/prompt.md](https://github.com/mattpocock/sandcastle/blob/main/.sandcastle/agent-workflows/explore/prompt.md),
[.github/workflows/agent-explore.yml](https://github.com/mattpocock/sandcastle/blob/main/.github/workflows/agent-explore.yml).

**Reporting-back is entirely via the GitHub issue tracker** (labels + comments + PR), using `gh` in the
workflow. There is no external DB or dashboard.

---

## 5) Concurrency, failure handling, retries, guardrails

| Concern | Mechanism | Source |
|---------|-----------|--------|
| **Concurrency / no double-claim** | `concurrency.group: agent-implement-issue-${{ github.event.issue.number }}` with `cancel-in-progress: false` — serialises runs per issue. Plus the `agent:implement → agent:in-progress` label flip removes the trigger. | agent-implement.yml |
| **Wall-clock cap** | `timeout-minutes: 60` on the job. | all agent-*.yml |
| **Idle / hang guard** | `run()` has `idleTimeoutSeconds` (default 600) before a completion signal; `completionTimeoutSeconds` (default 60) after the `<promise>COMPLETE</promise>` signal, to salvage commits when a child process hangs stdout. | README (Prompts / hanging processes) |
| **Scope refusals (human gates)** | implement refuses **sub-issues** and **PRD-shaped issues** (issues with sub-issues), and refuses if an **open PR from a collaborator** already targets the issue — marking `agent:blocked` and commenting instead of running. | agent-implement.yml (`Detect issue shape`, `Preflight existing PR`, `Refuse *` steps) |
| **"Did it do the work?" gate** | Driver fails if `git rev-list --count main..HEAD == 0` (agent produced no commits). | implement.ts |
| **Failure reporting** | `fail()` writes `failure_reason.txt` to `OUTPUT_DIR`; the workflow's `if: failure()` step reads it, adds `agent:blocked`, and comments the reason + run URL. | common.ts + agent-implement.yml (`Mark blocked on failure`) |
| **Retry** | **Manual, human-driven**: "Re-add `agent:implement` to retry." No automatic retry loop in the GHA path. (Structured-output has an in-`run()` `maxRetries` that resumes the session, used by extraction, but not a workflow-level retry.) | agent-implement.yml, README (Structured output) |
| **Always-cleanup** | `if: always()` step removes `agent:in-progress` so a crashed run doesn't wedge the label state. | agent-implement.yml |
| **Permissions least-privilege** | Per-workflow `permissions:` block (implement: contents/issues/PRs write; explore: issues write only). | agent-*.yml |
| **Spend guardrails** | **None explicit** beyond the 60-min timeout and single-issue scope. No token budget / cost cap in the workflows. | (absence) |

Note the implement workflow uses `secrets.AGENT_PAT || secrets.GITHUB_TOKEN` for checkout/label ops — a
PAT is needed so that pushing a branch / adding the `agent:review` label **triggers downstream workflows**
(the default `GITHUB_TOKEN` does not re-trigger `on:` events). Source: agent-implement.yml.

---

## 6) Reusable vs. must-change-for-GitHub-Actions

life-ledger already targets GitHub Actions, and sandcastle's own `agent-*` workflows already run in
GitHub Actions with `noSandbox()`. So most of the GHA path is **directly reusable**; the parts that must
change are mainly (a) the skill-invocation shim and (b) the sandcastle-repo build assumptions.

| Aspect | Sandcastle (GHA path) | For life-ledger in GitHub Actions | Verdict |
|--------|-----------------------|-----------------------------------|---------|
| Trigger / claim | `on: issues: [labeled]`, gated `if: label.name == 'agent:implement'` | Same — pick label names (e.g. `agent:implement`, `agent:research`) | **Reusable as-is** |
| Per-issue concurrency | `concurrency.group` keyed on issue number, `cancel-in-progress: false` | Same | **Reusable as-is** |
| Label state machine | request → `in-progress` → done / `blocked`, `always()` cleanup | Same | **Reusable as-is** |
| Issue context feed | `gh issue view <n> --comments` → `promptArgs.ISSUE_CONTEXT` | Same | **Reusable as-is** |
| Sandbox | **`noSandbox()`** — agent runs on the runner | Same — the ephemeral runner *is* the isolation. **Do NOT port Docker/Podman/Vercel or the local daemon templates.** | **Reusable as-is** (drop the container providers) |
| Skill invocation | `promptFile` with the method **inlined**; no `/implement` slash command | **Must change / decide.** life-ledger *wants* to run Matt's `/implement`, `/research`. But `/implement` is `disable-model-invocation:true` → not auto-fireable headless. Options: (A) mirror sandcastle — author a `prompt.md` that inlines the method and calls only model-invocable sub-skills (`/tdd`, `/code-review`, `/research`); (B) test whether `claude -p "/implement …"` (prompt = user turn) actually expands a user-invoked slash command in headless mode. | **Must change** |
| Agent runtime dependency | `sandcastle.run()` from `@ai-hero/sandcastle`; repo also runs `npm ci` + `npm run build` (it's building *itself*) | life-ledger can either (A) `npm i -D @ai-hero/sandcastle` and reuse `run()` for prompt substitution + completion-signal + structured-output plumbing, or (B) call `claude -p` directly and skip the dependency. sandcastle's `npm run build` step is repo-specific and must be dropped. | **Must change** (keep or drop the dep; drop the self-build) |
| Auth | `CLAUDE_CODE_OAUTH_TOKEN` secret; `AGENT_PAT`/`GITHUB_TOKEN` | Same — add `CLAUDE_CODE_OAUTH_TOKEN` + a PAT so downstream workflows re-trigger | **Reusable as-is** |
| Output: implement | push branch → draft PR "Closes #N" → label `agent:review` | Same shape; life-ledger already opens PRs from feature branches | **Reusable as-is** |
| Output: research | structured `<output>` tag → extraction → `gh issue comment`. But life-ledger's `/research` writes a **Markdown file in the repo** (`docs/research/*.md`) | **Must change**: decide research output target — commit the `.md` on a `research/*` branch (+ PR/comment) vs. only post a comment. sandcastle's explore posts a comment and writes nothing to the repo. | **Must change** |
| Guardrails | 60-min timeout, scope refusals, no-commit gate, `blocked` on fail, manual re-label retry | Reusable; consider adding a token/cost cap (sandcastle has none) | **Reusable, extend** |
| Issue selection strategy | Reactive (human labels one issue). No autonomous backlog polling in GHA. | If life-ledger wants autonomous "pick next issue," that is **new** — sandcastle's autonomous picking lives only in the *local daemon* templates, not the GHA path. | **Must add if desired** |

---

## 7) Confidence & open questions

**High confidence** (read directly from primary source files):
- The GHA path uses `noSandbox()` and runs the agent on the runner — no container in the GHA workflows.
  (implement.ts, explore.ts both pass `sandbox: noSandbox()`.)
- Claiming is label-triggered with a per-issue `concurrency` group and a label state machine; reporting
  is via `gh` PR/comment/label. (agent-*.yml.)
- Sandcastle does **not** invoke `/implement` as a slash command; it inlines the method in `prompt.md`
  and selects behaviour by which driver/promptFile the workflow runs. (implement/prompt.md, implement.ts.)
- Matt's `implement` skill is user-invoked (`disable-model-invocation: true`) and `research` is
  model-invocable — verified from the SKILL.md frontmatter and `.agents/invocation.md`.

**Open questions that could block D2 ("how a workflow invokes a skill on an issue"):**
1. **Does `claude -p "/implement …"` fire a user-invoked skill in headless mode?** In `claude -p`, the
   prompt is the *user* turn, so a slash command there is arguably a "human typing its name." If it
   expands, life-ledger could invoke `/implement` literally instead of re-inlining the method. If it does
   **not** expand (likely, given `disable-model-invocation` is designed to exclude non-interactive model
   reach), we must follow sandcastle and inline the method. **This is the crux of D2 and should be settled
   with a spike** (`claude -p "/implement"` in a scratch checkout with the plugin installed). Not
   resolvable from source alone.
2. **Exact `claude` argv used by sandcastle's `claudeCode` provider** is not in the repo (bundled in the
   published package; only `AgentProvider.ts` interface is in `src/`). Flags cited (`--dangerously-skip-permissions`,
   `--resume`, `--fork-session`) come from the README, not from reading the provider source. If life-ledger
   calls `claude` directly rather than through `sandcastle.run()`, confirm the flags against the installed
   `@ai-hero/sandcastle` dist or the `@anthropic-ai/claude-code` CLI `--help`.
3. **Skill availability on the runner.** For any model-invocable sub-skill (`/tdd`, `/code-review`,
   `/research`) to be reachable headless, the skills must be installed where Claude Code loads them
   (`.claude/skills/` in the checkout, or the plugin). life-ledger already mirrors skills to
   `.claude/skills/` and `.agents/skills/`, so this is likely satisfied, but verify the plugin/skill load
   path is present on the ephemeral runner checkout.
4. **Research output convention mismatch.** sandcastle's explore posts a comment and writes nothing to the
   repo; life-ledger's `/research` writes `docs/research/*.md`. The workflow must choose commit-to-branch
   vs. comment-only. Product decision, not a source question.

---

## Source index

Sandcastle (`mattpocock/sandcastle@main`):
- [README.md](https://github.com/mattpocock/sandcastle/blob/main/README.md)
- [.github/workflows/agent-implement.yml](https://github.com/mattpocock/sandcastle/blob/main/.github/workflows/agent-implement.yml)
- [.github/workflows/agent-explore.yml](https://github.com/mattpocock/sandcastle/blob/main/.github/workflows/agent-explore.yml)
- [.sandcastle/agent-workflows/implement/implement.ts](https://github.com/mattpocock/sandcastle/blob/main/.sandcastle/agent-workflows/implement/implement.ts)
- [.sandcastle/agent-workflows/implement/prompt.md](https://github.com/mattpocock/sandcastle/blob/main/.sandcastle/agent-workflows/implement/prompt.md)
- [.sandcastle/agent-workflows/explore/explore.ts](https://github.com/mattpocock/sandcastle/blob/main/.sandcastle/agent-workflows/explore/explore.ts)
- [.sandcastle/agent-workflows/explore/prompt.md](https://github.com/mattpocock/sandcastle/blob/main/.sandcastle/agent-workflows/explore/prompt.md)
- [.sandcastle/agent-workflows/shared/common.ts](https://github.com/mattpocock/sandcastle/blob/main/.sandcastle/agent-workflows/shared/common.ts)
- [docs/agents/issue-tracker.md](https://github.com/mattpocock/sandcastle/blob/main/docs/agents/issue-tracker.md)
- [src/AgentProvider.ts](https://github.com/mattpocock/sandcastle/blob/main/src/AgentProvider.ts)

Skills (`mattpocock/skills@main`):
- [.agents/invocation.md](https://github.com/mattpocock/skills/blob/main/.agents/invocation.md)
- [skills/engineering/implement/SKILL.md](https://github.com/mattpocock/skills/blob/main/skills/engineering/implement/SKILL.md)
- [skills/engineering/research/SKILL.md](https://github.com/mattpocock/skills/blob/main/skills/engineering/research/SKILL.md)
- [.claude-plugin/plugin.json](https://github.com/mattpocock/skills/blob/main/.claude-plugin/plugin.json)
</content>
</invoke>
