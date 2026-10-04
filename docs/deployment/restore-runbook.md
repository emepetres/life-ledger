# Restore runbook — recover the database from a Drive snapshot

When a bad write, a corruption, a broken migration or a dead drive has damaged
the live database, this is how you put a known-good snapshot back. It is the
operator counterpart to the nightly offsite backup
([ADR-0014](../adr/0014-nightly-offsite-snapshot-to-google-drive.md)): the timer
*makes* dated snapshots on Google Drive; this runbook *promotes* one back into
production. It also covers the first real restore of a fresh guest
([mini-PC runbook](mini-pc/README.md), step 8).

Commands that run **on the box** (the Proxmox host or the `life-ledger` guest,
CT `101`) are `bash`; commands on the dev machine are `pwsh`
([AGENTS.md](../../AGENTS.md)). Run the `bash` blocks in the guest as root, for
example `ssh root@192.168.1.167` then `pct enter 101`.

> **Scope.** Data damage and box loss. The RPO is 24 hours (ADR-0014): the newest
> snapshot is at most one night old, so anything written since 03:00 is lost.

## Names used below

| What | Value |
| --- | --- |
| App unit | `life-ledger.service` |
| Updater | `life-ledger-update.service` and `life-ledger-update.timer` |
| Live DB | `/var/lib/life-ledger/expenses.db` (a symlink into `/var/lib/private/life-ledger`, because of `DynamicUser`) |
| rclone remote | `gdrive:life-ledger`, config `/etc/life-ledger/rclone.conf` |
| Snapshots | `daily/YYYY-MM-DD.db`, `monthly/YYYY-MM.db`; restores also leave `pre-restore/` |

`pre-restore/` is outside `daily/`, so the nightly prune never touches it.

## The one gate: is a migration implicated?

> **Did a database migration cause or contribute to the damage?**

- **No** — a fat-finger delete, corruption, an app-logic bug, a dead drive. Take the
  [ordinary restore](#ordinary-restore).
- **Yes** — a migration itself mangled the data, or shipped alongside the bug.
  Take the [migration-is-the-bug](#migration-is-the-bug-pin-an-older-image)
  path: restoring old data under the *current* binary would re-run the bad
  migration on boot and damage it again.

## Ordinary restore

### 1. List the snapshots and download one

```bash
export RCLONE_CONFIG=/etc/life-ledger/rclone.conf
rclone lsl gdrive:life-ledger/daily      # newest 7 nights
rclone lsl gdrive:life-ledger/monthly    # month-ends, kept forever
SNAPSHOT=daily/2026-10-03.db             # the newest one that predates the damage
rclone copyto "gdrive:life-ledger/$SNAPSHOT" /root/restore.db
```

### 2. Run the same three-check gate on it

The same gate the nightly script applies before it uploads. Do not install a file
that fails it.

```bash
[[ $(sqlite3 /root/restore.db 'PRAGMA integrity_check') == ok ]] || echo "FAILED integrity_check"
sqlite3 /root/restore.db 'SELECT MAX(version_id) FROM goose_db_version'   # must be >= 1
sqlite3 /root/restore.db 'SELECT COUNT(*) FROM expense'                  # note this row count
```

It must open, report `ok`, and have a goose version of at least 1. If it fails,
pick an older snapshot.

### 3. Stop the app and the update timer

The app must not write during the swap, and the updater must not restart the unit
under you.

```bash
systemctl stop life-ledger-update.timer
systemctl stop life-ledger.service
```

### 4. Keep the current DB as a dated `pre-restore` copy, locally and on Drive

A wrong restore must itself be reversible. Copy the DB and its `-wal` and `-shm`
sidecars (the files that exist), then upload the lot.

```bash
STAMP=$(date -u +%Y-%m-%dT%H%M%SZ)
mkdir -p /root/pre-restore-$STAMP
cp -a /var/lib/private/life-ledger/expenses.db* /root/pre-restore-$STAMP/
rclone copy /root/pre-restore-$STAMP "gdrive:life-ledger/pre-restore/$STAMP"
rclone lsl "gdrive:life-ledger/pre-restore/$STAMP"      # confirm it landed
```

If the drive is dead and there is no live DB to keep, skip this step.

### 5. Install the snapshot and remove the sidecars

Ownership comes from the unit's `StateDirectory`: the directory belongs to the
app's dynamic uid, and the file must match it. The stale `-wal`/`-shm` belong to
the old DB and must go, or SQLite would replay them onto the snapshot.

```bash
cd /var/lib/private/life-ledger
uid=$(stat -c %u .)
rm -f expenses.db expenses.db-wal expenses.db-shm
install -o "$uid" -g "$uid" -m 0644 /root/restore.db expenses.db
rm /root/restore.db
```

### 6. Start the unit and verify

```bash
systemctl start life-ledger.service
sleep 4
curl -fsS http://127.0.0.1:8080/health          # ok
sqlite3 -readonly /var/lib/life-ledger/expenses.db 'SELECT COUNT(*) FROM expense'   # the count from step 2
systemctl start life-ledger-update.timer
```

Then check the data in the UI at `https://ledger.carnero.net`, and run
`systemctl start life-ledger-backup.service` so Drive holds the restored state
tonight. A restored box with a failing unit is usually a missing DB:
`life-ledger.service` has `AssertPathExists=` on the DB path (ADR-0012), so a
restore that left no file there leaves the unit *failed* instead of starting an
empty ledger.

## Migration-is-the-bug: pin an older image

Migration implicated. You need **code that never had the bad migration**, carrying
**old data**. Never split the two: start the old binary only on the old data.

**1. Pick the snapshot and an image SHA, both from before the bad migration.**
Images are tagged by `github.sha`
(`ghcr.io/emepetres/life-ledger:<sha>`). Nothing records which SHA was live when a
snapshot was taken, so correlate by hand: match the snapshot's date against the git
history to find a commit from before the bad migration merged.

**2. Do the ordinary restore steps 1–5** (download, gate, stop the app and the
timer, keep the `pre-restore` copy, install the snapshot). Stop *before* step 6:
the unit stays stopped.

**3. Pin the updater to the old image with a systemd drop-in.** The updater
already honours `LIFELEDGER_IMAGE`.

```bash
OLD_SHA=<git sha from before the bad migration>
mkdir -p /etc/systemd/system/life-ledger-update.service.d
cat >/etc/systemd/system/life-ledger-update.service.d/pin.conf <<EOF
[Service]
Environment=LIFELEDGER_IMAGE=ghcr.io/emepetres/life-ledger:$OLD_SHA
EOF
systemctl daemon-reload
rm -f /var/lib/life-ledger-updater/failed.digest   # a past failed digest would be skipped
systemctl start life-ledger-update.service         # swaps the binary, restarts the unit, checks /health
journalctl -u life-ledger-update -n 30             # "updated to sha256:..."
```

The old binary boots on the old snapshot, sees its own schema version, and goose
is a no-op. **Do not run `goose down`** on the live schema.

**4. Keep the pin and restart the timer** (`systemctl start life-ledger-update.timer`).
With the pin in place the timer sees an unchanged digest and does nothing, so the
bad `:latest` is not pulled again.

**5. Verify** as in ordinary step 6.

**6. Revert the pin once a fixed image is on `:latest`.** Until then the box runs
old code, and you lose whatever good code shipped with the bad migration.

```bash
rm /etc/systemd/system/life-ledger-update.service.d/pin.conf
systemctl daemon-reload
systemctl start life-ledger-update.service        # pulls :latest (the fix) and swaps it in
```

## Restoring onto a fresh guest

A new or wiped guest has no DB, so `life-ledger.service` will not start. Run the
ordinary restore from step 1, skipping steps 3 and 4 (there is nothing to stop or
keep). The first start needs the guest bootstrapped and `rclone.conf` in place
([mini-PC runbook](mini-pc/README.md), step 7).

## Why it is shaped this way

[ADR-0014](../adr/0014-nightly-offsite-snapshot-to-google-drive.md) for the backup
design and its accepted risks, [ADR-0012](../adr/0012-storage-durability-on-the-box.md)
for why a missing DB fails loudly, and
[ADR-0011](../adr/0011-runtime-lxc-static-binary.md) for the updater.
