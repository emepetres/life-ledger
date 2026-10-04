#!/usr/bin/env bash
# Run INSIDE the life-ledger guest as root (create-guest.sh does this). Idempotent.
# Installs what the app and its ingress need and wires the systemd units.
# Secrets are NOT set here: put them in /etc/life-ledger/*.env (see the runbook).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)

CRANE_VERSION=v0.22.1
CRANE_SHA256=0ab7a1d6932a213aed964ce97666c3077fe691c8606413674a8b3e0b9ec4cda0
RCLONE_VERSION=v1.75.1
RCLONE_DEB_SHA256=09c9f7606ed9e31eecc1eec26a89992cf2931a8d2d1a5f0ae2bb1c11630ffb15

export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq ca-certificates curl tar sqlite3 unattended-upgrades needrestart

# --- cloudflared from Cloudflare's apt repo (ADR-0013), patched unattended -----
install -d -m 0755 /usr/share/keyrings
curl -fsSL https://pkg.cloudflare.com/cloudflare-main.gpg -o /usr/share/keyrings/cloudflare-main.gpg
echo 'deb [signed-by=/usr/share/keyrings/cloudflare-main.gpg] https://pkg.cloudflare.com/cloudflared any main' \
  >/etc/apt/sources.list.d/cloudflared.list
apt-get update -qq
apt-get install -y -qq cloudflared

# Let unattended-upgrades take cloudflared updates, and restart services after
# a binary swap (needrestart 'a' = automatic) so the new version actually runs.
cat >/etc/apt/apt.conf.d/52unattended-upgrades-cloudflared <<'CONF'
Unattended-Upgrade::Origins-Pattern:: "site=pkg.cloudflare.com";
CONF
install -d /etc/needrestart/conf.d
printf '%s\n' "\$nrconf{restart} = 'a';" >/etc/needrestart/conf.d/50-auto.conf
systemctl enable --now unattended-upgrades.service >/dev/null

# --- crane, pinned and checksum-verified ---------------------------------------
if ! /usr/local/bin/crane version 2>/dev/null | grep -q "${CRANE_VERSION#v}"; then
  tmp=$(mktemp -d)
  curl -fsSL -o "$tmp/crane.tgz" \
    "https://github.com/google/go-containerregistry/releases/download/${CRANE_VERSION}/go-containerregistry_Linux_x86_64.tar.gz"
  echo "$CRANE_SHA256  $tmp/crane.tgz" | sha256sum -c -
  tar -xzf "$tmp/crane.tgz" -C "$tmp" crane
  install -m 0755 "$tmp/crane" /usr/local/bin/crane
  rm -rf "$tmp"
fi

# --- rclone for the offsite backup (#104), pinned and checksum-verified --------
if ! rclone version 2>/dev/null | head -1 | grep -q "rclone $RCLONE_VERSION\$"; then
  tmp=$(mktemp -d)
  curl -fsSL -o "$tmp/rclone.deb" \
    "https://downloads.rclone.org/${RCLONE_VERSION}/rclone-${RCLONE_VERSION}-linux-amd64.deb"
  echo "$RCLONE_DEB_SHA256  $tmp/rclone.deb" | sha256sum -c -
  apt-get install -y -qq "$tmp/rclone.deb"
  rm -rf "$tmp"
fi

# --- directories and root-only env files ---------------------------------------
install -d -m 0755 /opt/life-ledger
install -d -m 0755 /var/lib/life-ledger          # DB lives here; restored before first start
install -d -m 0700 /etc/life-ledger
install -d -m 0755 /var/lib/life-ledger-updater
for f in app.env cloudflared.env; do
  [[ -e /etc/life-ledger/$f ]] || install -m 0600 /dev/null "/etc/life-ledger/$f"
done

# --- units and updater ---------------------------------------------------------
install -m 0755 "$here/life-ledger-update.sh" /usr/local/sbin/life-ledger-update
install -m 0755 "$here/life-ledger-backup.sh" /usr/local/sbin/life-ledger-backup
for u in life-ledger.service cloudflared.service life-ledger-update.service life-ledger-update.timer \
         life-ledger-backup.service life-ledger-backup.timer; do
  install -m 0644 "$here/$u" "/etc/systemd/system/$u"
done
systemctl daemon-reload
systemctl enable life-ledger.service cloudflared.service life-ledger-update.timer >/dev/null
# life-ledger-backup.timer is NOT enabled here: it needs rclone.conf and
# backup.env first (runbook step 7), otherwise it would fail every night.
echo "bootstrap done"
