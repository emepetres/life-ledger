#!/usr/bin/env bash
# Run ON THE PROXMOX HOST as root (ADR-0011). Creates the `life-ledger` LXC and
# its firewall, then bootstraps the guest from deploy/minipc/guest/.
#
#   create-guest.sh <path-to-guest-dir> [--replace]
#
# Without --replace it refuses to touch an existing guest; with --replace it
# destroys the old one first. Destroying is irreversible: look at the target.
set -euo pipefail

VMID=101
HOSTNAME=life-ledger
TEMPLATE=local:vztmpl/debian-13-standard_13.6-1_amd64.tar.zst
GATEWAY=192.168.1.1
HA_VMID=100

guest_dir=${1:?usage: create-guest.sh <path-to-guest-dir> [--replace]}
replace=${2:-}

if pct status "$VMID" >/dev/null 2>&1; then
  if [[ $replace != --replace ]]; then
    echo "guest $VMID already exists; pass --replace to destroy and recreate it" >&2
    exit 1
  fi
  pct stop "$VMID" 2>/dev/null || true
  pct destroy "$VMID" --purge 1
fi

pct create "$VMID" "$TEMPLATE" \
  --hostname "$HOSTNAME" \
  --unprivileged 1 \
  --ostype debian \
  --cores 2 --memory 1024 --swap 512 \
  --rootfs local-lvm:8 \
  --net0 name=eth0,bridge=vmbr0,ip=dhcp,firewall=1 \
  --onboot 1 --startup order=2 \
  --start 0

# Auto-start order: HA first, then Life Ledger (ADR-0011). Guests without an
# explicit order start last, so HA needs order=1 or it would start after us.
if ! qm config "$HA_VMID" | grep -q '^startup:'; then
  qm set "$HA_VMID" --startup order=1
fi

# --- firewall -----------------------------------------------------------------
# The datacenter firewall must be on for guest rules to apply. The *host*
# firewall is left off explicitly so enabling this cannot lock SSH or the web UI
# out of the host.
node=$(hostname)
install -d /etc/pve/firewall "/etc/pve/nodes/$node"
if [[ ! -f /etc/pve/firewall/cluster.fw ]]; then
  printf '[OPTIONS]\nenable: 1\n' >/etc/pve/firewall/cluster.fw
fi
if [[ ! -f /etc/pve/nodes/$node/host.fw ]]; then
  printf '[OPTIONS]\nenable: 0\n' >"/etc/pve/nodes/$node/host.fw"
fi

# No inbound rules at all (ingress is an outbound tunnel). Outbound: DNS and the
# gateway, then drop every private range (the rest of the LAN, HA VM included),
# then the internet is allowed. First match wins, so the order matters.
cat >"/etc/pve/firewall/$VMID.fw" <<FW
[OPTIONS]
enable: 1
policy_in: DROP
policy_out: ACCEPT
dhcp: 1
ndp: 0
radv: 0
macfilter: 1

[RULES]
OUT ACCEPT -dest $GATEWAY -p udp -dport 53
OUT ACCEPT -dest $GATEWAY -p tcp -dport 53
OUT ACCEPT -dest $GATEWAY
OUT DROP -dest 10.0.0.0/8
OUT DROP -dest 172.16.0.0/12
OUT DROP -dest 192.168.0.0/16
OUT DROP -dest 169.254.0.0/16
FW

pct start "$VMID"

# --- bootstrap ----------------------------------------------------------------
# Wait for a default route, then ship the guest dir in and run it.
for _ in $(seq 1 30); do
  pct exec "$VMID" -- ip route show default 2>/dev/null | grep -q default && break
  sleep 2
done
tar -C "$guest_dir" -czf /tmp/life-ledger-guest.tgz .
pct push "$VMID" /tmp/life-ledger-guest.tgz /root/guest.tgz
rm -f /tmp/life-ledger-guest.tgz
pct exec "$VMID" -- bash -c 'rm -rf /root/guest && mkdir /root/guest && tar -xzf /root/guest.tgz -C /root/guest && bash /root/guest/bootstrap.sh'
