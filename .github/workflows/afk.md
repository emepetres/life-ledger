---
description: >
  AFK production workflow — one `afk` label on a ready issue drives a headless
  `/implement` run end-to-end, from the label to a draft PR waiting for review.
  Three-phase topology (dispatch → agent → finalize) with a strict token split:
  the LLM provider key lives only in the read-only agent job, the write tokens
  only in the finalize (safe-outputs) job, and they never coexist. Engine seam is
  engine-neutral (copilot BYOK against an OpenAI-compatible endpoint via the
  `LLM_*` secrets) so a second engine is a change to the `engine:` block alone.
  Spec: #77. This ticket: #81.

# Per-run title (Actions tab): `run-name` supports github/inputs/vars but NOT
# `needs`, and the top-level `name` (derived from the H1 below) supports NO
# expression contexts at all — so the dynamic issue number lives here, and the
# H1 stays a static literal.
run-name: "AFK · `/implement` on issue #${{ github.event.issue.number || inputs.issue_number }}"

# ── Phase boundaries at a glance ────────────────────────────────────────────
#   dispatch (custom job)  issues:write   — role-gated to maintainers; derives
#                                            skill+branch+concurrency via
#                                            internal/afk; claims afk:running;
#                                            clears stale terminal labels.
#   agent    (agent job)   *read-only*     — holds the LLM key (engine.env);
#                                            loads the issue, runs the inlined
#                                            /implement method, emits ONLY via
#                                            safe-outputs.
#   finalize (safe_outputs) write tokens   — opens/updates the draft PR, posts
#                                            the outcome comment, lands the issue
#                                            in needs-review.
# The LLM key (agent) and a write token (finalize) are never in the same job.

on:
  # Reactive, single-issue entry point: applying the persistent `afk` label.
  # The label stays on the item (distinct from `ready-for-agent`); the dispatch
  # phase flips it to `afk:running` as the claim.
  issues:
    types: [labeled]
    names: [afk]
  # Manual sweep / re-check: dispatch one issue by number after being offline.
  workflow_dispatch:
    inputs:
      issue_number:
        description: "Issue number to dispatch (manual sweep / re-check)."
        required: true
        type: string

  # Only maintainers can spend credits or run code — a drive-by label from a
  # non-maintainer is refused at activation, before any AI cost is incurred.
  roles: [admin, maintainer]

# Agent-job permissions: READ-ONLY. All writes are deferred to the finalize
# (safe-outputs) job and the dispatch job, each with its own scoped token.
permissions:
  contents: read
  issues: read
  pull-requests: read

# ── Engine seam (engine-neutral; copilot BYOK for v1) ───────────────────────
# The only per-engine block. Swapping engines is a change here and nowhere else.
# BYOK isolates the real credential in gh-aw's API-proxy sidecar; the neutral
# LLM_* secrets map to the copilot provider vars, and the model is a repo
# Actions *variable* so it swaps via Settings without a .lock.yml recompile.
engine:
  id: copilot
  max-turns: 30
  # Repo-wide cap on concurrent AFK agent runs (a spend guardrail, spec #77 D4).
  # A GitHub concurrency group is a mutex, so this static group serialises agent
  # execution to one at a time across the whole workflow — the simplest safe cap.
  # Per-spec-branch serialisation of the *push* is separate (safe-outputs below).
  concurrency:
    group: "gh-aw-afk-agent"
  env:
    COPILOT_PROVIDER_BASE_URL: ${{ secrets.LLM_BASE_URL }}   # REQUIRED — activates BYOK
    COPILOT_PROVIDER_API_KEY: ${{ secrets.LLM_API_KEY }}     # the real key, sidecar-isolated
    COPILOT_PROVIDER_TYPE: openai                            # OpenAI-compatible endpoint
    COPILOT_MODEL: ${{ vars.LLM_MODEL }}                     # a repo variable, not a secret

# Egress allowlist: the LLM provider host, GitHub, and the Go toolchain hosts an
# /implement run needs. A compromised or misled agent cannot exfiltrate elsewhere.
# The provider host must be a LITERAL here: network.allowed is a compile-time,
# reviewer-auditable security artifact and rejects every `${{ }}` expression
# (secret OR variable), and gh-aw does NOT auto-extract the host when the base URL
# comes from a secret — so without this literal the firewall blocks the BYOK
# api-proxy's upstream call. The host is not a credential (only LLM_API_KEY is);
# the full URL still lives in the LLM_BASE_URL secret feeding COPILOT_PROVIDER_BASE_URL.
network:
  allowed:
    - defaults
    - go
    - "forge.plainconcepts.com" # LLM provider host (== host of secrets.LLM_BASE_URL)

# Per-skill tools: /implement gets the local kit (edit + a bash allowlist for the
# Go toolchain and gh) plus the GitHub read toolsets. No open network fetch tool.
tools:
  edit:
  bash:
    - "go"
    - "gofmt"
    - "git"
    - "gh issue view"
    - "gh issue list"
    - "gh pr list"
    - "gh pr view"
  github:
    toolsets: [issues, pull_requests, repos]

runtimes:
  go:
    version: "1.26"

# Spend guardrails — configurable with sane defaults. A tripped limit is a
# failure (surfaced, recoverable), never an unbounded bill.
timeout-minutes: 20
max-ai-credits: 1000

# ── Finalize phase (safe-outputs) — holds the write tokens ──────────────────
# The agent requests these; a separate permission-scoped job executes them.
safe-outputs:
  # Serialize finalize per resolved spec branch so two agents heading for the
  # same PR branch never race on the push (spec #77, user story 13/20).
  concurrency-group: "afk-finalize-${{ needs.dispatch.outputs.branch }}"
  # Create path: a draft PR to `main`, `[afk] `-prefixed, `afk`-labelled.
  # `Part of #<n>` linkage (never `Fixes`) is written by the agent into the PR
  # body, so auto-close is disabled — closure stays a human decision.
  create-pull-request:
    draft: true
    title-prefix: "[afk] "
    labels: [afk]
    base-branch: main
    auto-close-issue: false
    preserve-branch-name: true
    if-no-changes: warn
  # Push path: when a PR already exists for the branch, accumulate onto it.
  push-to-pull-request-branch:
    target: "*"
    required-title-prefix: "[afk] "
    if-no-changes: warn
  # The in-agent typecheck / test / review outcome, posted for triage.
  add-comment:
    max: 3
    target: "*"
  # State transition to needs-review + clearing the afk:running claim.
  add-labels:
    max: 4
  remove-labels:
    max: 4

# ── Dispatch phase (custom job) — the activation, with issues:write ─────────
# Role-gated by `on.roles` above. Derives the routing decision from the pure
# internal/afk core (spec #79), then claims the issue and clears stale labels.
# It never holds the LLM key. The agent job is gated on its `action` output.
jobs:
  dispatch:
    # Gate on the maintainer role-check (pre_activation) BEFORE any issues:write
    # label op runs — a non-maintainer's drive-by `afk` label never mutates state
    # or reaches the agent (spec #77, user story 35).
    needs: [pre_activation]
    if: needs.pre_activation.outputs.activated == 'true'
    runs-on: ubuntu-latest
    permissions:
      contents: read
      issues: write
    outputs:
      action: ${{ steps.decide.outputs.action }}
      skill: ${{ steps.decide.outputs.skill }}
      branch: ${{ steps.decide.outputs.branch }}
      issue_number: ${{ steps.decide.outputs.issue_number }}
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true
      - name: Derive the dispatch decision (internal/afk)
        id: decide
        env:
          GH_TOKEN: ${{ github.token }}
          ISSUE_NUMBER: ${{ github.event.issue.number || inputs.issue_number }}
        run: |
          set -euo pipefail

          # Everything flows through env / jq --arg — never inlined into the
          # script — so a `$`, backtick, or quote in an issue body is inert
          # (AGENTS.md: never inline injected values into run: scripts).
          issue_json=$(gh issue view "$ISSUE_NUMBER" --json number,title,body,labels)
          title=$(jq -r '.title' <<<"$issue_json")
          body=$(jq -r '.body // ""' <<<"$issue_json")
          labels=$(jq -c '[.labels[].name]' <<<"$issue_json")

          # Parent spec via "Part of #<n>" — feeds rungs 1-2 of the branch ladder.
          parent_json=null
          parent_n=$(grep -oiE 'part of #[0-9]+' <<<"$body" | grep -oE '[0-9]+' | head -1 || true)
          if [ -n "${parent_n:-}" ]; then
            p=$(gh issue view "$parent_n" --json number,title,body)
            parent_json=$(jq -c '{number,title,body}' <<<"$p")
          fi

          # Existing remote branches — detect an already-created shared branch.
          branches=$(git ls-remote --heads origin | sed 's#.*refs/heads/##' \
            | jq -R . | jq -sc .)

          # Open blockers: "#<n>" refs under a "Blocked by" heading, still OPEN.
          blockers='[]'
          blocked_section=$(printf '%s\n' "$body" | sed -n '/[Bb]locked by/,/^##[^#]/p')
          for n in $(printf '%s\n' "$blocked_section" | grep -oE '#[0-9]+' \
              | grep -oE '[0-9]+' | sort -u); do
            state=$(gh issue view "$n" --json state -q .state 2>/dev/null || echo "")
            if [ "$state" = "OPEN" ]; then
              blockers=$(jq -c ". + [$n]" <<<"$blockers")
            fi
          done

          input=$(jq -nc \
            --argjson number "$ISSUE_NUMBER" \
            --arg title "$title" \
            --arg body "$body" \
            --argjson labels "$labels" \
            --argjson parent "$parent_json" \
            --argjson branches "$branches" \
            --argjson blockers "$blockers" \
            '{number:$number, title:$title, labels:$labels, body:$body,
              parent:$parent, existingBranches:$branches, openBlockers:$blockers}')

          # afk-dispatch appends skill/branch/action/reason/concurrency_group to
          # $GITHUB_OUTPUT itself; add the resolved issue number for later phases.
          echo "$input" | go run ./cmd/afk-dispatch
          echo "issue_number=$ISSUE_NUMBER" >> "$GITHUB_OUTPUT"
      - name: Claim the issue (afk -> afk:running) and clear stale terminal labels
        if: steps.decide.outputs.action == 'run'
        env:
          GH_TOKEN: ${{ github.token }}
          ISSUE_NUMBER: ${{ steps.decide.outputs.issue_number }}
        run: |
          set -euo pipefail
          gh issue edit "$ISSUE_NUMBER" --add-label "afk:running" --remove-label "afk"
          # Auto-clear stale terminal labels so a re-run starts clean.
          gh issue edit "$ISSUE_NUMBER" --remove-label "afk:failed" || true
          gh issue edit "$ISSUE_NUMBER" --remove-label "needs-review" || true
      - name: Refuse (HITL-only / unknown / ambiguous) — strip afk, hand back
        if: steps.decide.outputs.action == 'refuse'
        env:
          GH_TOKEN: ${{ github.token }}
          ISSUE_NUMBER: ${{ steps.decide.outputs.issue_number }}
          REASON: ${{ steps.decide.outputs.reason }}
        run: |
          set -euo pipefail
          gh issue edit "$ISSUE_NUMBER" --remove-label "afk" || true
          gh issue comment "$ISSUE_NUMBER" --body "🚫 AFK refused: ${REASON}"
      - name: Skip (blocked) — leave afk for a later re-check
        if: steps.decide.outputs.action == 'skip'
        env:
          GH_TOKEN: ${{ github.token }}
          ISSUE_NUMBER: ${{ steps.decide.outputs.issue_number }}
          REASON: ${{ steps.decide.outputs.reason }}
        run: |
          set -euo pipefail
          echo "::notice::AFK skipped #${ISSUE_NUMBER}: ${REASON}"

  # Gate the generated agent job on a RUN decision. `needs`/`if` are merged with
  # the compiler-generated dependencies and conditions.
  agent:
    needs: [dispatch]
    if: needs.dispatch.outputs.action == 'run'

# Put the agent on the resolved branch before it starts (created from main, or
# tracking the shared spec branch when one already exists on the remote).
steps:
  - name: Check out the resolved AFK branch
    env:
      BRANCH: ${{ needs.dispatch.outputs.branch }}
    run: |
      set -euo pipefail
      if git ls-remote --exit-code --heads origin "$BRANCH" >/dev/null 2>&1; then
        git fetch origin "$BRANCH"
        git checkout -B "$BRANCH" "origin/$BRANCH"
      else
        git checkout -B "$BRANCH"
      fi
---

# AFK · `/implement`

You are the agent phase of the AFK dispatch system (spec #77). The dispatch phase
has already role-gated the maintainer, resolved the skill and branch, and claimed
the issue (`afk:running`). Your job is to run the resolved skill headlessly and
hand the result back through safe-outputs — you hold no write token, so you never
push, open a PR, or edit a label yourself.

{{#runtime-import shared/afk-implement-method.md}}
