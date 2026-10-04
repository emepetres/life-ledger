# Mini-PC deployment runbook

How Life Ledger is provisioned on the home mini-PC (Proxmox). Decisions live in
[ADR-0010](../../adr/0010-host-home-mini-pc.md) (host),
[ADR-0011](../../adr/0011-runtime-lxc-static-binary.md) (runtime),
[ADR-0012](../../adr/0012-storage-durability-on-the-box.md) (durability) and
[ADR-0013](../../adr/0013-public-ingress-cloudflare-tunnel.md) (ingress). This file
records the *steps*. The backup decision is in
[ADR-0014](../../adr/0014-nightly-offsite-snapshot-to-google-drive.md).

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
  guest/life-ledger-backup.*   # nightly VACUUM INTO -> gate -> Google Drive (rclone)
```

In the guest: binary `/opt/life-ledger/life-ledger`, DB
`/var/lib/life-ledger/expenses.db`, root-only files `/etc/life-ledger/app.env`,
`/etc/life-ledger/cloudflared.env`, `/etc/life-ledger/rclone.conf` and
`/etc/life-ledger/backup.env`.

## What only you can do

Everything else in this runbook is scripted. These steps need a human (a browser,
a password, an account), in this order:

| # | Step | Where | Ticket |
| --- | --- | --- | --- |
| 1 | Run `create-guest.sh` on the Proxmox host (type the host password at the `ssh`/`scp` prompts; no key is set up) | step 1 | [#103](https://github.com/emepetres/life-ledger/issues/103), done |
| 2 | Create the Cloudflare tunnel, copy its token into `.env` and `cloudflared.env` | step 3 | [#103](https://github.com/emepetres/life-ledger/issues/103), done |
| 3 | Make the GHCR package public, and grant the repo "Manage Actions access" on it | step 5 | [#103](https://github.com/emepetres/life-ledger/issues/103), [#106](https://github.com/emepetres/life-ledger/issues/106), done |
| 4 | Create the Google OAuth client (**In production**, scope `drive.file`) | step 7a | [#104](https://github.com/emepetres/life-ledger/issues/104) |
| 5 | Authorise rclone in your browser, ship `rclone.conf` to the guest | step 7b | [#104](https://github.com/emepetres/life-ledger/issues/104) |
| 6 | Create the healthchecks.io check, put the ping URL on the guest | step 7c | [#104](https://github.com/emepetres/life-ledger/issues/104) |
| 7 | Store every secret in the password manager | step 7d | [#104](https://github.com/emepetres/life-ledger/issues/104) |
| 8 | Restore `data/` into the guest **before the first real start** | step 8 | [#108](https://github.com/emepetres/life-ledger/issues/108), done |
| 9 | Pull the plug once (real power-cut test) | step 6 | [#103](https://github.com/emepetres/life-ledger/issues/103), open |

The agent cannot run anything on the box unattended: SSH there is
password-only. Either type the password when prompted, or install your key once
(`ssh-copy-id root@192.168.1.167`) so the agent can drive the box.

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
There are **no backup keys** here: the backup is a separate box-side job with its
own files (step 7).

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
(ADR-0012), so it **refuses to start** until a DB is in place. On a fresh guest,
restore the real one first (step 8). The live guest already has it (#104).

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

**Prerequisites:** the image must exist on GHCR. CI publishes it on merge to
`main` (`ci-cd.yml`), so there is nothing to push by hand. Two one-time human
steps on the package (GitHub → Packages → `life-ledger`; neither has an API):

- **Package settings → Change visibility → Public**, so the guest can pull with no
  credentials.
- **Package settings → Manage Actions access → Add repository**
  `emepetres/life-ledger` with the **Write** role, so the workflow's `GITHUB_TOKEN`
  can push to a package that was created outside Actions.

Force a run now: `systemctl start life-ledger-update.service`, then
`journalctl -u life-ledger-update -n 50`.

## Findings from the first run

- Proxmox warns "Systemd 257 detected. You may need to enable nesting" on create.
  It is not needed: the unit runs with the full sandbox (`DynamicUser`,
  `ProtectSystem=strict`, `PrivateTmp`, ...) in the unprivileged guest.
- `LIFELEDGER_SESSION_KEY` must be set (a random 32-byte Base64 value). It was
  empty in `.env`, so one was minted. Keep it in the password manager.

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

## 7. Nightly offsite backup (Google Drive)

Decided in [#99](https://github.com/emepetres/life-ledger/issues/99): a box-side
job, no app code. `life-ledger-backup.timer` fires at 03:00 Europe/Madrid
(`Persistent=true`, so a night lost to a power cut runs on boot).
`life-ledger-backup` does:

1. `sqlite3 ... "VACUUM INTO"` a snapshot, as the app's own dynamic user
   (`User=life-ledger` + `DynamicUser=yes` resolves to the same uid as the app, so
   no root-owned `-wal`/`-shm` files can appear next to the DB).
2. **Gate:** `PRAGMA integrity_check` = `ok` and `goose_db_version` >= 1. A trip
   means no upload, no prune, and a `/fail` ping.
3. `rclone copyto` to `daily/YYYY-MM-DD.db`, plus `monthly/YYYY-MM.db` on the 1st,
   labelled by the month that just ended.
4. `rclone delete --min-age 7d daily/`. Month-ends are kept forever.
5. Ping healthchecks.io. A missed night, or a `/fail`, emails you.

The bootstrap installs a pinned, SHA-256-verified `rclone`, `sqlite3`, the script
(`/usr/local/sbin/life-ledger-backup`) and both units, but **does not enable the
timer**: it needs the two files below first. The bootstrap is idempotent, so on
the existing guest, copy `deploy/minipc/` to the host again (step 1) and
re-bootstrap *without* recreating the CT:

```bash
# On the Proxmox host
tar -C /root/minipc/guest -czf /tmp/life-ledger-guest.tgz .
pct push 101 /tmp/life-ledger-guest.tgz /root/guest.tgz
pct exec 101 -- bash -c 'rm -rf /root/guest && mkdir /root/guest && tar -xzf /root/guest.tgz -C /root/guest && bash /root/guest/bootstrap.sh'
```

### 7a. Google OAuth client (browser, once)

1. <https://console.cloud.google.com> → create a project (e.g. `life-ledger`).
2. **APIs & Services → Library → Google Drive API → Enable.**
3. **OAuth consent screen / Google Auth Platform:** user type **External**, app name
   `Life Ledger backup`, your e-mail as support and developer contact. Add the
   scope `.../auth/drive.file`. Add yourself as a test user.
4. **Audience → Publish app → In production.** Google may say the app is
   unverified: that is fine for personal use. Skipping this leaves it in *Testing*,
   where refresh tokens **expire after 7 days** and the backup silently dies.
5. **Clients → Create client → Desktop app.** Copy the **client ID** and **client
   secret**.

### 7b. Authorise rclone on your machine, ship the config

A headless box cannot open a browser, so authorise on the dev machine and copy the
result. `winget install Rclone.Rclone` if needed.

```pwsh
rclone config
# n (new remote)  name: gdrive   storage: drive
# client_id / client_secret: the ones from 7a
# scope: 3 (drive.file)   root_folder_id: <blank>   service_account_file: <blank>
# Edit advanced config: n   Use auto config: y  -> sign in in the browser, "Allow"
# Configure as Shared Drive: n   keep this remote: y
rclone config file          # prints the rclone.conf path
rclone lsd gdrive:          # must not error (it will look empty: drive.file)
```

The remote **must be named `gdrive`** (the unit says `gdrive:life-ledger`).
Then send only that remote to the guest, via the Proxmox host (the guest accepts
no inbound connections):

```pwsh
$conf = rclone config file | Select-Object -Last 1
scp $conf root@192.168.1.167:/root/rclone.conf
ssh root@192.168.1.167 "pct push 101 /root/rclone.conf /etc/life-ledger/rclone.conf --perms 0600 && shred -u /root/rclone.conf"
```

If your `rclone.conf` holds other remotes, trim it to the `[gdrive]` section first.

### 7c. healthchecks.io

1. Sign up at <https://healthchecks.io> (free), create a check `life-ledger-backup`:
   **period 1 day, grace time 6 hours**, with your e-mail as the integration.
2. Copy its **ping URL** (`https://hc-ping.com/<uuid>`). The script appends
   `/start` and `/fail` to it.
3. Put it on the guest:

```bash
# On the Proxmox host
pct exec 101 -- bash -c 'umask 077; read -rp "ping URL: " u; echo "HEALTHCHECK_URL=$u" >/etc/life-ledger/backup.env'
```

### 7d. Password manager

Store, as canonical copies (ADR-0011): the contents of `rclone.conf`, the OAuth
client ID/secret, and the ping URL. The box holds deployment copies only.

### 7e. Enable and test

```bash
# In the guest
systemctl enable --now life-ledger-backup.timer
systemctl start life-ledger-backup.service        # a real run
journalctl -u life-ledger-backup -n 30            # "backup ok: daily/<date>.db"
systemctl list-timers life-ledger-backup.timer    # next run ~03:00
```

Check, in order:

- [ ] Drive has `life-ledger/daily/<today>.db` (the first of each month also
      `monthly/`). Look in the Drive web UI: files created by rclone are visible.
- [ ] healthchecks.io shows the check **up** with a fresh ping.
- [ ] **Forced gate failure** emails you. An empty file has no `goose_db_version`:

```bash
# In the guest. Same sandbox as the unit, but pointed at an empty DB.
touch /var/lib/life-ledger/empty.db
systemd-run --wait --collect -P -p User=life-ledger -p DynamicUser=yes \
  -p StateDirectory=life-ledger -p RuntimeDirectory=life-ledger-backup \
  -p LoadCredential=rclone.conf:/etc/life-ledger/rclone.conf \
  -p EnvironmentFile=/etc/life-ledger/backup.env \
  -p Environment=DB_PATH=/var/lib/life-ledger/empty.db \
  -p Environment=BACKUP_REMOTE=gdrive:life-ledger \
  /usr/local/sbin/life-ledger-backup || echo "failed as expected"
rm /var/lib/life-ledger/empty.db
```

- [ ] **A snapshot restores.** Download one and check it:

```bash
# In the guest, as root
RCLONE_CONFIG=/etc/life-ledger/rclone.conf rclone copyto \
  gdrive:life-ledger/daily/$(date +%F).db /root/restore-test.db
sqlite3 /root/restore-test.db 'PRAGMA integrity_check; SELECT MAX(version_id) FROM goose_db_version;'
rm /root/restore-test.db
```

### Accepted risks (from #99)

Unencrypted: a compromise of the Google account or of the box's `drive.file`
token exposes the full history. RPO is up to 24h (amends ADR-0010). `age`
encryption is the planned upgrade; the gate already runs producer-side so it slots
in unchanged.

### Verified 2026-10-04

Timer enabled (next run 03:00 Madrid). A real run uploaded `daily/<date>.db`; a
downloaded snapshot passed `integrity_check`; a forced gate failure sent the
`/fail` ping and emailed the dev; healthchecks.io showed the check up.

## 8. Restore the real database

The procedure lives in the [restore runbook](../restore-runbook.md): it covers
restoring from a Drive snapshot, the migration gate and the `:<sha>` downgrade.
For the first start on a fresh guest follow its
[fresh-guest section](../restore-runbook.md#restoring-onto-a-fresh-guest).

Done 2026-10-04 from the last copy of the old host (2026-09-12,
85 expenses and 8 incomes, goose version 2). The repo's local `data/expenses.db`
was **older** (July): check file dates before choosing the source. When the source
is a file on your machine rather than a Drive snapshot, `scp` it to the host,
`pct push 101` it to `/root/restore.db`, and continue from the runbook's gate step.

The real DB is in place (#104), so there is no placeholder to clean up.

Old snapshots: only the **monthly** ones (`2026-07`, `2026-08`) were uploaded to
`gdrive:life-ledger/monthly/` (`rclone copy`, which keeps modification times).
The old dailies (2026-09-06..12) were **not** uploaded: they are older than 7 days,
so the next run's prune would delete them. They stay in the local archive.

## Post-merge checklist (#109)

Run by the dev, once, after the Azure-removal work is merged. The agent only
writes it. The Azure subscription is already deleted, so nothing is torn down in
Azure: this removes the leftovers in GitHub and on the dev machine.

```pwsh
# 1. Confirm the app secrets are in the password manager BEFORE deleting the repo
#    copies: LIFELEDGER_PASSWORD_HASH and LIFELEDGER_SESSION_KEY (step 2), plus the
#    tunnel token, rclone.conf, OAuth client ID/secret and ping URL (7d).
Read-Host "Every secret is in the password manager? Press Enter to continue"

# 2. Delete the obsolete repo secrets (leave `LLM_*` and `GH_AW_*` alone).
$Repo = "emepetres/life-ledger"
foreach ($Name in "AZURE_CLIENT_ID", "AZURE_TENANT_ID", "AZURE_SUBSCRIPTION_ID",
                  "AZURE_CI_PRINCIPAL_ID", "LIFELEDGER_PASSWORD_HASH",
                  "LIFELEDGER_SESSION_KEY") {
    gh secret delete $Name --repo $Repo
}

# 3. Delete the obsolete repo variables.
foreach ($Name in "AZURE_RESOURCE_GROUP", "AZURE_LOCATION") {
    gh variable delete $Name --repo $Repo
}

# 4. Check what is left: only `LLM_*` and `GH_AW_*` should remain.
gh secret list --repo $Repo
gh variable list --repo $Repo

# 5. Clean the local .env: keep only the keys in .env.example. Anything listed
#    with "=>" is in .env but not in the template (e.g. SUBSCRIPTION_ID,
#    RESOURCE_GROUP, LOCATION, REPO, APP_REG_NAME): delete those lines.
$Keys = { param($Path) Get-Content $Path | Where-Object { $_ -match '^[A-Z_]+=' } | ForEach-Object { $_.Split('=')[0] } }
Compare-Object (& $Keys .env.example) (& $Keys .env)
```

- [ ] App secrets confirmed in the password manager.
- [ ] The six repo secrets and two variables are deleted; `LLM_*` and `GH_AW_*` untouched.
- [ ] Local `.env` has only the keys listed in `.env.example`.
