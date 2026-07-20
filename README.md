# Life Ledger

Personal expense register — a single free-text line becomes a stored **Expense**
on the server, listed, edited, and deleted. See [CONTEXT.md](CONTEXT.md) for the
domain language and [issue #9](https://github.com/emepetres/life-ledger/issues/9)
for the full feature spec.

This is the **walking skeleton** ([issue #11](https://github.com/emepetres/life-ledger/issues/11)):
a server that boots, renders a home page, serves a vendored htmx asset, and
reports healthy — no database, parser, or auth yet.

## Run locally

Requires [Go](https://go.dev/dl/) 1.26+. No external services.

```sh
make run          # or: go run ./cmd/life-ledger
```

Then open <http://localhost:8080>. The health probe is at `/health`.

### Configuration

| Env var           | Default   | Meaning                                  |
| ----------------- | --------- | ---------------------------------------- |
| `LIFELEDGER_ADDR` | `:8080`   | Listen address (`host:port` or `:port`). |

```sh
LIFELEDGER_ADDR=127.0.0.1:9000 go run ./cmd/life-ledger
```

## Build

Produces a single fully-static binary (cgo disabled), ready for a `FROM scratch`
container image:

```sh
make build        # -> bin/life-ledger

# Linux container target from any dev OS:
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/life-ledger ./cmd/life-ledger
```

Templates and static assets (including the vendored, version-pinned htmx script)
are embedded via `go:embed`, so the binary is self-contained — no CDN, no
sidecar files.

## Test

```sh
make test         # or: go test ./...
```

Correctness lives at two seams (see the spec): pure-function parser unit tests
(future slice) and black-box HTTP-boundary tests via `net/http/httptest`.

## Layout

```
cmd/life-ledger/     entrypoint (reads config, starts the server)
internal/server/     HTTP handler: home page, static assets, health check
web/                 go:embed'd templates and static assets
  templates/         html/template sources
  static/vendor/     vendored, version-pinned third-party assets (htmx)
```
