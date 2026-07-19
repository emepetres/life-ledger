# Stack Selection Research

Research for [issue #2](https://github.com/emepetres/life-ledger/issues/2). Companion to the
[SplittyPie reference](./splittypie-reference.md). Persistence design is owned by a separate
ticket (#5); this doc only recommends a storage **default**.

## Question

Recommend a tech stack to build a minimal personal expense-tracking web app that mimics
SplittyPie's simple, single-deployable UX, but with a backend in **Python, Rust, or Go**
(explicitly **not** Node/TypeScript — the user has less Node full-stack experience).

The signature UX to reproduce (from the SplittyPie reference, §7): a free-text **"quick-add"**
entry box with **live preview**, plus a **list view with edit/delete**.

Every option is judged against these hard criteria:

1. **Single deployable unit** — server + web UI shipped together (server-rendered templates, or
   server + light HTML-over-the-wire like htmx), not a separate API + heavy SPA, unless a split is
   clearly justified.
2. **Simple "easy server"** — minimal ceremony/boilerplate; the user admired how little setup
   SplittyPie's server needs.
3. **Azure-deployable** cheaply for a personal app — a concrete path must exist, with rough
   cost/free-tier noted. (Not deploying now, only confirming feasibility.)
4. **Locally runnable for QA** — trivial local-run story.
5. Supports free-text quick-add + live preview + list with edit/delete, with an explicit
   recommendation on the UI approach.

### The UI approach question, decided once up front

The quick-add + live-preview + editable-list UX does **not** require an SPA. htmx delivers exactly
this pattern — "access modern browser features directly from HTML, rather than using JavaScript"
— by letting any element issue an AJAX request on any event and swapping the returned HTML fragment
into the page. Live preview is a documented first-class use: `hx-trigger="keyup changed delay:500ms"`
posts the input as the user types and swaps a server-rendered preview; edit/delete return updated
fragments or a `204 No Content`. htmx ships as a **single `<script>` tag** (CDN or vendored file),
no build system ([htmx docs](https://htmx.org/docs/)).

**Therefore the recommended UI approach across all options is: server-side templates + htmx**
(HTML-over-the-wire). This keeps the app a single deployable unit and keeps the "easy server"
property, while still giving the responsive quick-add/preview feel of SplittyPie's Ember client
without a client-side framework. It also directly satisfies criterion 1. Options below differ mainly
in the *backend language + templating* layer under that same UI approach.

Storage default for all options: **SQLite** (a single embedded file — no separate DB server to
provision, deploy, or pay for), which best fits the single-deployable + cheap-Azure constraints.
Final schema is #5's call.

## Options compared

### Option A — Python + FastAPI + Jinja2 + htmx

- **Language / framework:** Python, [FastAPI](https://fastapi.tiangolo.com/).
- **Templating / UI:** Server-side [Jinja2](https://fastapi.tiangolo.com/advanced/templates/) templates
  returning `HTMLResponse`, progressively enhanced with htmx for quick-add live preview and
  list edit/delete.
- **Setup / ceremony:** Very low. FastAPI's template support is `pip install jinja2`, then
  `templates = Jinja2Templates(directory="templates")` and
  `app.mount("/static", StaticFiles(directory="static"))`; a handler returns
  `templates.TemplateResponse(request=request, name="item.html", context={...})`
  ([FastAPI templates docs](https://fastapi.tiangolo.com/advanced/templates/)). Handlers are plain
  `async def` functions with type-hinted params. Returning an HTML fragment for an htmx swap is just
  another `TemplateResponse` pointing at a partial template — a natural fit.
- **Storage default:** SQLite via Python's stdlib `sqlite3` (or SQLModel/SQLAlchemy if an ORM is
  wanted later — #5 decides).
- **Azure deploy path:** Azure App Service supports Python natively ("App Service supports a variety
  of web stacks: .NET, Java …, Node.js, **Python**, and PHP … on both Windows and Linux") and lists
  Django among its Marketplace templates
  ([App Service overview](https://learn.microsoft.com/en-us/azure/app-service/overview)). Alternatively,
  containerize and run on Azure Container Apps (below). Either path is concrete.
- **Local run:** `uvicorn main:app --reload` (or `fastapi dev`). One process, one command.

### Option B — Python + Django (+ htmx)

- **Language / framework:** Python, [Django](https://docs.djangoproject.com/en/5.2/intro/overview/).
- **Templating / UI:** Django's built-in template system + htmx partials; same HTML-over-the-wire
  approach.
- **Setup / ceremony:** Higher. Django is explicitly "batteries-included" — ORM, admin, forms,
  URL routing, template system all bundled
  ([Django overview](https://docs.djangoproject.com/en/5.2/intro/overview/)). For a minimal
  single-purpose expense tracker that is more scaffolding (settings module, apps, migrations,
  `manage.py`) than the task needs. The upside is the free auto-generated **admin** and mature
  forms/validation if the app grows.
- **Storage default:** SQLite is Django's default database backend, so this option gets the
  single-file DB for free.
- **Azure deploy path:** Same as Option A — App Service Python runtime (Django is even a named
  App Service Marketplace template
  ([App Service overview](https://learn.microsoft.com/en-us/azure/app-service/overview))) or a
  container on Container Apps.
- **Local run:** `python manage.py runserver`. One command, but preceded by project/app scaffolding
  and `migrate`.

### Option C — Go + html/template + htmx (optionally Templ)

- **Language / framework:** Go, standard-library `net/http` (plus a light router if desired).
- **Templating / UI:** [`html/template`](https://pkg.go.dev/html/template) — part of the **standard
  library**, with **context-aware auto-escaping** (HTML/CSS/JS/URL) that prevents injection without
  manual escaping. Rendered via `template.ParseFiles`/`ParseFS` + `t.Execute`/`t.ExecuteTemplate`,
  which makes returning an htmx partial trivial (execute a named sub-template). Optionally
  [Templ](https://templ.guide/) for compile-time **type-safe** components that render HTML fragments —
  a natural fit for htmx — at the cost of a **code-generation build step**.
- **Setup / ceremony:** Low-to-moderate. No framework to install for the core (templating + server
  are stdlib), but you assemble more yourself (routing, form parsing) than FastAPI/Django hand you.
  Ships as a **single static binary** — the cleanest possible "single deployable unit."
- **Storage default:** SQLite via a Go driver (e.g. `modernc.org/sqlite`, pure-Go, no cgo — keeps
  the static-binary property). #5 decides specifics.
- **Azure deploy path:** Go is not a first-class App Service runtime, so the concrete path is a
  **container**: build a small image (static binary → `FROM scratch`/distroless) and run on
  **Azure Container Apps**, which runs "containers from any registry, public or private"
  ([Container Apps overview](https://learn.microsoft.com/en-us/azure/container-apps/overview)).
- **Local run:** `go run .` — one command, no external toolchain beyond Go. (Templ adds a
  `templ generate` step before build.)

### Option D — Rust + Axum + Askama + htmx

- **Language / framework:** Rust, [Axum](https://docs.rs/axum/latest/axum/) (ergonomic router on
  Tokio/Hyper/Tower).
- **Templating / UI:** [Askama](https://docs.rs/askama/latest/askama/) — compile-time, **type-safe**
  Jinja-like templates via a `#[derive(Template)]` macro, auto-escaping by default. Handlers return
  anything implementing `IntoResponse`, including rendered HTML, so htmx partials are just a struct
  that renders to a fragment.
- **Setup / ceremony:** Highest of the four. Axum itself is minimal, but Rust adds async setup
  (Tokio), the borrow checker, and longer compile times; templates are checked at compile time
  (a correctness win, but more upfront friction). Best when performance/correctness matters more
  than iteration speed.
- **Storage default:** SQLite via `sqlx` or `rusqlite`. #5 decides.
- **Azure deploy path:** Like Go, not a first-class App Service runtime → **container on Azure
  Container Apps** ([Container Apps overview](https://learn.microsoft.com/en-us/azure/container-apps/overview)).
  Produces a single small binary/image.
- **Local run:** `cargo run` — one command, but the first build is slow.

### Azure cost / free-tier feasibility (shared)

Both concrete deploy paths are cheap enough for a personal app:

- **Azure App Service** (best for Python Options A/B) has a documented **free tier (F1)** plus the
  Azure-for-Students starter offer
  ([App Service overview](https://learn.microsoft.com/en-us/azure/app-service/overview)). F1 is
  limited (shared compute, no custom-domain TLS, no always-on) but sufficient for personal QA/demo.
- **Azure Container Apps** (needed for Go/Rust Options C/D, also usable for A/B) is serverless with a
  **monthly free grant per subscription**: the first **180,000 vCPU-seconds**, **360,000
  GiB-seconds**, and **2 million HTTP requests** are free each calendar month, and revisions can
  **scale to zero** (no charge when idle)
  ([Container Apps billing](https://learn.microsoft.com/en-us/azure/container-apps/billing)). For a
  low-traffic personal app that scales to zero between uses, this typically means **~$0/month**.

So criterion 3 is satisfied for every option; the difference is that Python can use the simpler
App Service push-deploy, while Go/Rust go through a container (still cheap, slightly more setup).

## Recommendation

**Python + FastAPI + Jinja2 + htmx, with SQLite as the storage default (Option A).**

Rationale, tied to the criteria:

1. **Single deployable unit** — FastAPI serves the Jinja2-rendered pages and htmx partials from one
   process; htmx is a single vendored script. No separate API/SPA split.
2. **Easy server** — this is the closest match to the "how little setup SplittyPie's server needs"
   admiration: template support is one `pip install` + two lines, handlers are plain typed functions,
   and an htmx fragment is just another `TemplateResponse`
   ([FastAPI templates](https://fastapi.tiangolo.com/advanced/templates/)). Less ceremony than Django,
   far less than Rust.
3. **Azure-deployable cheaply** — Python is a **first-class App Service runtime** with a free F1 tier,
   giving the simplest concrete deploy path of any option
   ([App Service overview](https://learn.microsoft.com/en-us/azure/app-service/overview)); Container
   Apps' scale-to-zero free grant is available as a fallback.
4. **Locally runnable** — `uvicorn main:app --reload` (or `fastapi dev`), one command.
5. **Quick-add + live preview + list edit/delete** — served directly by the htmx pattern above
   (`hx-trigger` for live preview, fragment swaps for edit/delete) with server-side Jinja2 partials.

Python also lowers total risk given the stated constraint (the user has less Node experience and
wants Python/Rust/Go): Python is the most broadly approachable of the three, and FastAPI keeps the
"easy server" feel without Django's scaffolding overhead.

## Trade-offs / risks

- **Runner-up: Go + html/template + htmx (Option C).** The main thing given up by *not* choosing Go
  is **operational simplicity of the artifact**: Go compiles to a **single static binary** and its
  templating is **standard library** (no dependency, context-aware auto-escaping), which is arguably
  an even purer "single deployable unit" than a Python app + interpreter + venv. The reasons it loses
  to FastAPI here: Go is **not a first-class App Service runtime**, so its only concrete Azure path is
  a container on Container Apps (slightly more setup than App Service push-deploy), and you assemble
  more of the server yourself (routing, form parsing) than FastAPI hands you. If deployment artifact
  cleanliness or raw performance later outweigh iteration speed, Go is the strong second choice.
- **Django (B)** is heavier than this minimal app warrants; its batteries (admin, ORM, forms) are the
  reason to pick it *only if* the app is expected to grow substantially.
- **Rust + Axum + Askama (D)** offers the strongest compile-time correctness (type-safe templates,
  single binary) but has the highest ceremony and slowest iteration — poor fit for a "minimal
  personal app" where fast iteration matters more than maximal safety/perf.
- **htmx maturity risk:** htmx is a small, stable single-file library, but it *is* a client-side
  dependency; vendor a pinned version rather than relying on a CDN for the single-deployable
  guarantee.
- **SQLite on Azure caveat (flagged for #5):** SQLite is a single file, so on App Service/Container
  Apps its durability depends on **persistent storage** (App Service's mounted file share, or an
  Azure Files mount for Container Apps) and it is **single-writer** — fine for a personal app, but
  #5 should confirm the persistence mount and decide if/when to graduate to a managed DB.

## Sources

- SplittyPie reference (this repo): [docs/research/splittypie-reference.md](./splittypie-reference.md)
- FastAPI — Templates: https://fastapi.tiangolo.com/advanced/templates/
- htmx — Documentation: https://htmx.org/docs/
- Django — Overview: https://docs.djangoproject.com/en/5.2/intro/overview/
- Go — `html/template` package: https://pkg.go.dev/html/template
- Templ (Go): https://templ.guide/
- Axum (Rust): https://docs.rs/axum/latest/axum/
- Askama (Rust): https://docs.rs/askama/latest/askama/
- Azure App Service — Overview (runtimes, free tier): https://learn.microsoft.com/en-us/azure/app-service/overview
- Azure Container Apps — Overview (any-registry containers, scale-to-zero): https://learn.microsoft.com/en-us/azure/container-apps/overview
- Azure Container Apps — Billing (monthly free grant): https://learn.microsoft.com/en-us/azure/container-apps/billing
