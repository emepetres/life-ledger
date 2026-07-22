# syntax=docker/dockerfile:1

# Life Ledger ships as a single fully-static Go binary (ADR-0003, ADR-0005):
# a multi-stage build compiles it with CGO disabled — pure-Go SQLite, no libc —
# and drops it into a distroless static image that runs as a non-root user.
# Templates, static assets, and migrations are all go:embed'd, so the runtime
# image needs nothing but the binary.

# --- build stage -------------------------------------------------------------
FROM golang:1.26 AS build
WORKDIR /src

# Download modules first so this layer is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off → fully static, cross-compiled for the Linux container.
# -trimpath strips local paths; -s -w drop the symbol/DWARF tables to shrink it.
# tzdata is embedded via `import _ "time/tzdata"` (ADR-0005), so no zoneinfo is
# copied into the runtime image.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/life-ledger ./cmd/life-ledger

# --- runtime stage -----------------------------------------------------------
# distroless/static: no shell, no package manager, minimal attack surface. The
# :nonroot tag runs as an unprivileged user and bundles CA certificates.
FROM gcr.io/distroless/static-debian12:nonroot

# Sensible defaults; production overrides via Container Apps (Bicep, ADR-0005).
#   LIFELEDGER_DB_PATH — the DB lives on the mounted Azure Files volume directory.
#   TZ                 — selects the embedded zoneinfo so "today" is the local day.
ENV LIFELEDGER_DB_PATH=/data/expenses.db \
    TZ=Europe/Madrid

COPY --from=build /out/life-ledger /life-ledger

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/life-ledger"]
