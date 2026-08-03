---
# Shared component (no `on:` field) — the invariant AFK `/implement` prompt.
#
# This is the ONE source of truth for how a headless agent runs `/implement`
# on a ready ticket (spec #77, user stories 16-17, 56). It is imported verbatim
# into the body of the production workflow `afk.md`; keeping the method here —
# not copied into each engine's workflow — is what makes the prompt invariant
# across the production (copilot BYOK) and the throwaway opencode validation
# workflows. Only the `engine:` block differs between them; this method does not.
#
# It inlines `/implement`'s method because that skill is
# `disable-model-invocation: true` and cannot be auto-fired headless (see the
# sandcastle research, docs/research/sandcastle-reference.md). `/tdd` and
# `/code-review` are model-invocable and are referenced by name so the agent
# loads and follows them from the checked-out `.claude/skills` / `.agents/skills`.
---

# TASK — run `/implement` on issue #${{ needs.dispatch.outputs.issue_number }}

You are running the repository's `/implement` skill **headlessly** on one ready,
unblocked issue, away from any human. The way has already been cleared — the
ticket is specified and its blocking dependencies are resolved — so your job is
the mechanical execution the maintainer delegated. Follow this method exactly;
it is the inlined `/implement` method (that skill is user-invoked only and cannot
be typed here, so its steps are written out below).

## 1. Load the ticket

Read the full ticket, **including its comments**, as your source of truth:

```
gh issue view ${{ needs.dispatch.outputs.issue_number }} --comments
```

The dispatch phase has already resolved the routing for you — do not re-derive it:

- **Skill:** `${{ needs.dispatch.outputs.skill }}` (this is an `/implement` run).
- **Branch:** you are on `${{ needs.dispatch.outputs.branch }}`, already checked
  out from `main`. Commit all your work onto this branch. Do not switch branches.

## 2. Read the project context before changing code

Match the repo's existing vocabulary and conventions. Before editing, read:

- `CONTEXT.md` and `AGENTS.md` (repo-level agent instructions);
- `docs/adr/` for any ADR touching the area you are changing;
- `docs/architecture.md` for the module map;
- the closest existing sibling code — write code that reads like what surrounds it.

## 3. Execute test-first at pre-agreed seams — follow `/tdd`

Follow the repository's **`/tdd`** skill. Where a test seam already exists for the
work (or the ticket names one), do red → green → refactor in **vertical slices**:

1. **RED** — write one failing test at the seam that specifies the next behavior.
2. **GREEN** — write the smallest correct change that makes it pass.
3. **REPEAT** — one slice at a time; let each test respond to what the last taught you.
4. **REFACTOR** — only once green, and only behavior-preserving changes.

Test **external behavior at a seam**, never private helpers — the same discipline
as the existing `internal/server/*_test.go` and `internal/expense/*_test.go` tests.
Do not write tests at a seam the ticket did not agree on; not everything is tested.

This is a **Go** repository (`go.mod`, module `github.com/emepetres/life-ledger`).
Use the Go toolchain, not npm:

- **Typecheck / vet regularly** as you work: `go build ./...` then `go vet ./...`.
- **Run focused tests** for the package you are changing: `go test ./internal/<pkg>/...`.
- **Run the full suite once at the end**, before you finalize: `go test ./...`, and
  confirm the tree is `gofmt`-clean: `gofmt -l .` must print nothing.

## 4. Review your own work — follow `/code-review`

When the implementation is done, follow the repository's **`/code-review`** skill
against `main` (`git diff main...HEAD`), on both axes it defines:

- **Standards** — does the change follow this repo's documented coding standards?
- **Spec** — does it faithfully implement what issue
  #${{ needs.dispatch.outputs.issue_number }} asked for?

Fix what the review surfaces before finalizing. Record the review's outcome — you
report it in step 6.

> **Known limitation (recorded, not a blocker).** Under a headless engine
> `/code-review`'s parallel sub-agents degrade to a **single inline pass**. Do the
> review inline; note in your outcome comment that it was a single-pass review.

## 5. Commit your work

Make one or more commits on `${{ needs.dispatch.outputs.branch }}` with
conventional-commit messages describing what you did. Do **not** push the branch
yourself, do **not** open or edit a pull request yourself, and do **not** edit
issue labels yourself — the finalize phase does all of that through safe-outputs
(you have no write token). Just commit.

## 6. Hand back through safe-outputs only

You emit results **exclusively** through the safe-output tools — never by pushing,
opening a PR, or editing labels directly. Do all of the following:

- **Open or update the PR.** First check whether an open pull request already
  exists for the head branch `${{ needs.dispatch.outputs.branch }}`
  (`gh pr list --head ${{ needs.dispatch.outputs.branch }} --state open`):
  - **If none exists**, use the **create-pull-request** safe output. It is a
    **draft** PR against `main`, its title is prefixed `[afk] `, it carries the
    `afk` label, and its body links the source issue with **`Part of #${{ needs.dispatch.outputs.issue_number }}`**
    — never `Fixes`/`Closes` (closure stays a human decision).
  - **If one already exists**, use the **push-to-pull-request-branch** safe output
    to add your commits to that existing PR branch (one PR per spec branch).
- **Post the outcome comment.** Use the **add-comment** safe output to post the
  concrete outcome of this run so the maintainer can triage without opening the
  Actions logs: the result of `go build`/`go vet` (typecheck), of `go test ./...`
  (tests — pass/fail with counts), and of the inline `/code-review` (single-pass;
  what it found and what you fixed). If the diff was **empty** (nothing to change),
  say so plainly — an empty-diff run is a valid success, not a failure.
- **Land the issue in `needs-review`.** Use the label safe outputs to add
  `needs-review` and remove the `afk:running` claim from issue
  #${{ needs.dispatch.outputs.issue_number }}, so the maintainer sees it is waiting.

Never merge or approve the PR, and never enable auto-merge — the gate to `main` is
always a human review plus branch protection.
