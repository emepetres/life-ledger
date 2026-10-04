# Storage durability on the home box: no in-app backup

**Status:** accepted. Supersedes the storage-mechanism half of
[ADR-0003](0003-persistence-and-storage.md) (ephemeral `EmptyDir` + backup-on-write
to Azure Blob + restore-on-boot). ADR-0003's SQLite, driver, migrations, table
shape, pragmas and DB location still hold. Decided in
[#100](https://github.com/emepetres/life-ledger/issues/100).

On the home mini-PC ([ADR-0010](0010-host-home-mini-pc.md),
[ADR-0011](0011-runtime-lxc-static-binary.md)) the DB file lives on **persistent
local ext4** in the `life-ledger` guest. Offsite backup is a nightly box-side
snapshot to Google Drive ([#99](https://github.com/emepetres/life-ledger/issues/99)),
so **the app has no backup role**: it opens a local SQLite file, and that is all.

## Decisions

- **Remove the in-app backup machinery entirely.** `internal/blobbackup`, the
  `store.Backup` interface, `WithBackup`, restore-on-boot, every
  `backupAfterWrite` call, `LIFELEDGER_BACKUP_BLOB_URL` and the Azure SDK
  dependencies all go. No unused hook is kept "in case": backup lives outside the
  app by ADR-0010's portability invariant, and git history keeps the old seam. The
  boot log keeps two origins — *created new* and *opened existing*. The removal
  ships with the deploy-pipeline rewrite, so no Azure deploy ever runs without its
  durability layer.
- **A missing DB on the box fails loudly, outside the app.** The app keeps
  create-if-absent, so local QA and CI stay zero-config. The systemd unit carries
  `AssertPathExists=` on the DB path, so a lost mount, wrong path or wiped guest
  leaves the unit *failed* instead of silently starting an empty ledger (which the
  nightly job would then upload). An assert failure is not a process exit, so
  `Restart=always` does not loop on it. Restore — and the first bootstrap from the
  local `data/` copy — is a manual runbook step.
- **`synchronous=FULL`, set explicitly** in the DSN and asserted in the pragma
  test. It was already the effective value (SQLite's default), but with a 24h
  offsite RPO the local disk is the *only* copy of the day's writes. `NORMAL`
  survived the armed power cut in [#96](https://github.com/emepetres/life-ledger/issues/96),
  but SQLite does not guarantee it in WAL mode, and one fsync per commit costs
  nothing at a handful of writes a day.
- **The revision-swap clobber risk is gone**, not reduced. It came from two ACA
  replicas each holding a private copy and racing to one Blob path. Here there is
  one file and one process, and `systemctl restart` stops before it starts; even
  an overlap would share one file under real POSIX locks. The "don't enter data
  mid-deploy" rule is retired.
- **The #96 fsync rule binds the box-side scripts, not the app.** The app writes
  no marker or status files. Scripts in `deploy/minipc/` must fsync (or `sync`)
  any file they rely on — the local snapshot before upload in particular — and
  treat any other local status file as advisory. healthchecks.io is the source of
  truth for "did last night's backup run".

## Considered options

- **Keep the `Backup` seam unused.** Rejected: dead code implying a backup role
  the app no longer has.
- **App-level "must exist" flag.** Rejected for `AssertPathExists=`: same loud
  failure, zero app code, and the guard sits with the rest of the box-specific ops.
- **`synchronous=NORMAL`.** Measured safe on this hardware, but not guaranteed;
  rejected because the saving is irrelevant at this write volume.

## Consequences

- The binary loses the Azure SDK; `go.mod` drops to the non-Azure dependency set.
- Durability = persistent local ext4 + `FULL` fsync per commit + a nightly offsite
  snapshot. Worst-case loss is the box's disk dying: up to 24h of writes
  (ADR-0010's RPO as amended by #99).
- Restoring is an operator action, documented in the restore runbook rewrite.
