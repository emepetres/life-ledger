# Life Ledger

Personal expense register — a single free-text line becomes a stored **Expense**
on the server, listed, edited, and deleted. See [CONTEXT.md](CONTEXT.md) for the
domain language and [issue #9](https://github.com/emepetres/life-ledger/issues/9)
for the full feature spec.

## Run locally

Requires [Go](https://go.dev/dl/) 1.26+. No external services.

The ledger is guarded by a single shared password (ADR-0004), enforced in-app and
identical locally and in production. So that the app boots, first generate a
bcrypt hash of your chosen password and set it in the environment — the plaintext
is never stored:

```pwsh
$Env:LIFELEDGER_PASSWORD_HASH = "$(make -s hash-password)"   # prompts for the password
make run                                                       # or: go run ./cmd/life-ledger
```

Then open <http://localhost:8080>, log in, and start entering expenses. The
health probe at `/health`, the login page, and static assets are reachable
without a session; everything else requires one.

### Configuration

| Env var                    | Default       | Meaning                                                                                                                                                                                                               |
| -------------------------- | ------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `LIFELEDGER_ADDR`          | `:8080`       | Listen address (`host:port` or `:port`).                                                                                                                                                                              |
| `LIFELEDGER_DB_PATH`       | `./data/expenses.db` | SQLite database file; the parent directory is created on first run.                                                                                                                                            |
| `LIFELEDGER_BACKUP_BLOB_URL` | _(unset)_   | Azure Storage **container** URL for backup-on-write / restore-on-boot (authenticated via managed identity). Unset means no backup — a pure local file, no Azure dependency; set it only in production on ephemeral storage. |
| `LIFELEDGER_PASSWORD_HASH` | _(required)_  | bcrypt hash of the shared password (generate with `make hash-password`). The app won't boot without it.                                                                                                               |
| `LIFELEDGER_SECURE_COOKIE` | `false`       | Sets the session cookie's `Secure` flag. Leave off for local `http://localhost`; set `true` in production (HTTPS).                                                                                                    |
| `LIFELEDGER_SESSION_KEY`   | _(ephemeral)_ | Session-cookie signing secret. **Required** when `LIFELEDGER_SECURE_COOKIE=true` (production). Left unset for local QA, a random key is generated per boot (sessions drop on restart). Rotating it logs everyone out. |

```pwsh
$Env:LIFELEDGER_ADDR = "127.0.0.1:9000"; go run ./cmd/life-ledger
```

## Build

Produces a single fully-static binary (cgo disabled), ready for a `FROM scratch`
container image:

```pwsh
make build        # -> bin/life-ledger

# Linux container target from any dev OS:
$Env:CGO_ENABLED = 0; $Env:GOOS = "linux"; $Env:GOARCH = "amd64"
go build -o bin/life-ledger ./cmd/life-ledger
```

Templates and static assets (including the vendored, version-pinned htmx script)
are embedded via `go:embed`, so the binary is self-contained — no CDN, no
sidecar files.

The container image is built the same way (`docker build .`, or `make docker-build`):
a multi-stage Dockerfile compiles the static binary and drops it into a
distroless image (ADR-0005).

## Deploy

Life Ledger runs on Azure Container Apps, provisioned by Bicep in `infra/` and
shipped by GitHub Actions (ADR-0005):

- Push to `main` runs the gated CI/CD workflow — tests, then deploy.
- Infrastructure is applied by a separate workflow on `infra/**` changes or manual
  dispatch.

First-time setup (OIDC trust, GitHub Secrets, bootstrap) is a single script,
[`scripts/first-deploy.ps1`](scripts/first-deploy.ps1), driven by a `.env` file
(copy [`.env.example`](.env.example)):

```pwsh
Copy-Item .env.example .env   # then fill it in
pwsh scripts/first-deploy.ps1
```

The `.env` it reads (see [`.env.example`](.env.example)):

| Value                      | Meaning                                                                          |
| -------------------------- | -------------------------------------------------------------------------------- |
| `SUBSCRIPTION_ID`          | Azure subscription for the resource group and all resources.                     |
| `RESOURCE_GROUP`           | Resource group to create/reuse; the deploy identity is scoped here.              |
| `LOCATION`                 | Azure region (e.g. `westeurope`).                                                 |
| `REPO`                     | GitHub repo as `owner/repo`.                                                      |
| `APP_REG_NAME`             | Entra app-registration display name; looked up by name and reused on re-run.     |
| `LIFELEDGER_PASSWORD_HASH` | bcrypt login-password hash. Empty ⇒ the script hashes a password you type.       |
| `LIFELEDGER_SESSION_KEY`   | Session-cookie signing secret. Empty ⇒ the script mints a random key.            |

The script is idempotent — safe to re-run. Full walkthrough (and how to repair
any step by hand) in
[docs/deployment/first-deploy.md](docs/deployment/first-deploy.md). The overall
system — modules, request flow, and deployment topology — is in
[docs/architecture.md](docs/architecture.md).

## Test

```pwsh
make test         # or: go test ./...
```

Correctness lives at two seams (see the spec): pure-function parser unit tests
(future slice) and black-box HTTP-boundary tests via `net/http/httptest`.

## Layout

```
cmd/life-ledger/     entrypoint (reads config, wires auth, starts the server)
cmd/hashpw/          helper: bcrypt-hash a password for LIFELEDGER_PASSWORD_HASH
internal/server/     HTTP handlers: home page, add/edit/delete, login/logout, health
internal/auth/       session cookie, rate limiter, and the protective middleware
internal/store/      SQLite persistence (self-creating, embedded migrations)
internal/expense/    free-text entry parser and the Expense record
web/                 go:embed'd templates and static assets
  templates/         html/template sources
  static/vendor/     vendored, version-pinned third-party assets (htmx)
```
