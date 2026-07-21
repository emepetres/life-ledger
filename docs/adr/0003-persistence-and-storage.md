# Persistence & storage design

Expenses are persisted in **SQLite**, accessed from Go through the pure-Go **`modernc.org/sqlite`** driver, with the schema created and evolved by **versioned migrations embedded in the binary**. The database is a single file on disk; in future Azure deployment that file lives on a persistent volume.

## Storage mechanism

SQLite, one file. It honours every standing preference for this effort — single deployable unit, cheap, splittypie-simple, trivially runnable locally — and the app's load profile (one user, a handful of writes a day) never touches SQLite's concurrency limits.

The one real tension is Azure: **Container Apps has an ephemeral filesystem**, so a DB file on the container's local disk would be wiped on every restart, scale event, or revision deploy. The design intent is therefore SQLite **on a mounted persistent volume** (Azure Files / SMB share) at the DB directory. SQLite over an SMB share has documented file-locking quirks, but they only bite under concurrent writers from multiple hosts — not this single-container, single-user app. Actual deployment is out of scope for this map; the requirement is recorded for the build.

## Table shape

```sql
CREATE TABLE expense (
  id          INTEGER PRIMARY KEY,              -- rowid alias, autoincrements
  date        TEXT    NOT NULL,                 -- expense day, ISO-8601 'YYYY-MM-DD'
  amount      INTEGER NOT NULL,                 -- full paid, EUR minor units (cents)
  description TEXT    NOT NULL,
  split       INTEGER NOT NULL DEFAULT 0 CHECK (split IN (0,1)),
  account     TEXT,                             -- nullable; blank/omitted = NULL
  raw_text    TEXT    NOT NULL,
  created_at  TEXT    NOT NULL,                 -- ISO-8601 timestamp, UTC
  updated_at  TEXT    NOT NULL
);

CREATE INDEX idx_expense_date ON expense (date DESC);
```

Columns come straight from the stored-Expense record shape ([ADR 0001](0001-stored-expense-record-shape.md)); the choices below are the SQLite-specific *representation*, since SQLite has no native boolean or date type:

- **`id INTEGER PRIMARY KEY`** (not `AUTOINCREMENT`) — the plain rowid alias already gives monotonic ids; `AUTOINCREMENT` only guards against id reuse after deletion, at a cost not worth paying here.
- **`date` as ISO-8601 TEXT `'YYYY-MM-DD'`** — human-readable, lexically sortable (`ORDER BY date` just works), no timezone ambiguity for a calendar day. Integer unix would buy nothing at day granularity and hurt debuggability.
- **`split` as `INTEGER … CHECK (split IN (0,1))`** — SQLite has no bool; 0/1 with a check constraint is the idiom, and `database/sql` maps it cleanly.
- **`account TEXT` nullable** — NULL when omitted (not `NOT NULL DEFAULT ''`), keeping the stored record a faithful transcript; "personal" stays a display-time default.
- **`created_at` / `updated_at` as ISO-8601 UTC TEXT** — consistent with `date`, readable in a DB browser; the application sets both, refreshing `updated_at` on edit.
- **One index on `date DESC`** — the list view's natural sort. Effectively free at this volume and documents intent.

## Init & migration story

Schema is managed by **versioned migrations embedded in the binary** (`pressly/goose` with `go:embed`'d SQL files), applied automatically on startup. This costs almost nothing today for a single table, and makes the migrations ADR 0001 anticipates — promoting `account` to a controlled set, backfilling a `currency` column, refining `split` — clean, versioned operations rather than hand-rolled "has this column been added yet?" patches. The single-deployable-unit goal is preserved because migrations live *inside* the binary, nothing ships separately.

**Unified startup path (identical locally and on Azure):** on boot the app opens the DB file at a configured path, **creating it if absent**, applies any pending migrations, then serves. First run locally creates `./data/expenses.db`; first run on Azure does the identical thing against the volume-mounted path. No separate provisioning or seed step.

Two pragmas are set at connection open, regardless: `journal_mode=WAL` (better concurrency and resilience — this is what makes the volume-mounted single-writer story robust) and `foreign_keys=ON` (harmless now, the correct default for when a second table arrives). A third, `busy_timeout=5000`, accompanies them: it tells a connection to wait briefly for a momentary write lock rather than failing immediately with `SQLITE_BUSY`, which is the practical companion to WAL that keeps the single-writer story robust under overlapping requests.

## Driver

**`modernc.org/sqlite`** — SQLite transpiled to pure Go, no cgo. It produces a single fully-static binary, cross-compiles cleanly from the Windows dev machine to a Linux container (`GOOS=linux GOARCH=amd64`), and drops into a `FROM scratch` image with no C toolchain in CI. It is slightly slower than the cgo-based `mattn/go-sqlite3`, but that edge is irrelevant at a few writes a day, whereas the build/deploy simplicity is the most literal expression of "single deployable unit."

## DB location & configuration

- **Path** from env var **`LIFELEDGER_DB_PATH`**, defaulting to `./data/expenses.db` — local QA needs zero config; Azure sets the var to the volume-mount path (e.g. `/data/expenses.db`).
- The app **`os.MkdirAll`s the parent directory** on startup, so first run locally works with no manual folder step; on Azure the mount already exists.
- **`data/` is gitignored** — the DB file and its WAL sidecars are runtime state, never committed.
- **WAL sidecars** (`expenses.db-wal`, `expenses.db-shm`) live beside the main file, so the Azure persistent volume must be mounted at the **directory**, not the single file.

Decided in [issue #5](https://github.com/emepetres/life-ledger/issues/5); depends on the stack ([issue #2](https://github.com/emepetres/life-ledger/issues/2)) and record shape ([ADR 0001](0001-stored-expense-record-shape.md)).
