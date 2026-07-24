# Persistence & storage design

Expenses are persisted in **SQLite**, accessed from Go through the pure-Go **`modernc.org/sqlite`** driver, with the schema created and evolved by **versioned migrations embedded in the binary**. The database is a single file on disk; on Azure Container Apps that file lives on a **replica-scoped ephemeral volume (`EmptyDir`)**, backed up to Azure Blob after each write.

## Storage mechanism

SQLite, one file. It honours every standing preference for this effort — single deployable unit, cheap, splittypie-simple, trivially runnable locally — and the app's load profile (one user, a handful of writes a day) never touches SQLite's concurrency limits.

The real tension is *where the file lives on Azure Container Apps (ACA)*, and this was investigated in depth ([map #19](https://github.com/emepetres/life-ledger/issues/19)). The original design intent — SQLite on a **mounted Azure Files SMB share** — is **unviable**, and was replaced.

### Why not SMB (the original plan): proven unviable

The first Azure deploy crashed with `database is locked (5) (SQLITE_BUSY)` while goose created its version table. A minimal, isolated repro ([#20](https://github.com/emepetres/life-ledger/issues/20)) confirmed the cause is fundamental, not an artefact of the tangled first deploy: a bare `CREATE TABLE` against an Azure Files SMB share fails from a **Linux CIFS mount — which is exactly what ACA runs** — under *every* pragma set tried (`WAL`, `DELETE`, `DELETE`+`locking_mode=EXCLUSIVE`), always after the full `busy_timeout` elapses. Azure Files SMB does not provide the POSIX byte-range advisory locks SQLite needs ([#21](https://github.com/emepetres/life-ledger/issues/21)), so the lock never becomes grantable and `busy_timeout` can't rescue it.

The **dev-parity trap** that made this look uncertain: the *same* repro **succeeds** from a Windows SMB client. Anyone validating SMB+SQLite from a Windows dev box would wrongly conclude it works. The failure is specific to the Linux CIFS client — i.e. production.

### The decision: ephemeral `EmptyDir` + backup-on-write to Blob

The DB file lives on a **replica-scoped ephemeral volume (`EmptyDir`)** — local ext4, which gives SQLite **genuine POSIX file locking**, eliminating the `SQLITE_BUSY` root cause entirely. Because `EmptyDir` is wiped on replica reschedule, the app **backs the DB up to an Azure Blob container after each write** and **restores it on boot**. At a few writes a day this is near-free (~$0 over today's costs) and preserves every standing preference: single static binary, single-writer, and *perfect* local/prod parity (identical to `go run`).

**Durability guarantee:** worst-case loss on an unplanned replica reschedule is **the single most recent write** — and only if the reschedule lands in the sub-second window between commit and the backup completing.

**Known, accepted risk — the revision-swap window.** `EmptyDir` is *replica-scoped*: each replica has its own local disk. During a revision **deploy**, ACA briefly runs the old and new replica together while handing over. In that window both replicas read/write independent local DB copies and back up to the same Blob path, so last-writer-wins can clobber a write made on the other replica — losing potentially more than one write. For a single user who isn't entering an expense at the exact second a deploy is pushed, this is near-zero; deploys are rare and operator-initiated. This risk is **accepted**; the mitigation is operational (don't enter data mid-deploy), not architectural. NFS Azure Files is the option that *removes* this window — see rejected alternatives.

**Consistent-snapshot requirement — satisfied.** The backup produces a **consistent snapshot** via `VACUUM INTO` — a single sidecar-free file captured at a committed point, never a raw copy of a live WAL-mode database — so `Save`/`Load` move exactly one file. This is implemented in `internal/store`: a `Backup` sink (`Save`/`Load`), injected via `WithBackup` and provable with `go test` alone (a fake in-process sink). The store snapshots and `Save`s **synchronously after every mutation before the write returns**, so "write returned" ≡ "backup durable"; on boot it restores from the sink only when the local file is absent (an existing file is authoritative and never clobbered by an older snapshot), and migrations run after any restore. No sink → pure local file, unchanged.

## Rejected alternatives

Evidence: repro [#20](https://github.com/emepetres/life-ledger/issues/20), comparison [#21](https://github.com/emepetres/life-ledger/issues/21). Cost figures are order-of-magnitude (West Europe); they were not Pricing-Calculator-verified because the chosen option is the ~$0 one and these appear only as rejected.

- **Azure Files SMB + SQLite** — *proven dead* (#20). No pragma set acquires write locks on a Linux CIFS mount. This was the original plan.
- **NFS Azure Files** — viable and the proper *platform-durable* fix (real POSIX advisory locks, survives restart **and** reschedule, removes the revision-swap window). Rejected on **cost and complexity**: ~$16/mo floor (premium SSD, 100 GiB minimum) **plus a mandatory custom VNet / private endpoint** — durability we don't need for a personal tracker where the worst case is re-typing one just-entered expense.
- **Blob via BlobFuse** — **disqualified**: ACA cannot mount Blob at all, and BlobFuse is not POSIX-compliant (non-atomic rename, no real byte-range locking) — strictly worse than SMB for SQLite.
- **"Managed disk / block volume attached to the Container App"** (an early favourite) — **does not exist on ACA**. Container Apps supports only container-scoped ephemeral, replica-scoped `EmptyDir`, and Azure Files (SMB or NFS). Block-disk attach is an AKS/ACI capability.
- **PostgreSQL Flexible Server (B1ms)** — the robust "graduate off SQLite" option, but **overkill today**: ~$15–17/mo, rewrites `internal/store` (~120 lines: `?`→`$N`, `LastInsertId()`→`RETURNING`, drop WAL/`busy_timeout`, goose postgres dialect), and loses the single-static-binary and local-parity story. Kept as the escape hatch below.

## Escape hatch

Graduate to **PostgreSQL (B1ms)** when the **single-writer invariant no longer holds** — i.e. the app becomes multi-user or multi-replica, or write volume grows enough that `min1/max1` is a real constraint rather than a comfortable ceiling. Until then, SQLite-on-ephemeral wins on cost and simplicity.

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

Two pragmas are set at connection open, regardless: `journal_mode=WAL` (better concurrency and resilience under overlapping requests) and `foreign_keys=ON` (harmless now, the correct default for when a second table arrives). A third, `busy_timeout=5000`, accompanies them: it tells a connection to wait briefly for a momentary write lock rather than failing immediately with `SQLITE_BUSY`. These pragmas are sound here because the DB now lives on a **local ext4 filesystem** (the ephemeral `EmptyDir` volume), which honours the POSIX locks WAL and `busy_timeout` rely on — the guarantee an SMB share could *not* provide (see Storage mechanism).

## Driver

**`modernc.org/sqlite`** — SQLite transpiled to pure Go, no cgo. It produces a single fully-static binary, cross-compiles cleanly from the Windows dev machine to a Linux container (`GOOS=linux GOARCH=amd64`), and drops into a `FROM scratch` image with no C toolchain in CI. It is slightly slower than the cgo-based `mattn/go-sqlite3`, but that edge is irrelevant at a few writes a day, whereas the build/deploy simplicity is the most literal expression of "single deployable unit."

## DB location & configuration

- **Path** from env var **`LIFELEDGER_DB_PATH`**, defaulting to `./data/expenses.db` — local QA needs zero config; Azure sets the var to the ephemeral-volume mount path (e.g. `/data/expenses.db`).
- The app **`os.MkdirAll`s the parent directory** on startup, so first run locally works with no manual folder step; on Azure the `EmptyDir` mount already exists (and starts empty on each reschedule — the boot-time restore-from-Blob repopulates it).
- **`data/` is gitignored** — the DB file and its WAL sidecars are runtime state, never committed.
- **WAL sidecars** (`expenses.db-wal`, `expenses.db-shm`) live beside the main file, so the ephemeral volume is mounted at the **directory**, not the single file.

Decided in [issue #5](https://github.com/emepetres/life-ledger/issues/5); depends on the stack ([issue #2](https://github.com/emepetres/life-ledger/issues/2)) and record shape ([ADR 0001](0001-stored-expense-record-shape.md)).
