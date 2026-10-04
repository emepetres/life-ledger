# Mini-PC deployment runbook

How Life Ledger is provisioned on the home mini-PC (Proxmox). Decisions live in
[ADR-0010](../../adr/0010-host-home-mini-pc.md) (host),
[ADR-0011](../../adr/0011-runtime-lxc-static-binary.md) (runtime),
[ADR-0012](../../adr/0012-storage-durability-on-the-box.md) (durability) and
[ADR-0013](../../adr/0013-public-ingress-cloudflare-tunnel.md) (ingress). This file
records the *steps*. It supersedes the Azure docs in this folder, which go away
with the Azure teardown.

Commands that run on the mini-PC are `bash`; the ones on the dev machine are
`pwsh`. Ops files are in [`deploy/minipc/`](../../../deploy/minipc/).

> **Status:** written while working
> [#103](https://github.com/emepetres/life-ledger/issues/103). A step marked
> has been written but not executed or verified on the box.

## Layout

```text
deploy/minipc/
  proxmox/create-guest.sh      # on the Proxmox host: create LXC + firewall, bootstrap
  guest/bootstrap.sh           # in the guest: packages, crane, cloudflared, units
  guest/life-ledger.service    # the app (DynamicUser, ProtectSystem=strict)
  guest/cloudflared.service    # the tunnel (token from its own env file)
  guest/life-ledger-update.*   # 5-min digest poll, swap, /health check, rollback
```

In the guest: binary `/opt/life-ledger/life-ledger`, DB
`/var/lib/life-ledger/expenses.db`, root-only env files
`/etc/life-ledger/app.env` and `/etc/life-ledger/cloudflared.env`.

## Facts about the host

- Proxmox VE 9.2 at `192.168.1.167`, web UI on port `8006`, node `proxmox`.
  Connection details for the dev machine live in the gitignored `.env`
  (`MINIPC_HOST`, `MINIPC_USER`, `MINIPC_PASSWORD`, `PROXMOX_PORT`).
- Home Assistant is VM `100`. The Life Ledger guest is CT `101`.
- Template: `local:vztmpl/debian-13-standard_13.6-1_amd64.tar.zst`. Storage:
  `local-lvm` (thin LVM).
- CT `101` was the throwaway #96 lock-probe guest. It held no data and is replaced
  by step 1.

## 1. Create the guest and firewall

Copy `deploy/minipc/` to the host and run the script there. `--replace` destroys
the old CT `101`: check what is in it first.

```pwsh
# From the repo root on the dev machine
scp -r deploy/minipc root@192.168.1.167:/root/minipc
ssh root@192.168.1.167 "chmod +x /root/minipc/*/*.sh"
```

```bash
# On the Proxmox host
bash /root/minipc/proxmox/create-guest.sh /root/minipc/guest --replace
```

What it does:

- Creates an unprivileged Debian 13 LXC `life-ledger`: 2 cores, 1 GB RAM, 512 MB
  swap, 8 GB root on `local-lvm`, DHCP, `onboot=1`, `startup: order=2`.
- Gives HA VM `100` `startup: order=1` if it has none. Guests with no order start
  last, so without this the app would start before HA.
- Turns the **datacenter** firewall on (guest rules need it) and leaves the
  **host** firewall explicitly off, so SSH and the web UI cannot be locked out.
  The host itself was unfiltered before and still is.
- Writes `/etc/pve/firewall/101.fw`: **no inbound rules**; outbound allows DNS and
  the gateway, drops all private ranges (rest of the LAN, HA included), and allows
  the internet.
- Runs `guest/bootstrap.sh` in the guest: `ca-certificates`, `curl`,
  `unattended-upgrades`, `needrestart`, `cloudflared` from Cloudflare's apt repo
  (patched by unattended-upgrades), a SHA-256-verified pinned `crane`, the
  directories and empty root-only env files, and the systemd units.

Give the guest a fixed DHCP lease in the router if you want a stable address; the
app never needs one, since nothing connects in.

## 2. Secrets: the env files

Both files are `0600 root`. **The canonical copy of every value is in the
password manager**; these are deployment copies.

`/etc/life-ledger/app.env`:

```ini
LIFELEDGER_PASSWORD_HASH=<bcrypt hash>
LIFELEDGER_SESSION_KEY=<random key>
```

`/etc/life-ledger/cloudflared.env`:

```ini
TUNNEL_TOKEN=<tunnel token>
```

The tunnel token has its own file so the app process never sees it. The listen
address, secure-cookie flag, DB path and timezone are set in the unit, not here.
There are **no backup keys** (the backup is a separate box-side job,
[#104](https://github.com/emepetres/life-ledger/issues/104)).

## 3. Cloudflare Tunnel (dashboard, by hand)

1. Cloudflare dashboard → **Zero Trust → Networks → Tunnels → Create a tunnel →
   Cloudflared**. Name it `life-ledger`.
2. Copy the **token** from the install command it shows. Put it in the password
   manager and in `cloudflared.env` (step 2). Do not run the install command it
   shows: the unit in this repo replaces it.
3. **Public hostname:** subdomain `ledger`, domain `carnero.net`, service type
   `HTTP`, URL `127.0.0.1:8080`.
4. Zone `carnero.net` → **SSL/TLS → Edge Certificates → Always Use HTTPS: On**.
5. Record any other setting you changed here.

Then in the guest: `systemctl restart cloudflared` and check
`systemctl status cloudflared`.

## 4. First start needs a DB

`life-ledger.service` has `AssertPathExists=/var/lib/life-ledger/expenses.db`
(ADR-0012), so it **refuses to start** until a DB is in place. The real restore is
its own step in the map. To smoke-test the plumbing before then, create an empty
file the app will migrate:

```bash
# In the guest. Throwaway DB for the smoke test only: delete it before restoring.
touch /var/lib/life-ledger/expenses.db
```

## 5. The binary: GHCR updater

`life-ledger-update.timer` runs every 5 minutes. `life-ledger-update`:

1. `crane digest` the `:latest` image (`ghcr.io/emepetres/life-ledger:latest`,
   public package, no credentials).
2. If the digest is unchanged, or equals the last digest that failed `/health`,
   do nothing.
3. Otherwise `crane export` the image, extract `life-ledger`, keep the old binary
   as `.prev`, swap it in, and restart the unit.
4. Poll `http://127.0.0.1:8080/health` for up to 20 s. On success record the
   digest. On failure restore `.prev`, restart, and remember the bad digest so it
   is not retried every 5 minutes.

**Prerequisite:** the image must exist on GHCR and the package must be public
(GitHub → Packages → `life-ledger` → Package settings → Change visibility; there
is no API for it). The first `:latest` was pushed by hand with `crane append` onto
`gcr.io/distroless/static-debian12:nonroot` plus `crane mutate` (entrypoint, env,
`org.opencontainers.image.source` label). The pipeline rewrite will replace that.
Pushing needs a `gh` token with `write:packages` (`gh auth refresh -h github.com -s write:packages`).

Force a run now: `systemctl start life-ledger-update.service`, then
`journalctl -u life-ledger-update -n 50`.

## Findings from the first run

- Proxmox warns "Systemd 257 detected. You may need to enable nesting" on create.
  It is not needed: the unit runs with the full sandbox (`DynamicUser`,
  `ProtectSystem=strict`, `PrivateTmp`, ...) in the unprivileged guest.
- `LIFELEDGER_SESSION_KEY` must be set (a random 32-byte Base64 value). It was
  empty in `.env`, so one was minted. Keep it in the password manager.
- The empty `expenses.db` from step 4 is a throwaway placeholder. Delete it before
  the real restore.

## 6. Verify

Done 2026-10-04: health, public HTTPS (200) with HTTP→HTTPS redirect, firewall
(internet and DNS work; Proxmox host 22/8006 blocked from the guest), updater
swap, and rollback (bad binary rejected, old one restored, bad digest not
retried). Rollback was tested with a stub `crane` in the guest.

- [x] `curl -fsS http://127.0.0.1:8080/health` in the guest returns `ok`.
- [x] `https://ledger.carnero.net` loads, and login works **from a phone on mobile
      data**.
- [x] From the guest, the LAN is blocked: `ping 192.168.1.100` (HA) fails while
      `curl https://ghcr.io` works.
- [ ] From another LAN machine, nothing answers on the guest's address.
- [x] A bad image rolls back: push a binary that exits non-zero, confirm the old
      one is restored and the digest is not retried.
- [ ] **Real power cut (not performed when #103 was closed):** pull the plug, restore power, touch nothing. HA, the
      guest, the app and the tunnel all come back and the phone login works again.
