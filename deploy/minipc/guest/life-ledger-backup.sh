#!/usr/bin/env bash
# Nightly offsite backup (ADR-0010 as amended by #99). Runs in the guest from
# life-ledger-backup.service, as the SAME dynamic user as the app so the SQLite
# -wal/-shm files never end up root-owned.
#
#   VACUUM INTO -> integrity gate -> upload daily/ (+ monthly/ on the 1st) -> prune -> ping
#
# A gate trip means: no upload, no prune, ping /fail. The dead-man's switch
# (healthchecks.io) mails the dev on /fail or on a missed night.
#
# Inputs (from the unit): DB_PATH, BACKUP_REMOTE (e.g. gdrive:life-ledger),
# HEALTHCHECK_URL (from /etc/life-ledger/backup.env), and the credential
# `rclone.conf` in $CREDENTIALS_DIRECTORY.
set -euo pipefail

: "${DB_PATH:?}" "${BACKUP_REMOTE:?}" "${HEALTHCHECK_URL:?}"
work=${RUNTIME_DIRECTORY:?}

ping() { curl -fsS -m 15 --retry 3 -o /dev/null "$HEALTHCHECK_URL$1" || echo "healthcheck ping $1 failed" >&2; }
fail() { echo "backup FAILED: $*" >&2; ping /fail; exit 1; }
trap 'fail "unexpected error at line $LINENO"' ERR

# rclone may rewrite the config when it refreshes the access token; keep a
# writable throwaway copy so the root-only credential stays read-only.
export RCLONE_CONFIG=$work/rclone.conf
cp "$CREDENTIALS_DIRECTORY/rclone.conf" "$RCLONE_CONFIG"

ping /start
snap=$work/snapshot.db
rm -f "$snap"
sqlite3 "$DB_PATH" "VACUUM INTO '$snap'"

# Integrity gate: the snapshot must be sound AND migrated (goose_db_version >= 1).
[[ $(sqlite3 "$snap" 'PRAGMA integrity_check') == ok ]] || fail "integrity_check failed"
ver=$(sqlite3 "$snap" 'SELECT COALESCE(MAX(version_id),0) FROM goose_db_version')
(( ver >= 1 )) || fail "goose_db_version=$ver (empty or unmigrated DB)"

day=$(date +%F)
rclone copyto "$snap" "$BACKUP_REMOTE/daily/$day.db"
# On the 1st the snapshot represents the month that just ended: label it so (ADR-0007).
if [[ $(date +%d) == 01 ]]; then
  rclone copyto "$snap" "$BACKUP_REMOTE/monthly/$(date -d yesterday +%Y-%m).db"
fi

# Retention (ADR-0007): dailies 7 days, month-ends forever. Done here so a
# stopped timer degrades to stale-but-present, never to missing.
rclone delete --min-age 7d "$BACKUP_REMOTE/daily"

rm -f "$snap"
echo "backup ok: daily/$day.db (goose version $ver)"
ping ""
