# Host Life Ledger on the home mini-PC

**Status:** accepted — supersedes [ADR-0005](0005-deploy-azure-cicd.md) (deploy to
Azure Container Apps) in full, and with it [ADR-0006](0006-user-assigned-identity-for-acr-pull.md)
(user-assigned identity for ACR pull), which has no subject once ACR and Azure
identities are gone. [ADR-0003](0003-persistence-and-storage.md) and
[ADR-0007](0007-scheduled-retained-backup.md) are **not** superseded here — their
Azure-specific storage and backup mechanics are re-decided by the durability and
retained-backup work that follows this decision.

Life Ledger leaves Azure (the company subscription is being closed) and moves to
the **home mini-PC** — a Proxmox box that is already permanently on. It costs €0
extra, and it is the only candidate whose storage was **proven by measurement**
rather than by documentation: real ext4, POSIX byte-range locks that conflict
across processes, a consistent `VACUUM INTO`, and every acknowledged SQLite commit
surviving an armed unclean power cut, both on the host and in an unprivileged LXC
([#96](https://github.com/emepetres/life-ledger/issues/96)). It has a routable
public IPv4 (not CGNAT) and far more compute than the app needs
([#94](https://github.com/emepetres/life-ledger/issues/94)). Decided in
[#95](https://github.com/emepetres/life-ledger/issues/95).

## Considered options

Shortlist from [#91](https://github.com/emepetres/life-ledger/issues/91):

- **Fly.io** (~$2.20/mo, managed, local NVMe) — the strongest cloud option, but its
  volumes are **unreplicated**, so it still needs an offsite backup and is no more
  durable than home; it costs money and was never probed for POSIX locks.
- **Oracle Always Free** (€0, EU, real block volume) — idle-reclaims instances under
  20% CPU *and* network over 7 days, which describes this app exactly; the free
  allowance was also halved silently in 2026-08.
- **Railway** ($5/mo) — at the top of the budget, storage medium undocumented.

Disqualified outright: Cloud Run, Cloudflare, Render, Koyeb, Vercel (no always-on
process or no POSIX block device — [#97](https://github.com/emepetres/life-ledger/issues/97));
Hetzner (over budget).

## Consequences

- **The dev is the on-call operator.** This was taken against a stated preference
  for no home maintenance, knowingly. The burden: patching the Proxmox host and the
  guest; hardware failure with no spare (single drive, no mirror, no host backup);
  1–2 unclean power cuts a year with no UPS; home ISP/router outages; and losing the
  public IPv4 on a house or ISP change.
- **Availability is best-effort.** Outages of hours to a day are acceptable for a
  one-user app. The app must come back on its own after a power cut (auto-start on
  boot) — no manual step.
- **Near-zero data loss (RPO).** _Amended by [ADR-0014](0014-nightly-offsite-snapshot-to-google-drive.md): the RPO is 24 hours, and the offsite copy is any rclone remote, not S3-shaped._ The offsite copy is the *only* surviving copy if the
  drive dies, so every committed write must reach offsite storage — a nightly
  snapshot alone is not enough. With a handful of writes a day this is cheap. *How*
  is left to the durability design.
- **Home network exposure.** A public endpoint at home is accepted. Strong
  preference — not a hard rule — that **no inbound port is opened on the router**
  (an outbound tunnel); the ingress decision makes the final call.
- **Portability invariant.** The app stays a host-neutral container image on public
  GHCR, its offsite backup stays S3-shaped, and no Proxmox-specific code enters the
  app. The next migration should be "restore the snapshot elsewhere, repoint DNS".
  Likely triggers: hardware death, losing the public IPv4, moving house. No
  successor host is pre-chosen — that is decided when the move is needed, and any
  candidate must pass the [#96](https://github.com/emepetres/life-ledger/issues/96)
  storage probe first.
- **What ADR-0005 decided that is re-decided elsewhere:** the CI/CD pipeline (the
  `git diff` deploy gate, `:latest` + `:sha` tags, rollback), secrets handling, and
  TLS/DNS. Still holding from ADR-0005: the single-writer invariant (exactly one app
  instance), and the embedded tzdata with `TZ=Europe/Madrid`.
