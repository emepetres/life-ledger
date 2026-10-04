# Nightly offsite snapshot to Google Drive

**Status:** accepted — supersedes [ADR-0007](0007-scheduled-retained-backup.md)
(scheduled retained backup on Azure Blob). **Amends
[ADR-0010](0010-host-home-mini-pc.md)'s RPO from near-zero to 24 hours.** Decided
in [#99](https://github.com/emepetres/life-ledger/issues/99) and
[#104](https://github.com/emepetres/life-ledger/issues/104); the app side is in
[ADR-0012](0012-storage-durability-on-the-box.md).

On the home box the database is one file on local ext4, and the app has no backup
role. A **box-side script and systemd timer** takes a nightly, integrity-checked
snapshot and uploads it to Google Drive. It keeps what ADR-0007 decided about
*history* (7 dailies, month-ends forever, a gate before the copy) and replaces the
*mechanism* (Azure Blob and a GitHub Actions cron). The restore side is the
[restore runbook](../deployment/restore-runbook.md).

## Decisions

| Aspect | Decision |
| --- | --- |
| Scheduler | `life-ledger-backup.timer` at **03:00 Europe/Madrid**, `Persistent=true` (a night lost to a power cut runs on boot). The guest clock is UTC, so the zone is explicit in `OnCalendar`. |
| Runner | `life-ledger-backup.service` runs `life-ledger-backup` (bash) as the **app's own dynamic user** (`User=life-ledger` + `DynamicUser=yes` resolves to the same uid), so no root-owned `-wal`/`-shm` appears next to the DB. |
| Mechanism | `sqlite3 … "VACUUM INTO"` a snapshot in the unit's `RuntimeDirectory`. No app endpoint, no app change. |
| Integrity gate | The upload happens **only if** the snapshot opens, `PRAGMA integrity_check` = `ok` and `goose_db_version` ≥ 1. A trip means no upload, no prune and a `/fail` ping. |
| Layout | `daily/YYYY-MM-DD.db` and `monthly/YYYY-MM.db` under the remote path `gdrive:life-ledger`. On the 1st the run also writes `monthly/<previous month>.db`, labelled by the month it represents, as in ADR-0007. |
| Retention | **7 dailies, month-ends forever**, pruned **in the script** (`rclone delete --min-age 7d daily/`), after a passing gate only. A stopped timer degrades to stale-but-present, never to missing. |
| Transport | **`rclone`** to a Google Drive remote named `gdrive`, with the dev's own **OAuth client published *In production*** and the **`drive.file`** scope (the app only sees files it created). Left in *Testing*, refresh tokens expire after 7 days and the backup silently dies. |
| Credentials | `/etc/life-ledger/rclone.conf` and `backup.env`, root-only, handed to the unit with `LoadCredential=`. The canonical copies are in the password manager (ADR-0011). |
| Dead-man's switch | **healthchecks.io**: the script pings `/start`, then success or `/fail`. A period of 1 day with a 6 h grace emails the dev on a failure or a missed night. This **replaces the sticky `backup-alarm` issue** and closes the stopped-cron gap ADR-0007 accepted. |
| Encryption | **None**; `age` is deferred. The gate already runs producer-side, so encryption slots in after it. |
| Portability | ADR-0010's "S3-shaped" invariant is relaxed to **"any rclone remote"**. Moving off Drive is a config change, not a code change. |
| fsync | Per [ADR-0012](0012-storage-durability-on-the-box.md), the script `sync`s any file it relies on, and treats local status files as advisory. healthchecks.io is the truth for "did it run". |

## Amendment to ADR-0010: RPO is 24 hours

ADR-0010 asked for near-zero data loss, with every committed write reaching offsite
storage. That was written when the Azure per-write backup existed. A nightly
snapshot cannot meet it: if the drive dies, **up to 24 hours of writes are lost**.
For a one-user ledger with a handful of writes a day, re-entering a day of
expenses is acceptable, and per-write offsite shipping would put a backup role back
into the app, which ADR-0012 removed. The local disk is the only copy of the day's
writes, which is why ADR-0012 pins `synchronous=FULL`.

## Considered options

- **Keep ADR-0007's Azure Blob and GitHub Actions cron.** Gone with the Azure
  subscription.
- **S3-compatible bucket (Backblaze B2, R2).** Fits the old invariant, but costs a
  new account and a recurring bill. Drive is already paid for and watched.
- **Per-write offsite copy.** Meets the old RPO, but puts a backup role back in the
  app (ADR-0012).
- **The `backup-alarm` issue.** Needs an alarm raiser that runs somewhere
  independent of the box. healthchecks.io is that, and also catches a box that is
  down.

## Accepted risks

- **Account compromise.** Snapshots are unencrypted. A compromise of the Google
  account, or of the box's `drive.file` token, exposes the full history. `age` is
  the planned upgrade.
- **RPO of 24 hours** (above), and a drive failure on the box inside that window
  loses the day.
- **"7 days" is 7–9 in practice.** The prune runs only on nights the gate passes.
- **OAuth consent drift.** If Google moves the app back to *Testing* or revokes
  the token, uploads fail and healthchecks.io mails the dev.

## Consequences

- The Azure storage account, the `ledgersnapshots` container and the CI roles
  from ADR-0007 are no longer part of the design.
- The snapshot→image-SHA link is still not recorded; for the migration-is-the-bug
  path the operator correlates dates against git history by hand.
- Restore is an operator action documented in the
  [restore runbook](../deployment/restore-runbook.md), and the mini-PC runbook's
  step 7 (setup) and step 8 (restore) link to it.
