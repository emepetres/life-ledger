# Run the static binary in an unprivileged LXC, pulled from the GHCR image

**Status:** accepted. Refines [ADR-0010](0010-host-home-mini-pc.md) (host on the
home mini-PC). Decided in [#98](https://github.com/emepetres/life-ledger/issues/98).

On the mini-PC, Life Ledger runs as a **plain systemd service in a fresh
unprivileged Debian 13 LXC** (`life-ledger`). There is **no container runtime**.
The public GHCR image is still the build artifact. A systemd timer in the guest
polls `:latest` by digest every 5 minutes. When the digest changes, `crane`
extracts `/life-ledger` from the image, the timer swaps it in and restarts the
unit, and if `/health` fails it rolls back to the previous binary.

## Considered options

- **Docker or Podman inside the LXC.** Runs the image as-is, but Proxmox does not
  support it, and AppArmor/runc regressions have broken it on host upgrades.
  The dev is the only operator, so that fragility is a recurring cost.
- **Proxmox native OCI→LXC** (tech preview). Every update means destroying and
  recreating the guest, which needs Proxmox-specific update tooling.
- **VM + Docker.** This is the supported way to run Docker on Proxmox and gives the
  strongest isolation. It costs ~0.5–1 GB RAM and a second OS to patch, and the
  [#96](https://github.com/emepetres/life-ledger/issues/96) storage probe was not
  run on a VM disk.

We chose the binary in an LXC because it has the fewest moving parts and its
storage path is the one #96 measured. A static Go binary with embedded assets and
tzdata does not need a container runtime to be portable.

## Consequences

- **The process runs the binary, not the image**, so it is not byte-identical to
  the image CI tested. The guest provides what distroless gave: CA certificates, a
  non-root user (`DynamicUser`), and a read-only root (`ProtectSystem=strict`).
  Do not "fix" this back to Docker without revisiting the reasons above.
- **The image stays the portable artifact.** The next host runs the GHCR image
  directly. Nothing Proxmox-specific enters the app.
- **Secrets** live in a root-only env file in the guest, which is a deployment
  copy. The canonical copy of every secret is in the dev's password manager. The
  backup decryption key must never exist only on the box.
- **Network.** The Proxmox firewall gives the guest outbound internet access plus
  DNS and the gateway, and denies the rest of the LAN, including HA VM 100. Inbound
  is allowed only from wherever the ingress terminates.
- **Auto-start chain:** BIOS restores power → Proxmox → guest `onboot=1`,
  `startup: order=2` (after HA) → unit `Restart=always`. After the first
  deployment, test it with a real power cut.
- Ops files (script, units, firewall) live in `deploy/minipc/`, with a runbook in
  `docs/deployment/`. Commands that run on the mini-PC are bash.
