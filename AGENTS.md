# AGENTS.md

## Agent skills

### Issue tracker

Issues live in GitHub Issues (emepetres/life-ledger), via the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Triage labels

Default vocabulary: needs-triage, needs-info, ready-for-agent, ready-for-human, wontfix. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: root `CONTEXT.md` + `docs/adr/`. See `docs/agents/domain.md`.

### Architecture

Living system map — modules, request flow, deployment topology — in
`docs/architecture.md`. Keep it current as modules are added.

### Documentation conventions

When creating or updating documentation in this repo, write shell scripts in
**PowerShell Core (`pwsh`)** — the target runtime is Windows. Use ```pwsh fenced
code blocks, not `bash`/`sh`/`shell`. Idiomatic translations:

- `FOO="bar"` → `$FOO = "bar"`
- `VAR=$(cmd)` → `$VAR = cmd`
- Quoted args like `"$VAR"` → `$VAR` (pwsh does not word-split on whitespace)
- Line continuation `\` → backtick `` ` ``
- `echo -n "$X" | gh secret set` → `$X | gh secret set` (gh trims trailing newlines)
- Embedding a variable inside single-quoted JSON → here-string `@'...'@` + `-replace`
- `openssl rand -base64 32` → `[Convert]::ToBase64String([System.Security.Cryptography.RandomNumberGenerator]::GetBytes(32))`
- `ENV=val cmd` (env-prefix) → `$Env:ENV = "val"; cmd`

### GitHub Actions: never inline injected values into `run:` scripts

`${{ secrets.* }}`, `${{ inputs.* }}`, `${{ vars.* }}`, and `${{ github.* }}`
are **text substitution** — GitHub pastes the value verbatim into the bash
script _before_ bash parses it. If the value contains `$`, backtick, `"`, or `\`
(e.g. a bcrypt hash `$2a$10$…`, a Base64 key, JSON), bash re-interprets those
characters at parse time. With `set -u` this surfaces as
`$2: unbound variable`; without it the value is silently corrupted.

Always route injected values through the step's (or job's) `env:` block and
reference them as shell variables. Bash expands a `$VAR` once and does **not**
re-scan the expanded text for more `$` tokens, so the embedded `$2a$…` survives
intact:

```yaml
env:
  PASSWORD_HASH: ${{ secrets.LIFELEDGER_PASSWORD_HASH }} # safe
steps:
  - run: |
      set -euo pipefail
      az … --parameters passwordHash="$PASSWORD_HASH"    # safe
```

Anti-pattern (do not do this):

```yaml
- run: |
    set -euo pipefail
    az … --parameters passwordHash="${{ secrets.LIFELEDGER_PASSWORD_HASH }}"
    # bcrypt hash pasted in as $2a$10$… → bash tries to expand $2 → crash
```

This applies to every `run:` in every workflow, not just the infra one.
