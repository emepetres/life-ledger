#!/usr/bin/env bash
# Poll :latest by digest; on change extract the binary, swap it in, check /health,
# and roll back if the new binary is unhealthy (ADR-0011). Run by the update timer.
set -euo pipefail

IMAGE=${LIFELEDGER_IMAGE:-ghcr.io/emepetres/life-ledger:latest}
BIN=/opt/life-ledger/life-ledger
STATE=/var/lib/life-ledger-updater
HEALTH=http://127.0.0.1:8080/health

digest=$(crane digest --platform linux/amd64 "$IMAGE")
[[ $digest == sha256:* ]] || { echo "unexpected digest: $digest" >&2; exit 1; }

current=$(cat "$STATE/current.digest" 2>/dev/null || true)
failed=$(cat "$STATE/failed.digest" 2>/dev/null || true)
[[ $digest == "$current" ]] && exit 0
# A digest that already failed /health is not retried every 5 minutes; a newer
# push has a new digest and is tried.
[[ $digest == "$failed" ]] && exit 0

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
crane export --platform linux/amd64 "${IMAGE%:*}@$digest" - | tar -x -C "$tmp" life-ledger
install -m 0755 "$tmp/life-ledger" "$BIN.new"

healthy() {
  for _ in $(seq 1 20); do
    curl -fsS --max-time 2 "$HEALTH" >/dev/null 2>&1 && return 0
    sleep 1
  done
  return 1
}

[[ -e $BIN ]] && cp -p "$BIN" "$BIN.prev"
mv "$BIN.new" "$BIN"
systemctl restart life-ledger.service

if healthy; then
  echo "$digest" >"$STATE/current.digest"
  rm -f "$STATE/failed.digest"
  echo "updated to $digest"
else
  echo "new binary $digest failed /health; rolling back" >&2
  echo "$digest" >"$STATE/failed.digest"
  if [[ -e $BIN.prev ]]; then
    mv "$BIN.prev" "$BIN"
    systemctl restart life-ledger.service
  fi
  exit 1
fi
