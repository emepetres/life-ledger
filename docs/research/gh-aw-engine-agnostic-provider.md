# Making gh-aw engine-agnostic against an arbitrary OpenAI-compatible endpoint

Research for wayfinder ticket **#67**. Investigates how [GitHub Agentic Workflows (gh-aw)](https://github.github.com/gh-aw/)
can be pointed at an **arbitrary OpenAI-compatible endpoint** (the classic `OPENAI_BASE_URL` + `OPENAI_API_KEY`
contract) while keeping **Matt Pocock's skills** (installed in this repo at `.agents/skills/` and `.claude/skills/`)
running **unchanged**.

Facts current as of **2026-07-31**. Every claim is cited to a primary source: the gh-aw docs source
(`docs/src/content/docs/**` in `githubnext/gh-aw`, which renders to `github.github.com/gh-aw`), the gh-aw Go
source (`pkg/workflow/**`), gh-aw ADRs (`docs/adr/**`), and official Anthropic Claude Code docs. Verbatim config
is quoted from the repo source (`githubnext/gh-aw@main`, fetched 2026-07-31).

## The question

> How can gh-aw be made engine-agnostic and target an arbitrary OpenAI-compatible endpoint configured via
> `OPENAI_BASE_URL` + `OPENAI_API_KEY`? For each available path: does it exist, what secrets/config does it
> need, and would Matt's skills run under it unchanged?

## TL;DR

- gh-aw is **already engine-swappable by design**: "You can switch later by changing only `engine:` and the
  corresponding secret." ([engines.md](https://github.github.com/gh-aw/reference/engines/))
- **Two paths reach an arbitrary OpenAI-compatible endpoint cleanly today**: `engine: codex` (uses the literal
  `OPENAI_BASE_URL` + `OPENAI_API_KEY`) and `engine: copilot` **BYOK** (uses `COPILOT_PROVIDER_BASE_URL` +
  `COPILOT_PROVIDER_API_KEY` with `COPILOT_PROVIDER_TYPE: openai`).
- **Matt's skills are a first-class gh-aw feature** (`skills:` frontmatter) and are installed **per-engine** via
  `gh skill install --agent <name>` — every built-in engine (copilot, claude, codex, gemini, pi, antigravity)
  declares an agent name, so `skills:` is **not Copilot-only**. The **officially shipped** example
  (`mattpocock-skills-reviewer.md`) runs Matt's skills under `engine: copilot`.
- **Recommended least-effort path: `engine: copilot` BYOK with `COPILOT_PROVIDER_TYPE: openai`** — it is the
  proven-unchanged home for Matt's skills and reaches any OpenAI-compatible URL; the only friction is renaming
  your two secrets into the `COPILOT_PROVIDER_*` vars. If you must keep the *literal* `OPENAI_BASE_URL` /
  `OPENAI_API_KEY` names, `engine: codex` is the exact env match, at the cost of less-proven skill runtime.

## How engine + endpoint config works in gh-aw

Engines are selected by `engine:` (or `engine.id:`) in workflow frontmatter, each with a required secret
([`reference/engines.md`](https://github.github.com/gh-aw/reference/engines/)):

| Engine | `engine:` value | Required secret |
|---|---|---|
| GitHub Copilot CLI (default) | `copilot` | `copilot-requests: write` (recommended) or `COPILOT_GITHUB_TOKEN` |
| Claude Code | `claude` | `ANTHROPIC_API_KEY` or Anthropic WIF |
| OpenAI Codex | `codex` | `OPENAI_API_KEY` |
| Google Gemini CLI | `gemini` | `GEMINI_API_KEY` or Google WIF |
| OpenCode (experimental) | `opencode` | `COPILOT_GITHUB_TOKEN` (default) |
| Pi (experimental) | `pi` | `COPILOT_GITHUB_TOKEN` (default) |

Two independent endpoint-override mechanisms exist:

1. **`engine.api-target`** — hostname only (GHEC/GHES/custom): "The value must be a hostname only — no protocol
   or path." Works with any engine.
2. **Per-engine base-URL env var in `engine.env`** — this is the OpenAI-compatible route. Verbatim from
   [engines.md](https://github.github.com/gh-aw/reference/engines/) ("Custom API Endpoints via Environment Variables"):

   > Set a base URL environment variable in `engine.env` to route API calls to an internal LLM router, Azure
   > OpenAI deployment, or corporate proxy. AWF automatically extracts the hostname and applies it to the API
   > proxy. The target domain must also appear in `network.allowed`.

   | Engine | Environment variable |
   |---|---|
   | `codex` | `OPENAI_BASE_URL` |
   | `claude` | `ANTHROPIC_BASE_URL` |
   | `copilot` | `GITHUB_COPILOT_BASE_URL` |
   | `gemini` | `GEMINI_API_BASE_URL` |

## Per-path findings

### Path A — `engine: codex` → literal `OPENAI_BASE_URL` + `OPENAI_API_KEY`  ✅ exact env match

- **Exists?** Yes. This is the documented OpenAI-compatible override for Codex.
- **Config/secrets:** `OPENAI_API_KEY` (or `CODEX_API_KEY`, which "takes precedence when both are present" —
  [codex.md](https://github.github.com/gh-aw/engines/codex/)) plus `OPENAI_BASE_URL` in `engine.env`. Verbatim
  from [engines.md](https://github.github.com/gh-aw/reference/engines/):

  ```yaml
  engine:
    id: codex
    model: gpt-4o
    env:
      OPENAI_BASE_URL: "https://llm-router.internal.example.com/v1"
      OPENAI_API_KEY: ${{ secrets.LLM_ROUTER_KEY }}

  network:
    allowed:
      - github.com
      - llm-router.internal.example.com
  ```

  This is the **only** path that consumes the ticket's exact `OPENAI_BASE_URL` + `OPENAI_API_KEY` pair
  verbatim. Note the source uses `${{ secrets.LLM_ROUTER_KEY }}`, i.e. you pass your key through any secret name
  bound to `OPENAI_API_KEY` in `engine.env`.
- **Skills unchanged?** The Codex engine declares `ghSkillAgentName: "codex"`
  (`pkg/workflow/codex_engine.go:59`), so `skills:` frontmatter installs Matt's skills for the Codex agent via
  `gh skill install --agent codex`. **Caveat:** whether the Codex CLI actually *surfaces/auto-invokes* installed
  skills at runtime the way Copilot/Claude do is **not documented** and unverified here (see open questions).
- **Endpoint arbitrariness:** Full. gh-aw "extracts the hostname and applies it to the API proxy" — it forwards
  to *your* host; you must add it to `network.allowed`. The endpoint must speak the OpenAI wire API that the
  Codex CLI emits (Responses/Completions).

### Path B — `engine: copilot` BYOK → OpenAI-compatible via `COPILOT_PROVIDER_*`  ✅ recommended

- **Exists?** Yes, and BYOK is now default-on behavior (ADR-27902 "make-copilot-byok-behavior-default";
  feature-flag composition in ADR-26544; BYOK provider vars are allowlisted under strict mode in ADR-29411).
- **Config/secrets:** Set `COPILOT_PROVIDER_BASE_URL` in `engine.env` to activate BYOK. Verbatim from
  [engines.md](https://github.github.com/gh-aw/reference/engines/) ("Copilot Bring Your Own Key (BYOK) Mode"):

  > The Copilot engine supports routing requests to an external LLM provider instead of GitHub's default
  > routing. This is useful when you want to use a different model or provider (e.g., OpenAI, Anthropic, Azure
  > OpenAI, or a local Ollama/vLLM instance) while still using the Copilot CLI tooling.
  >
  > Set `COPILOT_PROVIDER_BASE_URL` in `engine.env` to activate BYOK mode.

  Verbatim example:

  ```yaml
  engine:
    id: copilot
    env:
      COPILOT_PROVIDER_BASE_URL: ${{ secrets.PROVIDER_BASE_URL }}   # REQUIRED — activates BYOK
      COPILOT_MODEL: claude-sonnet-4                                # REQUIRED for most providers
      COPILOT_PROVIDER_API_KEY: ${{ secrets.PROVIDER_API_KEY }}     # OPTIONAL for local providers
      COPILOT_PROVIDER_TYPE: anthropic                              # OPTIONAL — default: openai

  network:
    allowed:
      - defaults
      - your-provider-domain.example.com
  ```

  For an **OpenAI-compatible** endpoint, `COPILOT_PROVIDER_TYPE` defaults to `openai` (the table lists
  `openai` (default), `azure`, or `anthropic`), so you simply omit it. The `COPILOT_PROVIDER_BASE_URL` /
  `COPILOT_PROVIDER_API_KEY` / `COPILOT_PROVIDER_BEARER_TOKEN` vars are explicitly allowed to carry
  `${{ secrets.* }}` in `engine.env` under strict mode, and (per the doc's NOTE) the real credential is
  isolated in the AWF API-proxy sidecar, not exposed to the agent. `COPILOT_PROVIDER_WIRE_API: responses` is
  available for GPT-5 / o-series.
- **The mapping cost vs. the ticket:** the env-var names differ from `OPENAI_BASE_URL` / `OPENAI_API_KEY` — you
  set `COPILOT_PROVIDER_BASE_URL` / `COPILOT_PROVIDER_API_KEY` instead (a trivial rename; the underlying
  secrets can still be your `OPENAI_*` secrets).
- **Skills unchanged?** **Best-proven of all paths.** Copilot declares `ghSkillAgentName: "github-copilot"`
  (`pkg/workflow/copilot_engine.go:43`), and the **official** `.github/workflows/mattpocock-skills-reviewer.md`
  runs Matt's skills under `engine: id: copilot` with a `skills:` block listing
  `mattpocock/skills/tdd@<sha>`, `mattpocock/skills/diagnosing-bugs@<sha>`, `.../codebase-design`,
  `.../domain-modeling`, etc. — the exact skill set installed in this repo. It even routes to a Claude model
  (`model: claude-sonnet-4.6`) through Copilot, demonstrating provider/model decoupling. Copilot also has the
  **broadest gh-aw feature support** (custom agents, `max-continuations`, harness) per the feature table.
- **Endpoint arbitrariness:** Full, including local (Ollama/vLLM) and cloud. When `COPILOT_PROVIDER_BASE_URL`
  is a literal URL, gh-aw auto-adds the host to the allowlist; when it comes from a secret/expression you must
  add the host to `network.allowed` yourself.

### Path C — `engine: opencode` (and the universal LLM-consumer engines)  ⚠️ exists, but NOT arbitrary-endpoint

- **Exists?** Yes, as an **experimental** built-in engine (ADR-25830). OpenCode is described as
  "provider-agnostic, open-source ... (BYOK) ... supports 75+ models via a unified CLI interface using a
  `provider/model` format." In the current source it is a *universal LLM consumer engine*
  (`pkg/workflow/universal_llm_consumer_engine.go`; ADR-27708), sharing routing logic with Crush.
- **Config/secrets:** `engine.model` is **required** and **must** be `provider/model` (ADR-27708 §Model Field):
  the provider prefix "**MUST** be one of the supported values: `copilot`, `anthropic`, `openai`, or `codex`."
  Default secret is `COPILOT_GITHUB_TOKEN`; native BYOK uses the provider's own secret (e.g. `ANTHROPIC_API_KEY`
  / `OPENAI_API_KEY`).
- **The blocker for "arbitrary endpoint":** gh-aw **controls the base URL itself** — it points the engine at
  gh-aw's *internal* gateway proxy, not a user URL. From ADR-27708 §Backend Profile Resolution: when the backend
  is `codex`/`openai` it "**MUST** set `OPENAI_BASE_URL` when the firewall is enabled"; when `anthropic` it sets
  `ANTHROPIC_BASE_URL`. The engine source confirms it *owns* these vars
  (`universal_llm_consumer_engine.go`: `baseURLEnvName: "OPENAI_BASE_URL"` /
  `extraURLEnvName: "OPENAI_BASE_URL"`). The real upstream host is resolved from the `provider/` prefix against
  a fixed `openCodeProviderDomains` map (ADR-25830), i.e. only the known provider APIs — **there is no
  documented knob to send it to an arbitrary OpenAI-compatible host** the way Codex/Copilot-BYOK allow. So
  although OpenCode *natively* supports OpenAI-compatible providers as a standalone CLI, **gh-aw's wrapper
  constrains it to the enumerated providers.**
- **Skills unchanged?** OpenCode also lacks a tools allowlist and `engine.bare`, and (per its ADR) does not
  support `--max-turns`. Its `gh skill` agent mapping is not clearly declared (no `opencode_engine.go`; logic
  lives in the universal engine). Treat skill support as **unverified/weaker** here.
- **Note:** `engine.command: opencode run` was always usable as a raw custom-command override (ADR-25830
  Alternative 1) — that would let you fully control OpenCode's own `opencode.jsonc` provider config for a
  truly arbitrary endpoint, but you'd manage install, permissions, and firewall yourself.

### Path D — `engine: claude` via `ANTHROPIC_BASE_URL` pointed at an OpenAI-compatible proxy/router  ⚠️ possible but unsupported for non-Claude models

- **Exists?** The gh-aw side exists: set `ANTHROPIC_BASE_URL` in `engine.env` (engines.md base-URL table). gh-aw
  extracts the host and routes through its proxy; add the host to `network.allowed`.
- **The catch (official Anthropic docs):** Claude Code speaks the **Anthropic Messages API** to the gateway, so
  an OpenAI-compatible endpoint must be fronted by a translating router (claude-code-router, or LiteLLM in
  Anthropic-passthrough mode) that exposes an **Anthropic-format** endpoint. Verbatim from
  [code.claude.com/docs/en/llm-gateway](https://code.claude.com/docs/en/llm-gateway):

  > Any gateway that exposes a supported API format works. **Anthropic doesn't endorse, maintain, or audit
  > third-party gateway products, and doesn't support routing Claude Code to non-Claude models through any
  > gateway.**

  and

  > `ANTHROPIC_BASE_URL` is the variable that points Claude Code at the gateway.

  So routing Claude Code to a *non-Claude* model behind an OpenAI-compatible proxy is explicitly **unsupported**
  by Anthropic (best-effort via third-party routers only). This is the most fragile path for "arbitrary
  OpenAI-compatible endpoint."
- **Config/secrets:** `ANTHROPIC_API_KEY` (real, or a gateway credential; note gh-aw ignores
  `CLAUDE_CODE_OAUTH_TOKEN`) plus `ANTHROPIC_BASE_URL` in `engine.env`, plus `network.allowed` for the proxy host.
- **Skills unchanged?** Strong on the *skill* axis — Claude declares `ghSkillAgentName: "claude-code"`
  (`pkg/workflow/claude_engine.go:31`), and Matt's skills use the Claude/Agent-Skills `SKILL.md` format
  natively. The weakness is purely the *model routing*, not the skills.

## Do Matt's skills run unchanged? (cross-cutting)

- Matt's skills in this repo (`.agents/skills/` and `.claude/skills/`, identical trees: `research`, `tdd`,
  `code-review`, `diagnosing-bugs`, `domain-modeling`, `codebase-design`, `grilling`, `wayfinder`, etc.) are
  plain `SKILL.md` files (YAML `name` + `description` + Markdown body) — **model- and engine-agnostic content**.
- gh-aw treats them as a **first-class feature**: `skills:` frontmatter "Installs ... skills in the activation
  job before the agent runs" ([frontmatter.md](https://github.github.com/gh-aw/reference/frontmatter/)),
  accepting `owner/repo/skill/path@<40-char-sha>` (e.g. `mattpocock/skills/tdd@<sha>`) or local paths
  (`skills/name`, installed `--from-local`).
- Install is **engine-parameterized**, not Copilot-locked: the activation step emits
  `GH_AW_GH_SKILL_AGENT_NAME` from `engine.GetGHSkillAgentName()`
  (`pkg/workflow/compiler_activation_steps.go:190`), and every engine sets one —
  `github-copilot`, `claude-code`, `codex`, `gemini-cli`, `pi`, `antigravity`
  (`pkg/workflow/*_engine.go`). So `skills:` works under copilot, claude, codex, and gemini alike.
- **Strongest guarantee** is Copilot: the officially shipped `mattpocock-skills-reviewer.md` *is* Matt's skills
  under `engine: copilot`. Under codex/gemini the install path exists but end-to-end runtime skill invocation is
  not documented — flagged below.

## Recommendation — least-effort path for "engine-agnostic + OpenAI-compatible + skills unchanged"

**Use `engine: copilot` with BYOK (`COPILOT_PROVIDER_TYPE: openai`).** Rationale:

1. It reaches **any** OpenAI-compatible endpoint (`COPILOT_PROVIDER_BASE_URL` + `COPILOT_PROVIDER_API_KEY`,
   type `openai` by default), including local vLLM/Ollama.
2. It keeps the **default engine with the broadest gh-aw feature support** and, crucially, is the **proven,
   officially shipped home for Matt Pocock's skills** — zero changes to the skills themselves.
3. Only friction: wrap your existing `OPENAI_BASE_URL` / `OPENAI_API_KEY` secret **values** into the
   `COPILOT_PROVIDER_BASE_URL` / `COPILOT_PROVIDER_API_KEY` **names**, and list the provider host in
   `network.allowed` (auto-added when the base URL is a literal).

**If the hard requirement is the literal `OPENAI_BASE_URL` + `OPENAI_API_KEY` env names**, use `engine: codex`
— it consumes exactly those two vars — accepting the caveat that skill *runtime* behavior under the Codex CLI is
less proven than under Copilot.

Avoid Path C (OpenCode) for *arbitrary* endpoints — gh-aw pins its base URL to its own proxy and restricts the
provider prefix to `copilot|anthropic|openai|codex`. Avoid Path D (Claude via proxy) unless you accept
Anthropic's explicit "not supported for non-Claude models through a gateway" position and run a translating
router.

## Confidence & open questions

**High confidence** (grounded in gh-aw docs source + Go source + ADRs, and official Anthropic docs):
- Codex accepts `OPENAI_BASE_URL` + `OPENAI_API_KEY` in `engine.env` for a custom/OpenAI-compatible endpoint.
- Copilot BYOK reaches OpenAI-compatible endpoints via `COPILOT_PROVIDER_BASE_URL` (+`_API_KEY`,
  `_TYPE: openai`), and is the officially shipped host for Matt's skills.
- `skills:` frontmatter is engine-parameterized via per-engine `gh skill --agent` names (not Copilot-only).
- Claude Code speaks the Anthropic API to a gateway; routing it to non-Claude models via a proxy is
  unsupported by Anthropic.
- OpenCode/universal engine forces its own base URL (internal proxy) and restricts providers to a fixed set.

**Unverified / open:**
1. **Codex/Gemini skill runtime.** The *install* path exists (`--agent codex`/`gemini-cli`), but no primary
   source confirms the Codex or Gemini CLI *discovers and auto-invokes* installed skills at run time the way
   Copilot/Claude do. Needs an actual workflow run to confirm.
2. **`gh skill` CLI version/availability** on the runner (frontmatter docs note "Requires a recent version of
   the `gh` CLI"); and whether `mattpocock/skills` are consumed as remote pinned specs vs. this repo's local
   `.agents/skills` / `.claude/skills` copies under gh-aw's `skills: skills/name --from-local` form.
3. **BYOK wire-API nuances** for specific OpenAI-compatible servers (`completions` vs `responses`,
   `COPILOT_PROVIDER_MODEL_ID` mapping) — verified only for OpenAI/Azure Foundry in the docs; a generic
   third-party server may need `COPILOT_PROVIDER_WIRE_API` tuning.
4. **Exact gh-aw version** these facts are pinned to: read from `githubnext/gh-aw@main` on 2026-07-31. The
   universal-consumer ADR-27708 is still marked *Draft*; confirm the shipped OpenCode behavior against the
   installed gh-aw release before relying on Path C specifics.
5. WebFetch summaries were cross-checked against the cloned repo source for every load-bearing config block;
   the `reference/auth/` page 404'd on the public site, so auth details were taken from `engines.md`,
   `engines/codex.md`, and the Go source instead.

## Sources

- gh-aw engines reference — https://github.github.com/gh-aw/reference/engines/ (source:
  `docs/src/content/docs/reference/engines.md`)
- gh-aw Codex engine — https://github.github.com/gh-aw/engines/codex/ (source: `.../engines/codex.md`)
- gh-aw Copilot engine — https://github.github.com/gh-aw/engines/copilot/ (source: `.../engines/copilot.md`)
- gh-aw frontmatter `skills:` — https://github.github.com/gh-aw/reference/frontmatter/ (source:
  `.../reference/frontmatter.md`, glossary.md)
- gh-aw source: `pkg/workflow/{copilot,claude,codex,gemini,pi,antigravity}_engine.go`,
  `universal_llm_consumer_engine.go`, `compiler_activation_steps.go`, `skills_frontmatter.go`,
  `agentic_engine.go`
- gh-aw ADRs: `docs/adr/25830-opencode-engine-integration.md`,
  `27708-universal-llm-consumer-engine-for-multi-provider-routing.md`,
  `26544-byok-copilot-feature-flag-composition.md`, `27902-make-copilot-byok-behavior-default.md`,
  `29411-copilot-byok-provider-vars-strict-mode-allowlist.md`
- gh-aw official example: `.github/workflows/mattpocock-skills-reviewer.md` (githubnext/gh-aw)
- Matt Pocock skills upstream — https://github.com/mattpocock/skills
- Claude Code LLM gateway (Anthropic official) — https://code.claude.com/docs/en/llm-gateway
- Repo skills inspected locally: `.agents/skills/`, `.claude/skills/`
