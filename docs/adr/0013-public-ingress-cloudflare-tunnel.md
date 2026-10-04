# Public ingress via Cloudflare Tunnel at `ledger.carnero.net`

**Status:** accepted. Supersedes [ADR-0004](0004-access-model-auth.md)'s *HTTPS*
row (TLS termination by the Container Apps platform); the rest of ADR-0004 —
in-app shared-password auth, cookie flags, rate limit — still holds. Decided in
[#101](https://github.com/emepetres/life-ledger/issues/101).

On the home mini-PC ([ADR-0010](0010-host-home-mini-pc.md),
[ADR-0011](0011-runtime-lxc-static-binary.md)) Life Ledger is reached over public
HTTPS at **`https://ledger.carnero.net`** through a **Cloudflare Tunnel**: an
outbound-only connection from the `life-ledger` guest to Cloudflare's edge. No
router port is opened and the home IP is never published.

## Decisions

- **Cloudflare Tunnel, not port-forward.** It honours ADR-0010's strong preference
  for no inbound router port, and needs no DDNS or certificate management.
- **TLS terminates at Cloudflare's edge.** Cloudflare sees the plaintext ledger
  traffic. Accepted: one user, behind ADR-0004's password; the alternative that
  avoids it costs an open port and an exposed home IP.
- **Hostname `ledger.carnero.net`.** The `carnero.net` zone is already on
  Cloudflare DNS, so nothing is bought or moved; the registrar (GoDaddy) is
  irrelevant to the tunnel. The name is not obscured — certificate-transparency
  logs would reveal it anyway, and auth is the real protection.
- **`cloudflared` runs inside the `life-ledger` guest** as its own systemd unit.
  The app binds `LIFELEDGER_ADDR=127.0.0.1:8080`, so it is never on the network,
  and the Proxmox firewall has **no inbound rules**. The guest is the whole unit:
  move it and the ingress moves with it. `LIFELEDGER_SECURE_COOKIE=true`, since the
  browser always sees HTTPS.
- **Remotely managed tunnel.** The tunnel and its single
  `ledger.carnero.net → http://127.0.0.1:8080` rule live in the Cloudflare
  dashboard; the box holds one **tunnel token** in the root-only env file, with its
  canonical copy in the password manager. The runbook records the dashboard
  settings.
- **`cloudflared` patches itself** via Cloudflare's apt repo and
  `unattended-upgrades`: it is internet-facing, and Cloudflare supports a release
  for only about a year.
- **HTTP→HTTPS** comes from the zone's *Always Use HTTPS* setting — no app redirect
  code, as before.
- **No Cloudflare code in the app.** The login rate limit keeps keying on the
  leftmost `X-Forwarded-For`, which Cloudflare populates; no `CF-Connecting-IP`
  handling, per ADR-0010's portability invariant.

## Considered options

- **Port-forward 443 + DDNS + Caddy/Let's Encrypt.** TLS ends on the box and no
  third party sees traffic, but it opens an inbound port, publishes the home IP,
  and adds DDNS and ACME to operate. Rejected.
- **`cloudflared` in a separate LXC or on the Proxmox host.** Rejected: a separate
  guest needs a LAN rule into the app guest, against ADR-0011's deny-LAN firewall;
  the host mixes the hypervisor into the app's exposure.
- **Locally managed tunnel** (`config.yml` + credentials JSON in `deploy/minipc/`).
  More as-code, but creation needs the account-wide `cert.pem` and leaves two
  artifacts to protect, for a single rule. Rejected.
- **Cloudflare Access in front of the app.** A free second gate (email OTP or
  Google login) before the password. Deferred, not rejected: it is the ready-made
  upgrade if the login ever sees real attack traffic.

## Consequences

- The old `*.azurecontainerapps.io` URL dies with the Azure teardown; there is no
  redirect. The app has no PWA manifest, so the only breakage is a bookmark or
  home-screen shortcut, re-added once.
- Ingress depends on Cloudflare. Leaving it means a new ingress decision, but no
  app change.
