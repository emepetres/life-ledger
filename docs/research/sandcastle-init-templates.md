# Sandcastle `init` templates — primary-source research

Follow-up to `docs/research/sandcastle-loop-engineering.md` (same repo,
`mattpocock/sandcastle`, branch `main`). That doc covered the repo's own
`.github/workflows/` dogfooding loop and the `.claude/skills` mechanism, and
flagged the CLI's `sandcastle init` scaffolding as unexplored. This doc closes
that gap: it does **not** repeat the label-state-machine, `claude` CLI
invocation shape, or skills-symlink findings already established there — refer
back to that file for those. It also does not touch sandboxing/Docker/VM
mechanics beyond naming them, per the task's scope.

All content fetched directly via `raw.githubusercontent.com/mattpocock/sandcastle/main/...`
(`curl`) and the GitHub Trees API (`gh api repos/mattpocock/sandcastle/git/trees/main?recursive=1`),
not from summaries. Every file path below was actually read in full.

**Framing correction vs. the previous research pass**: the earlier doc treated
`sandcastle init` / the CLI / the Planner-Implementer-Reviewer-Merger loop as
a secondary, "local-only, not wired to CI" feature, distinct from the repo's
own GitHub-Actions dogfood workflows (which use `noSandbox()` and plain
`.ts` scripts calling `sandcastle.run()` directly). That framing still holds —
**the four templates investigated here are a completely separate mechanism**
from the `.github/workflows/agent-*.yml` files. They are local/interactive
Docker-sandboxed orchestration scripts scaffolded onto a user's own repo by
running `sandcastle init`, not GitHub Actions. There is no GitHub Actions YAML
anywhere in `src/templates/`.

## 1. The `init` command mechanics

### 1.1 Where it lives

- CLI wiring: `src/cli.ts`, `initCommand` (`Command.make("init", …)`, lines
  156–534, confirmed read in full).
- Template registry + scaffolding logic: `src/InitService.ts` (1109 lines,
  confirmed read in full).
- Template file contents: `src/templates/{blank,simple-loop,sequential-reviewer,parallel-planner,parallel-planner-with-review}/`.
- Design rationale for why templates don't share code: `docs/adr/0009-templates-no-shared-code.md`.
- Confirmed **not** in scope: `.out-of-scope/bundled-workflow-templates.md`
  explicitly documents that Sandcastle deliberately does *not* ship large
  bundled "superpowers"/"freecc"-style workflow templates — the five built-in
  templates are "deliberately minimal, framework-agnostic starting points,"
  and a large opinionated template pack is meant to be distributed
  externally and dropped into the `custom`/blank scaffold path instead
  (referencing GitHub issue #627, "Python node with freecc superpowers").

### 1.2 Template registry (`src/InitService.ts:32-58`)

Five templates are registered, each just a `{name, description, dependencies?}`
tuple — `blank` and `simple-loop` have no extra host dependency;
`parallel-planner` and `parallel-planner-with-review` both declare
`dependencies: ["zod"]` (used for the planner's `<plan>` JSON schema);
`sequential-reviewer` has none. `listTemplates()` / `getTemplateDependencies()`
are the only public reads of this registry.

### 1.3 How the user picks a template — CLI flag or interactive prompt

`sandcastle init` resolves every choice as **"CLI flag wins, otherwise
interactive `@clack/prompts` select, otherwise (if no TTY) hard-fail naming
the missing flag."** Confirmed in `src/cli.ts`:
- `--template <name>` (validated against `listTemplates()` up front; unknown
  name fails immediately, before any prompts run).
- If absent and interactive: `clack.select({ message: "Select a template:", initialValue: "blank", options: templates.map(t => ({ value: t.name, label: t.name, hint: t.description })) })` (`src/cli.ts:377-393`).
- If absent and non-interactive (`process.stdin.isTTY !== true`): fails with
  `` `--template` is required in non-interactive mode (no TTY detected). ``

The same CLI-flag-or-prompt pattern resolves **agent** (`claude-code` default,
also `pi`/`codex`/`cursor`/`opencode`/`copilot` — see §1.4), **model**
(defaults to the agent's `defaultModel`), **sandbox provider** (`docker` or
`podman`, no default — user must choose), and **issue tracker**
(`github-issues` default, also `beads` or `custom` — see §1.5). Three
additional tri-state confirms (`--create-label`, `--build-image`,
`--install-template-deps`) use `clack.confirm(...)`.

### 1.4 What `init` writes to disk

`scaffold()` in `src/InitService.ts:1017-1109`:
1. Fails if `.sandcastle/` already exists ("Remove it first if you want to
   re-initialize").
2. Detects `main.mts` vs `main.ts` filename by checking the host
   `package.json`'s `"type": "module"` field (`detectMainFilename`,
   lines 996-1015).
3. Writes, in parallel (`Effect.all(..., {concurrency: "unbounded"})`):
   - `.sandcastle/<Dockerfile|Containerfile>` — the selected agent's
     Dockerfile template (one of six baked-in Dockerfiles for
     claude-code/pi/codex/cursor/opencode/copilot, each installing that
     agent's CLI via curl or `npm install -g`, plus a
     `{{ISSUE_TRACKER_TOOLS}}` placeholder for CLI tooling like `gh` or
     Beads). Filename depends on the sandbox provider (`Dockerfile` for
     docker, `Containerfile` for podman).
   - `.sandcastle/.gitignore` — `.env`, `logs/`, `worktrees/`.
   - `.sandcastle/.env.example` — built from the agent's `envExample` (e.g.
     `CLAUDE_CODE_OAUTH_TOKEN=`) plus the issue tracker's `envExample` (e.g.
     `GH_TOKEN=` for github-issues; empty for beads).
   - All files from `copyTemplateFiles(templateDir, configDir, mainFilename)`
     — a **verbatim file copy** of every file in `src/templates/<name>/`
     except `template.json`, `.env.example`, and compiled JS/`.d.ts`
     artifacts; `main.mts` is renamed to the detected `mainFilename` on copy
     (`src/InitService.ts:729-755`).
4. Post-copy rewrites (all mutate the just-copied files in place):
   - `rewriteMainTs` (lines 764-815): replaces the placeholder
     `claudeCode` factory import/calls with the selected agent's
     `factoryImport` + model, and the placeholder `docker` sandbox provider
     name with the selected one (`podman`), via targeted regex substitution
     — every template's `main.mts` is authored against `claudeCode`/`docker`
     as the canonical placeholders.
   - `substituteTemplateArgs` (lines 875-910): replaces `{{KEY}}` tokens
     across all text files in `.sandcastle/` using the selected issue
     tracker's `templateArgs` map (`LIST_TASKS_COMMAND`, `VIEW_TASK_COMMAND`,
     `CLOSE_TASK_COMMAND`, `ISSUE_TRACKER_TOOLS`) — this is what turns
     `{{LIST_TASKS_COMMAND}}` in a template's `prompt.md` into a concrete
     `gh issue list --state open --label Sandcastle …` command.
   - `rewritePromptFiles` (lines 822-848): if the user declined the
     "Sandcastle" GitHub label, strips literal `" --label Sandcastle"` from
     every `.md` file (so `gh issue list` isn't filtered by a label that was
     never created).
   - For the `custom` issue tracker only: writes
     `.sandcastle/SETUP_ISSUE_TRACKER.md`, a deliberately-broken-until-configured
     scaffold whose sentinel `LIST_TASKS_COMMAND` (`CUSTOM_LIST_TASKS_SENTINEL`)
     is `echo '…run SETUP_ISSUE_TRACKER.md…' >&2; exit 1` — every Sandcastle
     run hard-fails until a human/agent replaces the sentinel per that doc.
5. After scaffolding, `init` detects the host package manager (npm/pnpm/yarn/bun,
   via `packageManager` field or lockfile) and, if the chosen template
   declares a `zod` dependency and the host doesn't already have it, offers
   to run the right install command.
6. Offers to build the sandbox image now (`sandcastle docker build-image` /
   `podman build-image`), skipped automatically for the `custom` issue
   tracker (nothing valid to build yet).
7. Prints template-specific "Next steps" via `getNextStepsLines()`
   (`src/InitService.ts:622-693`) — e.g. "Templates use
   `copyToWorktree: ["node_modules"]` to copy your host node_modules into the
   sandbox for fast startup," and for reviewer-bearing templates: "Customize
   `.sandcastle/CODING_STANDARDS.md` with your project's standards — the
   reviewer agent loads it during review."

### 1.5 Agents and issue trackers `init` supports (context for the templates)

Six agent factories are registered (`AGENT_REGISTRY`,
`src/InitService.ts:409-476`): `claude-code` (default model
`claude-opus-4-8`), `pi`, `codex`, `cursor`, `opencode`, `copilot` — each with
its own Dockerfile, `.env.example` block, and `setupCommand` for the custom-tracker
setup flow. All five templates are written against `claudeCode` as a
placeholder and rewritten at scaffold time if a different agent is chosen —
templates are agent-agnostic in principle, though every template's own model
choices (opus for planning, sonnet for execution) assume Claude Code by
default.

Three issue trackers (`ISSUE_TRACKER_REGISTRY`,
`src/InitService.ts:530-571`): `github-issues` (default —
`gh issue list --state open --label Sandcastle --limit 100 --json …`),
`beads` (`bd ready --json` / `bd show <ID>` / `bd close <ID> …`), and `custom`
(broken-until-configured, per §1.4). **This is the concrete shape of
`{{LIST_TASKS_COMMAND}}` / `{{VIEW_TASK_COMMAND}}` / `{{CLOSE_TASK_COMMAND}}`
seen in every template's prompt files below.**

### 1.6 Built-in prompt arguments injected by the core, not by templates

Confirmed in `src/run.ts:628-733` (read directly, not part of any template):
every non-inline `run()` call auto-injects two prompt arguments before
user-supplied `promptArgs` are applied —
```
const effectiveArgs = {
  SOURCE_BRANCH: resolvedBranch,
  TARGET_BRANCH: currentHostBranch,
  ...userArgs,
};
```
`TARGET_BRANCH` is always the host repo's current branch at the moment
`run()`/`createSandbox()` was called (captured via `getCurrentBranch(hostRepoDir)`).
This is why `sequential-reviewer/review-prompt.md` and
`parallel-planner{,-with-review}/review-prompt.md` can reference
`{{TARGET_BRANCH}}` in `git diff {{TARGET_BRANCH}}...{{BRANCH}}` without any
template ever setting it explicitly via `promptArgs` — it comes free from the
framework, not from `InitService`'s issue-tracker substitution or any
template-specific code.

## 2. `simple-loop`

Files: `src/templates/simple-loop/{main.mts,prompt.md,template.json}`.

**Mechanism**: a single-phase, single-agent loop. `main.mts` calls
`sandcastle.run()` once with `maxIterations: 3`, `branchStrategy: { type: "merge-to-head" }`,
and `copyToWorktree: ["node_modules"]`. Each of the (up to 3) iterations is
one full agent turn against the same `prompt.md`.

**Sequence once a developer runs `npm run sandcastle`**:
1. Sandbox created (Docker), host `node_modules` copied in,
   `onSandboxReady` hook runs `npm install`.
2. The agent (`claude-sonnet-4-6` by default) receives `prompt.md`, which
   interpolates `!`{{LIST_TASKS_COMMAND}}`` (a shell expression evaluated
   inside the sandbox at the start of each iteration, so the list is always
   fresh) plus `git log --oneline --grep="RALPH" -10` for recent-work context.
3. The prompt casts the agent as **"RALPH — an autonomous coding agent
   working through issues one at a time,"** with an explicit priority order
   (bug fixes > tracer bullets > polish > refactors) and a strict
   **Explore → Plan → Execute (RGR: Red→Green→Repeat→Refactor) → Verify → Commit → Close**
   workflow. Quoted in full from `src/templates/simple-loop/prompt.md`:
   > "Work on **one issue per iteration**. Do not attempt multiple issues in
   > a single iteration." … "Do not close an issue until you have committed
   > the fix and verified tests pass." … "If you are blocked …, leave a
   > comment on the issue and move on — do not close it."
4. The agent itself closes the issue at the end of each successful
   iteration, via the substituted `{{CLOSE_TASK_COMMAND}}` (e.g.
   `gh issue close <ID> --comment "Completed by Sandcastle"`) — **no review
   step, no branch handoff, no merge phase**; the merge-to-head branch
   strategy means the agent's temp branch is merged back to the host's
   current branch automatically by the framework after each iteration
   completes, not by an explicit prompt step.
5. Loop stops after `maxIterations` (3) turns, or early if the agent emits
   `<promise>COMPLETE</promise>` (when the issue list is empty or all
   remaining issues are blocked).

This is the only template with **no explicit outer JS loop** — the
"looping" is entirely `run()`'s own `maxIterations` mechanism, one call.

## 3. `sequential-reviewer`

Files: `src/templates/sequential-reviewer/{main.mts,implement-prompt.md,review-prompt.md,CODING_STANDARDS.md,template.json}`.

**Mechanism**: an explicit `for` loop (`MAX_ITERATIONS = 10`) in `main.mts`,
each pass doing exactly two agent phases sharing one `sandcastle.createSandbox()`
instance (so both phases see the same named branch):

```ts
for (let iteration = 1; iteration <= MAX_ITERATIONS; iteration++) {
  const branch = `sandcastle/sequential-reviewer/${Date.now()}`;
  const sandbox = await sandcastle.createSandbox({ branch, sandbox: docker(), hooks, copyToWorktree });
  try {
    const implement = await sandbox.run({ name: "implementer", maxIterations: 1,
      agent: sandcastle.claudeCode("claude-sonnet-4-6"), promptFile: "./.sandcastle/implement-prompt.md" });
    if (!implement.commits.length) { console.log("…Stopping."); break; }
    await sandbox.run({ name: "reviewer", maxIterations: 1,
      agent: sandcastle.claudeCode("claude-sonnet-4-6"), promptFile: "./.sandcastle/review-prompt.md",
      promptArgs: { BRANCH: branch } });
  } finally { await sandbox.close(); }
}
```

**Sequence per iteration**:
1. **Implement** (`implement-prompt.md`, byte-identical content to
   `simple-loop/prompt.md`'s RALPH workflow/priority-order/rules): picks one
   issue, does RGR, commits with a `RALPH:`-prefixed message, and **closes
   the issue itself** via `{{CLOSE_TASK_COMMAND}}` — closing happens *before*
   review, not gated on it. `maxIterations: 1` forces exactly one issue per
   outer-loop pass (the in-code comment explains: "A higher value lets the
   agent drain the whole backlog onto this one branch in a single pass,
   which defeats the per-issue review").
2. If `implement.commits.length === 0` (no work found / everything blocked),
   the whole outer loop `break`s — no review phase runs, run ends.
3. **Review** (`review-prompt.md`, shared verbatim with the two
   `parallel-planner*` review-prompt files — confirmed via `diff`, zero
   differences): reviews `git diff {{TARGET_BRANCH}}...{{BRANCH}}` (the
   `{{BRANCH}}` prompt arg is the sequential-reviewer's own temp branch;
   `{{TARGET_BRANCH}}` is the framework's built-in, §1.6). Checklist quoted:
   > "Reduce unnecessary complexity and nesting … Avoid nested ternary
   > operators — prefer switch statements or if/else chains … Does the
   > change introduce injection vulnerabilities, credential leaks, or other
   > security issues? … Apply project standards: Follow the coding standards
   > defined in @.sandcastle/CODING_STANDARDS.md"
   On success/failure: **there is no branching logic at all** — the reviewer
   agent either "make[s] the changes directly on this branch … Run tests and
   type checking … Commit describing the refinements" or, "If the code is
   already clean and well-structured, do nothing," then always emits
   `<promise>COMPLETE</promise>`. There is **no loop-back to the implementer**
   on review failure — unlike the main repo's own dogfood
   `agent-review.yml`/`agent-implement-pr.yml` pairing (documented in the
   prior research file, §4), this template's reviewer is empowered to push
   fixes directly rather than requesting another implement pass.
4. `CODING_STANDARDS.md` is a placeholder file with HTML-comment examples
   under `## Style` / `## Testing` / `## Architecture` headings, to be
   filled in by the user; the reviewer prompt's `@.sandcastle/CODING_STANDARDS.md`
   reference is Claude Code's file-inclusion syntax.

**Comparison to the dogfood workflow** (prior doc, §1/§4): the sandcastle
repo's own `.github/workflows/agent-implement.yml` → `agent-review.yml` chain
is GitHub-Actions-triggered (label-based), uses `pull_request_target`, posts a
formal GitHub PR review object via `gh api .../reviews`, and marks the PR
ready for human merge — no auto-merge, a human decides. `sequential-reviewer`
is the opposite in every one of those respects: it's a local loop with no
GitHub Actions or PR involved at all, the reviewer edits code directly on the
implementer's own branch (not via review comments), and there's no PR /
human-approval gate — the branch is simply left committed, ready for whatever
merge/PR step the user layers on top (this template doesn't push or open a
PR; that's out of scope of what was scaffolded).

## 4. `parallel-planner`

Files: `src/templates/parallel-planner/{main.mts,plan-prompt.md,implement-prompt.md,merge-prompt.md,template.json}`.

**Mechanism**: an explicit `for` loop (`MAX_ITERATIONS = 10`), each pass
running three phases: **Plan → Execute (parallel) → Merge**.

### Plan phase — how parallelizability is decided and expressed

`plan-prompt.md` gives the agent (opus, `claude-opus-4-8`, "for deeper
reasoning") the filtered open-issue list and this exact instruction, quoted
in full:
> "Analyze the open issues and build a dependency graph. For each issue,
> determine whether it **blocks** or **is blocked by** any other open issue.
> An issue B is **blocked by** issue A if: B requires code or infrastructure
> that A introduces / B and A modify overlapping files or modules, making
> concurrent work likely to produce merge conflicts / B's requirements
> depend on a decision or API shape that A will establish. An issue is
> **unblocked** if it has zero blocking dependencies on other open issues.
> For each unblocked issue, assign a branch name using the exact format
> `sandcastle/issue-{id}` (no slug or other suffix). This must be
> deterministic so that re-planning the same issue always produces the same
> branch name and accumulated progress is preserved."

Output schema (data structure): the agent emits a `<plan>…</plan>`-wrapped
JSON object, `{"issues": [{"id": "42", "title": "Fix auth bug", "branch": "sandcastle/issue-42"}]}`.
`main.mts` extracts and validates this via `sandcastle.Output.object({ tag: "plan", schema: planSchema })`
where `planSchema` is a **Zod** schema (`z.object({ issues: z.array(z.object({ id, title, branch }: z.string()x3)) })`)
— this is the concrete reason `parallel-planner*` templates declare `zod` as
a host dependency in `TEMPLATES` (§1.2) and why `init` offers to install it.
A malformed/missing `<plan>` tag throws `StructuredOutputError` and aborts
the run (per the in-code comment). If every issue is blocked, the prompt
instructs the planner to include "the single highest-priority candidate"
rather than emitting an empty plan, so the loop still makes progress; a
genuinely empty backlog is signaled with `<plan>{"issues": []}</plan>`,
which makes `main.mts` `break` out of the outer loop entirely.

### Execute phase — branch creation/tracking

`Promise.allSettled(issues.map(issue => sandcastle.run({ branchStrategy: { type: "branch", branch: issue.branch }, maxIterations: 100, agent: claudeCode("claude-sonnet-4-6"), promptFile: "./.sandcastle/implement-prompt.md", promptArgs: { TASK_ID, ISSUE_TITLE, BRANCH: issue.branch } })))`
— one independent sandbox + agent per planned issue, each pinned to its own
named branch (the deterministic `sandcastle/issue-{id}` from the plan),
running concurrently. `Promise.allSettled` means one agent's rejection
(network error, sandbox crash) doesn't cancel siblings; failures are logged
per-issue afterward.

`implement-prompt.md` (shared verbatim with `parallel-planner-with-review`,
confirmed via `diff`): fixes exactly one issue on its assigned `{{BRANCH}}`,
following the same RGR/typecheck-test/`RALPH:`-prefixed-commit shape as the
other templates, but explicitly **does not close the issue**:
> "If the task is not complete, leave a comment on the issue with what was
> done. **Do not close the issue - this will be done later.**" … "ONLY WORK
> ON A SINGLE TASK."

Post-execution, `main.mts` filters to `completedIssues`/`completedBranches` —
only agents whose run produced `commits.length > 0` — before the merge
phase; issues whose agent made zero commits are silently dropped from this
cycle (they remain open and may be re-planned next iteration).

### Merge phase — trigger and conflict handling

Triggered unconditionally once `completedBranches.length > 0` (skipped with
`continue` if the round produced zero commits anywhere). One agent
(`claude-sonnet-4-6`, "sufficient for merge conflict resolution" per the
in-code comment) runs `merge-prompt.md` with `{{BRANCHES}}` (a markdown list
of branch names) and `{{ISSUES}}` (a markdown list of `id: title` pairs).
Quoted in full — this is the actual conflict-handling instruction:
> "For each branch: 1. Run `git merge <branch> --no-edit` 2. If there are
> merge conflicts, resolve them intelligently by reading both sides and
> choosing the correct resolution 3. After resolving conflicts, run
> `npm run typecheck` and `npm run test` to verify everything works 4. If
> tests fail, fix the issues before proceeding to the next branch. After all
> branches are merged, make a single commit summarizing the merge."
The merge agent then closes each merged issue itself via
`{{CLOSE_TASK_COMMAND}}` — this is the only phase, across the whole
`parallel-planner` template, that closes issues. Conflict resolution is
entirely the merge agent's judgment call (sequential `git merge` per branch,
one at a time, in the order the branches list was built) — there is no
separate conflict-detection tooling or automated fallback; if the agent's
resolution breaks tests it's instructed to "fix the issues before proceeding
to the next branch," but nothing in the prompt or script forces a retry loop
if it can't.

The outer `for` loop then repeats (up to `MAX_ITERATIONS`), so issues
unblocked only after this round's merges get planned and executed on the
next pass.

## 5. `parallel-planner-with-review`

Files: `src/templates/parallel-planner-with-review/{main.mts,plan-prompt.md,implement-prompt.md,review-prompt.md,merge-prompt.md,CODING_STANDARDS.md,template.json}`.

Confirmed via `diff`: `plan-prompt.md`, `implement-prompt.md`, `merge-prompt.md`,
and `CODING_STANDARDS.md` are **byte-identical** to their `parallel-planner`
/ `sequential-reviewer` counterparts; `review-prompt.md` is byte-identical to
`sequential-reviewer`'s. This is a direct, intentional consequence of ADR
0009 (§1): "the existing duplication of `CODING_STANDARDS.md` between
`sequential-reviewer` and `parallel-planner-with-review` is the intended
shape."

**Mechanism**: identical Plan phase to `parallel-planner` (same opus agent,
same `<plan>` JSON schema, same dependency-graph instructions). The
difference is entirely in the Execute phase, which becomes **Execute +
Review per branch**:

```ts
const settled = await Promise.allSettled(
  issues.map(async (issue) => {
    const sandbox = await sandcastle.createSandbox({ branch: issue.branch, sandbox: docker(), hooks, copyToWorktree });
    try {
      const implement = await sandbox.run({ name: "implementer", maxIterations: 100, ... implement-prompt.md ... });
      if (implement.commits.length > 0) {
        const review = await sandbox.run({ name: "reviewer", maxIterations: 1, ... review-prompt.md ...,
          promptArgs: { BRANCH: issue.branch } });
        return { ...review, commits: [...implement.commits, ...review.commits] };
      }
      return implement;
    } finally { await sandbox.close(); }
  }),
);
```

Each planned issue gets its **own `createSandbox()`** (unlike bare
`parallel-planner`, which calls `sandcastle.run()` directly per issue without
an explicit shared-sandbox object) so that the implementer and reviewer for
*that issue* share the same sandbox/branch — mirroring `sequential-reviewer`'s
pattern, but N of them running concurrently instead of one at a time. Review
only runs `if (implement.commits.length > 0)` — an implementer that made no
commits skips review entirely and its result passes through unchanged. The
review step's own behavior (fix-in-place or no-op, always
`<promise>COMPLETE</promise>`, no loop-back to the implementer) is identical
to `sequential-reviewer`'s (§3) — read directly, same file.

Merge phase, branch-name scheme, conflict handling, and the outer
`MAX_ITERATIONS` loop are otherwise identical to bare `parallel-planner`
(§4) — same `merge-prompt.md`, same `{{BRANCHES}}`/`{{ISSUES}}` args, same
sequential `git merge <branch> --no-edit` per-branch conflict resolution, same
issue-closing-happens-only-at-merge behavior.

**Net difference from `parallel-planner`**: exactly one added phase (a
per-branch reviewer, gated on the implementer having produced commits,
running in the same sandbox/branch as its implementer) — no change to
planning, branching, or merging logic. **Net difference from
`sequential-reviewer`**: the same implement→review pairing, but N pairs run
concurrently across independently-planned branches (via a Plan phase) instead
of one pair per outer-loop iteration sequentially.

## 6. Cross-template summary table

| Template | Phases | Branch strategy | Who closes issues | Review→implement loop-back? |
|---|---|---|---|---|
| `simple-loop` | 1 (implement) | `merge-to-head` (framework-managed) | implementer, per issue, each iteration | n/a (no review) |
| `sequential-reviewer` | 2 (implement, review), sequential | one temp branch per outer iteration, shared via `createSandbox` | implementer (before review runs) | no — reviewer edits in place or no-ops |
| `parallel-planner` | 3 (plan, execute×N parallel, merge) | one deterministic `sandcastle/issue-{id}` branch per planned issue | merger, after all merges | n/a (no review) |
| `parallel-planner-with-review` | 3, but execute phase is implement+review pairs×N parallel | same deterministic per-issue branches, `createSandbox` shared per pair | merger, after all merges | no — reviewer edits in place or no-ops, same as sequential-reviewer |

None of the four templates re-runs the implementer after a failed review —
in every reviewer-bearing template the reviewer is empowered to commit fixes
itself rather than bouncing back. This differs from the pattern the prior
research file documents for the main repo's own dogfooding workflow, where a
human must manually re-apply the `agent:implement` label to an already-reviewed
PR to get another implement pass (no auto-loop-back there either, but for a
different mechanical reason — GitHub label re-triggering vs. these templates'
prompt design choice).

## 7. Open questions / gaps for life-ledger port

- **No GitHub Actions involvement anywhere in `src/templates/`.** All four
  templates are local Node/TypeScript scripts (`main.mts`) run via
  `npx tsx .sandcastle/main.mts`, invoked manually or via an `npm run
  sandcastle` script — not CI-triggered. Porting these to life-ledger as
  "standalone scripts" is actually more direct than porting the dogfood
  workflows: no GitHub Actions YAML, label state machine, or `pull_request_target`
  quirks to reproduce — but it does mean the loop is not "hands-off" in the
  CI sense; someone/something has to invoke `npx tsx .sandcastle/main.mts`
  (a cron, a systemd timer, a long-running `run-daemon.sh`-style process, or
  a human) since there is no GitHub event trigger driving it at all.
- **Every template depends on `@ai-hero/sandcastle`'s core primitives**
  (`run()`, `createSandbox()`, `claudeCode()`, `docker()`, `Output.object()`,
  the built-in `{{SOURCE_BRANCH}}`/`{{TARGET_BRANCH}}` injection from
  `src/run.ts`, `branchStrategy` types `head`/`branch`/`merge-to-head`, the
  `hooks.sandbox.onSandboxReady` lifecycle hook, `copyToWorktree`). None of
  this is reimplemented in the templates themselves — per ADR 0009, templates
  are deliberately "self-contained... may not import from... any internal
  Sandcastle source," but they still import the *published npm package* as a
  hard runtime dependency. Reimplementing these templates without
  `@ai-hero/sandcastle` means life-ledger would need its own equivalents of:
  Docker-sandboxed worktree-per-run execution, the `<plan>`/`<output>` tag
  extraction+schema-validation pattern, and the `SOURCE_BRANCH`/`TARGET_BRANCH`
  auto-injection convention that several review prompts rely on implicitly.
- **The `init` scaffolding itself (Effect-based, `src/InitService.ts`) is
  non-trivial to reproduce as "a standalone script"**: it does host
  package-manager detection (npm/pnpm/yarn/bun via lockfile or
  `packageManager` field), `.mts`-vs-`.ts` detection via `package.json`
  `"type"`, six different agent Dockerfile templates keyed by a registry, an
  issue-tracker `{{KEY}}`-substitution system with three registered trackers
  plus a deliberately-broken `custom` fallback, and post-copy regex rewrites
  of `main.ts(x)` to swap the placeholder `claudeCode`/`docker` symbols for
  whichever agent/sandbox-provider the user picked. A life-ledger port could
  reasonably skip most of this generality (agent choice, sandbox-provider
  choice, issue-tracker choice) and hard-code "Claude Code + Docker + GitHub
  Issues," which would collapse `InitService.ts`'s ~1100 lines down
  considerably — but that's a design decision, not something discoverable
  from the source alone.
- **Not verified**: whether any released npm version of `@ai-hero/sandcastle`
  (vs. this `main`-branch source) actually ships these five templates as-is —
  this research reads `main` branch source directly per the task's method,
  not a specific published version's dist. The previous research file
  recorded `package.json` version `0.12.0` at the time of that pass; this
  pass did not re-check whether that version number has since changed, since
  it wasn't relevant to the templates' content.
- **`src/InitService.agents.test.ts`, `.packageManager.test.ts`,
  `.sandboxProviders.test.ts`, and `.test.ts`** exist alongside
  `InitService.ts` (confirmed present in the tree) but were not read — they
  likely pin exact expected scaffold output per agent/package-manager/sandbox
  combination and would be a useful cross-check before porting the rewrite
  logic (`rewriteMainTs`'s regex-based symbol substitution in particular is
  the kind of thing tests would catch edge cases for, e.g. an agent name that
  collides with a common word).
- **`docs/content/docs/*.mdx`** (the docs site content, per the earlier
  research pass's framing) was not fetched in this pass — the task's primary
  targets (`InitService.ts`, `cli.ts`, the five `src/templates/*` dirs, the
  ADR, and the out-of-scope note) fully answered the brief without needing
  the rendered docs site prose, but if life-ledger wants the user-facing
  "how to run `sandcastle init`" narrative (vs. the source-level mechanics
  documented here), that's the remaining unread source.
