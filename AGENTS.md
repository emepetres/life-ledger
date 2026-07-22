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
