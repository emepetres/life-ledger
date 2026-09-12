# GHCR as the registry: free-tier terms, pull auth per host class, and the redeploy trigger

Research for [issue #93](https://github.com/emepetres/life-ledger/issues/93), under the wayfinder map
[#90 (Leave Azure: a new home for Life Ledger)](https://github.com/emepetres/life-ledger/issues/90).
**Advisory** input to the host-selection and deploy-pipeline decisions: this document establishes what
GHCR actually costs, how each class of host authenticates a pull, and what can trigger a redeploy on a
host with no control plane. It makes no host choice.

Facts current as of **2026-09-12**; every claim is cited in [Sources](#sources). Shell in this document is
PowerShell Core per [AGENTS.md](../../AGENTS.md), except inside workflow `run:` blocks, which stay bash to
match `ci-cd.yml`.

> **Staleness flag.** Registry pricing and pull-rate policy are the two fastest-moving facts here. Docker
> Hub re-priced its pull limits more than once during 2025; GitHub's "container storage and bandwidth is
> currently free" wording is explicitly provisional (*"currently"* is GitHub's word). Both pages were
> re-read on **2026-09-12** and both must be re-read before the cutover ticket commits to either.
> Everything about Fly.io's private-registry support is a **negative** finding (absence from the docs),
> which is the weakest kind of claim — it is called out as such below.

## Verdict in one paragraph

**GHCR is viable, and for this app it is effectively free — but the decision that matters is public vs
private, not GHCR vs not.** GitHub's billing page says container storage and bandwidth on the Container
registry is *currently* free regardless of visibility; the *durable, unconditional* guarantee is the one
for public packages ("GitHub Packages usage is free for public packages"). On the metered path a
**private** package eats the GitHub Free plan's **500 MB storage / 1 GB monthly data transfer**, shared
with Actions artifacts — the distroless image here is tens of megabytes, so even the metered numbers are
not a real constraint at this scale. The real friction is not cost, it is **pull authentication**: GHCR
has **no OIDC/keyless pull path** for a third party. Every non-GitHub consumer authenticates with a
**long-lived classic PAT holding `read:packages`** — account-wide, manually rotated, sitting in a file on
the host. Making the package **public** removes that credential entirely (anonymous pull), which is the
single highest-leverage choice in this ticket, and is what preserves
[ADR-0006](../adr/0006-user-assigned-identity-for-acr-pull.md)'s no-stored-registry-credential property
once the managed identity dies. Push from Actions needs **no PAT**: the built-in `GITHUB_TOKEN` with
`permissions: packages: write` suffices.

## Answers in one table

| Ticket question | Answer |
| --- | --- |
| Free-tier storage / transfer, personal account | GitHub Free: **500 MB storage, 1 GB/month transfer**, *shared with Actions artifacts*. But also: *"Container image storage and bandwidth for the Container registry is currently free."* |
| Public packages | **Free, unmetered**, and **anonymously pullable** — no credential at all. |
| Does it count against Actions **minutes**? | **No.** Packages is a separate metered product; it shares the *storage* pool with Actions **artifacts**, never the minutes pool. |
| Pulls performed **by Actions** | Free — transfer via `GITHUB_TOKEN` *"does not count against the usage for the hosting repository"*. |
| Automatic retention / pruning | **None, and none configurable.** Untagged versions accumulate until deleted via UI / REST / GraphQL. 30-day restore window after deletion. |
| Push from Actions | **`GITHUB_TOKEN` suffices.** `permissions: { contents: read, packages: write }`. No PAT. |
| Pull auth, PaaS | **Classic PAT with `read:packages`**, stored as a registry secret. **No OIDC path exists.** Cloud Run cannot use a private GHCR image directly at all. |
| Pull auth, VPS / mini-PC | `docker login ghcr.io` with a classic `read:packages` PAT, persisted in `~/.docker/config.json` **base64-encoded, not encrypted**. Rotation entirely manual. |
| Best redeploy trigger, self-hosted, no inbound port | **Actions job SSH-ing in** (reuses SSH, synchronous feedback) or a **systemd timer polling the digest** (zero inbound). Watchtower polling is the low-effort middle. Webhook receivers need a new inbound port — avoid. |

---

## 1. GHCR free-tier terms

### 1.1 The numbers

GitHub's billing page carries two statements that must be read together.

The metered table:

| Plan | Storage | Data transfer (out) per month |
| --- | --- | --- |
| GitHub Free | 500 MB | 1 GB |
| GitHub Pro | 2 GB | 10 GB |
| GitHub Free for organizations | 500 MB | 1 GB |
| GitHub Team | 2 GB | 10 GB |
| GitHub Enterprise Cloud | 50 GB | 100 GB |

with the caveat that *"The storage amounts shown are shared with GitHub Actions artifacts"* — so the
500 MB is a **combined** Packages + Actions-artifacts pool, **not** a dedicated registry allowance. This is
the one number that could bite: an artifact-retention habit elsewhere in the repo consumes the same 500 MB
the images would.

And, overriding it for containers specifically:

> "Container image storage and bandwidth for the Container registry is currently free."

The word **"currently"** is GitHub's. Treat the metered table as the fallback for the day that sentence
disappears from the page.

### 1.2 Public vs private

- **Public packages are free, full stop**: *"GitHub Packages usage is free for public packages. In
  addition, data transferred in from any source is free."* Push (transfer **in**) is always free, for any
  visibility.
- **Private packages** accrue against the owner's storage allowance, billed on hourly usage. *"If your
  account does not have a valid payment method on file, usage is blocked once you use up your quota"* —
  so the failure mode is a **hard stop**, not a surprise bill. For a deployment registry, a hard stop
  means the host cannot start a new container.
- **Public packages allow anonymous pull**: *"In the Container registry, public packages allow anonymous
  access and can be pulled without authentication or signing in via the CLI."* This is the finding that
  makes every host-class row in §2 trivial.

**No pull-rate limit for `ghcr.io` is documented on docs.github.com** — neither anonymous nor
authenticated. GitHub's published rate limits cover the REST API, GitHub Apps, and (per the 2025-05-08
changelog) unauthenticated HTTPS clone / `raw.githubusercontent.com`; the registry appears in none of
them. **This is an absence of documentation, not a documented absence of limits** — GHCR is widely
reported to apply undocumented abuse throttles, and that reporting is exactly the kind of secondary source
this document does not rely on. At this app's pull volume (a handful of deploys a day at most) the
question is academic; do not design a fleet around it.

### 1.3 Does it consume Actions minutes?

**No.** Packages billing is its own metered product with its own storage and data-transfer quota. The only
overlap with Actions is the **storage** pool (shared with Actions *artifacts*), never the compute-minutes
pool. And pulls performed *by* a workflow using `GITHUB_TOKEN` are explicitly exempt from transfer
metering: *"the data transfer does not count against the usage for the hosting repository."*

### 1.4 Retention and expiry

**There is no automatic pruning and no configurable retention policy for GitHub Packages.** GitHub's
"Deleting and restoring a package" page documents only manual and programmatic deletion — web UI, REST
API, and (for some registries) GraphQL — and describes no lifecycle, expiry, or untagged-cleanup setting
anywhere. The contrast is instructive: Actions artifacts and logs *do* have a documented, configurable
retention period, and packages conspicuously do not.

Consequences for a SHA-tagged pipeline like the present `ci-cd.yml`:

- Every push to main creates a **new, permanent version**. Moving `:latest` orphans the previous version
  as **untagged**, and untagged versions **stay forever** unless something deletes them.
- If the metered path ever applies (private package, or the "currently free" sentence goes away), this is
  an unbounded slow leak against a 500 MB pool shared with Actions artifacts.
- The mitigation is a scheduled cleanup workflow calling the REST packages API (or the community
  `actions/delete-package-versions` action) to delete untagged versions older than N days. Budget this as
  **a small workflow the deploy-pipeline ticket must write**, not as something the registry does for you.
- Deletion is recoverable for **30 days**, and only while the namespace has not been reclaimed by another
  package.

### 1.5 Pushing from GitHub Actions

**The built-in `GITHUB_TOKEN` is sufficient. No PAT is needed**, provided the package is owned by — or
linked to — the workflow's own repository.

The documented minimum permissions block and login step:

```yaml
permissions:
  contents: read
  packages: write
  # Only if also publishing build-provenance attestations:
  # attestations: write
  # id-token: write

steps:
  - name: Log in to the Container registry
    uses: docker/login-action@v3
    with:
      registry: ghcr.io
      username: ${{ github.actor }}
      password: ${{ secrets.GITHUB_TOKEN }}
```

GitHub: *"You can use the automatically-generated `GITHUB_TOKEN` secret for the password"*, and *"you can
use the `GITHUB_TOKEN` to publish, install, delete, and restore packages in GitHub Packages without
needing to store and manage a personal access token."*

Two documented traps:

1. **The namespace trap.** *"The `GITHUB_TOKEN` will not have permission to push the package if you have
   previously pushed a package to the same namespace, but have not connected the package to the
   repository."* If a human ever pushes `ghcr.io/emepetres/life-ledger` by hand with a PAT first, the
   workflow token can find itself locked out afterwards.
2. **The fix is a Dockerfile label.** GitHub's own recommendation is to add
   `LABEL org.opencontainers.image.source=https://github.com/emepetres/life-ledger` to the `Dockerfile`,
   which auto-links the package to the repo so `GITHUB_TOKEN` inherits admin on it. **This repo's
   `Dockerfile` carries no such label today** — adding it is a one-line prerequisite for the pipeline
   rewrite.

Note that `packages: write` is *not* in the default `GITHUB_TOKEN` permission set, so the block above is
mandatory; and because declaring any `permissions:` key drops every other permission to `none`,
`contents: read` must be listed explicitly alongside it. The existing `deploy` job in `ci-cd.yml` already
follows exactly this pattern with `id-token: write` + `contents: read`.

### 1.6 The credential that pulls: what GHCR will and will not accept

This is the finding that shapes everything downstream.

- **GHCR accepts only classic PATs** for non-Actions authentication: *"GitHub Packages only supports
  authentication using a personal access token (classic)."* Fine-grained PATs — generally available since
  2025-03-18 — still list *"Using fine-grained personal access token to access Packages"* as a **known
  gap**. The modern, repo-scoped, mandatory-expiry token type therefore **cannot** be used here.
- Scopes: `read:packages` to pull, `write:packages` to push, `delete:packages` to delete. A classic PAT's
  scopes are **account-wide** — a `read:packages` PAT can read *every* package the account can read, not
  merely this one. There is no per-package scoping for classic PATs.
- **There is no OIDC or keyless pull path into GHCR.** GitHub's OIDC story (`id-token: write`) is about a
  *workflow* proving its identity **to a cloud** — exactly what `ci-cd.yml` does today against Azure. It
  is not a mechanism by which a *third-party host* proves its identity **to GHCR**. Nothing in the
  Packages documentation offers a federated, short-lived, or workload-identity credential for a pull.
- Classic PATs may be created without an expiry date; GitHub *"automatically removes personal access
  tokens that haven't been used in a year"* — which is a dead-token sweep, not a rotation policy. **A PAT
  used every day never expires on its own.** Rotation is entirely manual.

**Therefore:** any private-GHCR deployment ends with a long-lived, account-wide, manually-rotated secret
living in a file on the host. That is a strict security *regression* against the managed identity in
[ADR-0006](../adr/0006-user-assigned-identity-for-acr-pull.md), which exists precisely because storing a
registry credential was rejected there ("a security regression"). **A public package eliminates the
credential rather than managing it**, and is the recommended way to carry ADR-0006's intent forward.

---

## 2. Pull authentication, per host class

### 2.1 A PaaS that pulls an image

**Koyeb — documented, clean, PAT-based.** Koyeb explicitly supports `ghcr.io` (*"we support GitHub
Container Registry and not the older GitHub Packages Docker registry"*). A private image needs a registry
secret in Docker `auths` form:

```pwsh
'{ "auths": { "ghcr.io": { "username": "<GITHUB_USERNAME>", "password": "<CLASSIC_PAT>" } } }' |
  koyeb secrets create gh-registry-credentials --value-from-stdin

koyeb app init life-ledger `
  --docker "ghcr.io/emepetres/life-ledger" `
  --ports 8080:http --routes /:8080 `
  --docker-private-registry-secret gh-registry-credentials
```

Koyeb's own instruction on scope: a PAT with `write:packages` if you also build and push, **`read:packages`
if you are only deploying**. Least privilege is available and documented. Rotation means recreating the
secret and redeploying the service.

**Cloud Run — the awkward one.** Google is explicit: *"You can directly use container images stored in
Artifact Registry, or public images from Docker Hub or GitHub Container Registry."* A **public** GHCR
image therefore deploys with zero credentials — but note *"Public images from GitHub Container Registry
and Docker Hub images are cached for up to one hour"*, which makes a `:latest`-based redeploy unreliable
and a SHA tag mandatory. A **private** GHCR image **cannot be deployed directly at all**: *"You can use
container images from other public or private registries … or private images from GitHub Container
Registry, by setting up an Artifact Registry remote repository."* That means provisioning an Artifact
Registry remote repo that proxies GHCR with stored upstream credentials — an extra GCP resource, an extra
credential store, and a second cache layer between CI and production. Cloud Run + private GHCR is strictly
worse than Cloud Run + Artifact Registry directly (§4.2).

**Fly.io — no documented support for private third-party registries.** Fly's blueprints cover exactly two
cases: **its own registry** (`fly auth docker` → `docker push registry.fly.io/<app>:<tag>` →
`fly deploy --image registry.fly.io/<app>:<tag>`) and **public** registries via `fly.toml`'s
`[build] image = "..."`. Nothing in the Fly documentation describes supplying credentials for an external
private registry.

> **Flagged as a negative finding.** This is an *absence from Fly's documentation*, checked 2026-09-12 —
> not an explicit "unsupported" statement from Fly, and the strongest corroboration available is
> long-standing community feature requests, which are not primary sources. Verify with Fly before ruling
> them out on this basis.

Practical consequences for Fly: a **public** GHCR image works via `[build] image`; a **private** one would
require CI to re-push the built image into `registry.fly.io` (build once, push twice). That still
preserves the map's "the exact artifact that passed tests is the one that runs" property, since the
re-push is a byte-identical copy — at the cost of a `FLY_API_TOKEN` in Actions and a second registry to
reason about.

### 2.2 A plain VPS or the home mini-PC

Identical everywhere, and unglamorous:

```pwsh
$Env:CR_PAT | docker login ghcr.io -u emepetres --password-stdin
docker pull ghcr.io/emepetres/life-ledger:<sha>
```

with `CR_PAT` a **classic PAT holding `read:packages`**. Three things to know:

1. **`docker login` persists the credential to `~/.docker/config.json` as base64 — encoding, not
   encryption.** Anyone with read access to that file (a root-equivalent process, a stray backup of the
   home directory) has the token, and the token is account-wide. On a mini-PC at home that file is the
   whole attack surface of the registry credential.
2. **Rotation is manual with no forcing function.** A daily-used classic PAT never expires. Rotating means
   create a new PAT → `docker login` again on every host → revoke the old one. If the same PAT also sits
   in a PaaS secret and a `systemd` unit, that is three places to update in lockstep, and nothing will
   remind you.
3. **A public package removes all of the above.** `docker pull ghcr.io/emepetres/life-ledger:<sha>` with
   no `docker login` at all: no secret on the mini-PC, nothing to rotate, nothing to leak.

---

## 3. Redeploy trigger for a self-hosted target with no control plane

Four mechanisms, ranked by security posture. The decisive axis is **does it require a new inbound port**.

### 3.1 An Actions job SSH-ing in — recommended for a reachable VPS

After pushing the image, CI SSHes to the host and runs `docker pull … && docker compose up -d` (or a
`systemctl restart`).

- **Inbound:** SSH only — already open on any managed host, already key-only, already hardened. **No new
  port.**
- **Trust direction:** GitHub → host. A deploy private key lives in Actions secrets, so scope the blast
  radius: a dedicated `deploy` user, a `command=` restriction in `authorized_keys`, no sudo beyond the one
  restart.
- **Latency:** immediate, and **synchronous** — the workflow fails if the deploy fails. None of the
  polling options give you that, and it is the single biggest operational advantage on this list.
- **Cost:** the host must be reachable from GitHub's runner IPs. A home mini-PC behind CGNAT or a dynamic
  IP makes this the *hardest* option, not the easiest — which is precisely the case where §3.2 wins.

### 3.2 A systemd timer polling the manifest digest — recommended for the home mini-PC

A `.timer` + `.service` pair that periodically compares the registry's manifest digest to the running
container's image digest and restarts only when it moves:

```pwsh
# The check, conceptually — no pull happens unless the digest changed.
$remote = docker buildx imagetools inspect ghcr.io/emepetres/life-ledger:latest --format '{{.Manifest}}'
```

`docker buildx imagetools inspect` *"[s]how[s] details of an image in the registry"* and returns the
manifest digest **without pulling the image**, so the poll costs a single HEAD-ish registry round trip —
cheap in both bandwidth and metered transfer.

- **Inbound:** **none, ever.** Purely outbound HTTPS. This is the only option here with a zero-inbound
  guarantee, which is what makes it the natural fit for a home network where every inbound port is a hole
  punched through a router.
- **Trust direction:** host → GHCR only. With a public package, **no credential at all**.
- **Latency:** one poll interval.
- **Cost:** you write and maintain the unit files, and failures are silent unless you wire up an
  `OnFailure=` notification. No deploy feedback ever reaches the PR.

### 3.3 Watchtower — the low-effort middle

Watchtower runs as a container, watches the others, and pulls/recreates on a new image.

- **Polling mode** (`--interval` / `WATCHTOWER_POLL_INTERVAL`) — **default 86400 s = 24 h**, far too slow
  out of the box, so set it explicitly. **No inbound port.** Same posture as §3.2 with less code to write.
- **HTTP API mode** (`--http-api-update`) *"[r]uns Watchtower in HTTP API mode, only allowing image
  updates to be triggered by an HTTP request"*, on **port 8080**, endpoint **`/v1/update`**, authenticated
  by a bearer token from `WATCHTOWER_HTTP_API_TOKEN`. **This requires an inbound port** and is §3.4 in
  disguise.
- **Private-registry auth:** mount a Docker `config.json` into the container
  (`-v <PATH>/config.json:/config.json`), or point `credsStore` / `credHelpers` at a helper container. So
  the PAT ends up in yet another file, in yet another place to rotate.
- **The security cost that dominates all of the above:** Watchtower requires
  `-v /var/run/docker.sock:/var/run/docker.sock`. **Docker-socket access is root-equivalent on the host.**
  You are running a long-lived, internet-talking container with root-equivalent privilege in order to
  avoid writing a systemd timer. For a single-container app that trade is hard to justify; Watchtower
  earns its keep when there are many containers to keep current.

### 3.4 A webhook receiver — not recommended here

A small HTTP service on the host that GitHub (or the workflow) calls to trigger the pull.

- **Inbound:** a **new internet-reachable listening port** whose only job is to run privileged commands.
  It must terminate TLS, verify the HMAC signature on every request, be rate-limited, and be patched.
  GitHub's webhook source IPs are published and can be firewalled, but that is one more moving part to
  keep current.
- **Trust direction:** internet → host, which is the direction the rest of this project spends its effort
  avoiding.
- It buys **immediacy** over §3.2 and **no-GitHub-held-SSH-key** over §3.1 — real, but narrow.
- If the host already fronts the app with a reverse proxy terminating TLS (it will, for the app itself),
  the marginal cost is smaller than it looks. It is still strictly more attack surface than either
  recommended option, for a benefit §3.1 already delivers with feedback attached.

### 3.5 Summary

| Mechanism | New inbound port | Credential held where | Latency | Deploy feedback in CI | Verdict |
| --- | --- | --- | --- | --- | --- |
| Actions SSH | No (reuses SSH) | SSH key in Actions secrets | Immediate | **Yes, synchronous** | **Best for a reachable VPS** |
| systemd timer + digest poll | **None** | Nothing, if the package is public | One interval | No | **Best for the home mini-PC** |
| Watchtower (poll) | None | `config.json` on host | One interval | No | OK, but root-equivalent socket |
| Watchtower (HTTP API) | **Yes, 8080** | `config.json` + API token | Immediate | No | Worst of both |
| Webhook receiver | **Yes** | HMAC secret | Immediate | No | Avoid unless already proxying |

---

## 4. Fallbacks if GHCR disappoints

### 4.1 Docker Hub free tier

Usable, with a pull-rate ceiling GHCR does not (documentably) have:

| Tier | Pull limit |
| --- | --- |
| Unauthenticated | **100 per 6 hours**, per IPv4 address or IPv6 /64 subnet |
| Personal (authenticated, free) | **200 per 6 hours** |
| Pro / Team / Business | Unlimited |

Docker also *"reserves the right to throttle or charge accounts with excessive data and storage
consumption."* For a one-app, few-deploys-a-day workload these ceilings are not binding — **but the
unauthenticated limit is shared across an entire NAT address or IPv6 /64**, which matters on a home
connection or a shared cloud egress, where the budget is spent by everything else on the link.
Authenticating with a free Personal account doubles the limit and makes it per-account rather than per-IP;
the credential story is then a Docker Hub access token, no better or worse than a GHCR PAT. Docker Hub's
advantage over GHCR is that **every** host class treats it as a first-class registry; its disadvantages
are the documented rate limit and a second account/credential outside the GitHub perimeter this repo
already trusts.

### 4.2 Each host's own registry

- **Fly.io — `registry.fly.io`.** Auth via `fly auth docker`, which writes a Fly token into
  `~/.docker/config.json`; push `registry.fly.io/<app>:<tag>`, deploy `fly deploy --image …`. Access is
  **scoped per organization**, so one image can serve several apps. In CI this means one `FLY_API_TOKEN`
  and no GHCR credential anywhere. Given the private-third-party gap in §2.1, this is the *natural* shape
  for Fly rather than a fallback.
- **Cloud Run — Artifact Registry.** Google's recommended and only first-class path. CI would authenticate
  with Workload Identity Federation from Actions — the direct analogue of the Azure OIDC login `ci-cd.yml`
  performs today: **keyless, no stored registry credential**. That is the only arrangement on this page
  that fully preserves ADR-0006's property *without* making the image public. The price is a GCP project
  and its own free-tier terms.
- **Koyeb** ships no first-party registry; it consumes external ones, so GHCR (or Docker Hub) is the answer
  there regardless.

---

## 5. What this implies for the pipeline rewrite

Not decisions — inputs to the deploy-pipeline ticket:

1. **Publish the package public.** It converts every "store and rotate a `read:packages` PAT" row above
   into "no credential", unblocks Cloud Run's and Fly's documented paths, and preserves the
   no-stored-registry-credential property ADR-0006 was written to protect. The image is a static Go binary
   built from a public repo, with secrets supplied as env vars at runtime — it leaks nothing.
2. **Add `LABEL org.opencontainers.image.source` to the `Dockerfile`** so `GITHUB_TOKEN` reliably owns the
   package (§1.5, trap 2).
3. **Keep the SHA tag as the deployed reference.** `:latest` is a convenience for the polling triggers
   (§3.2/§3.3) and is subject to Cloud Run's one-hour public-image cache; the immutable SHA tag is what
   makes a rollback a retag rather than a rebuild — the property the map asks for.
4. **Write an untagged-version cleanup workflow.** Nothing prunes for you (§1.4).
5. **The `deploy` job's `permissions:` block becomes `contents: read` + `packages: write`**, and the whole
   `azure/login@v2` OIDC step plus the ACR discovery/login steps disappear with the subscription —
   replaced by whichever §3 mechanism the chosen host implies.

---

## Sources

All URLs checked **2026-09-12**.

**GitHub Packages — billing, quotas, visibility**

- GitHub Packages billing — the 500 MB / 1 GB Free-plan row, *"shared with GitHub Actions artifacts"*,
  *"Container image storage and bandwidth for the Container registry is currently free"*, *"GitHub
  Packages usage is free for public packages"*, *"data transferred in from any source is free"*, the
  `GITHUB_TOKEN`-download exemption, and the block-on-quota-exhaustion behaviour:
  <https://docs.github.com/en/billing/concepts/product-billing/github-packages>
- About permissions for GitHub Packages — *"public packages allow anonymous access and can be pulled
  without authentication"*, `GITHUB_TOKEN` publish/install/delete/restore, repository-scoped packages
  inheriting repository permissions:
  <https://docs.github.com/en/packages/learn-github-packages/about-permissions-for-github-packages>
- Configuring a package's access control and visibility:
  <https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility>

**GitHub Packages — authentication and the Container registry**

- Working with the Container registry — *"GitHub Packages only supports authentication using a personal
  access token (classic)"*, the `read:packages` / `write:packages` / `delete:packages` scope definitions,
  anonymous access to public images, and the `org.opencontainers.image.source` label recommendation:
  <https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry>
- Publishing and installing a package with GitHub Actions — the `GITHUB_TOKEN` namespace trap (*"will not
  have permission to push the package if you have previously pushed a package to the same namespace"*):
  <https://docs.github.com/en/packages/managing-github-packages-using-github-actions-workflows/publishing-and-installing-a-package-with-github-actions>
- Publishing Docker images — the `permissions: { contents: read, packages: write, attestations: write,
  id-token: write }` block, the `docker/login-action` step with `${{ secrets.GITHUB_TOKEN }}`, and *"You
  can use the automatically-generated `GITHUB_TOKEN` secret for the password"*:
  <https://docs.github.com/en/actions/publishing-packages/publishing-docker-images>
- Controlling permissions for `GITHUB_TOKEN` — the default permission set, and that declaring any key
  drops the remainder to `none`:
  <https://docs.github.com/en/actions/writing-workflows/choosing-what-your-workflow-does/controlling-permissions-for-github_token>
- Managing your personal access tokens — classic-PAT expiry options, *"GitHub automatically removes
  personal access tokens that haven't been used in a year"*, and the listed fine-grained-PAT gap *"Using
  fine-grained personal access token to access Packages"*:
  <https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens>
- Fine-grained PATs are now generally available (2025-03-18) — GA status of the token type that still
  cannot reach Packages:
  <https://github.blog/changelog/2025-03-18-fine-grained-pats-are-now-generally-available/>
- Packages: Container registry now supports `GITHUB_TOKEN` (2021-03-24):
  <https://github.blog/changelog/2021-03-24-packages-container-registry-now-supports-github_token/>

**GitHub Packages — retention**

- Deleting and restoring a package — UI / REST / GraphQL deletion, the 30-day restore window and the
  namespace-reclaim caveat; **no automatic pruning and no retention policy documented anywhere on the
  page**: <https://docs.github.com/en/packages/learn-github-packages/deleting-and-restoring-a-package>
- REST API endpoints for packages — the delete-version endpoints a cleanup workflow would call:
  <https://docs.github.com/en/rest/packages/packages>
- (Contrast, showing what a documented retention setting looks like when GitHub offers one) Configuring
  the retention period for GitHub Actions artifacts and logs:
  <https://docs.github.com/en/organizations/managing-organization-settings/configuring-the-retention-period-for-github-actions-artifacts-and-logs-in-your-organization>
- Rate limits for the REST API — the published limits; the registry is **not** among them:
  <https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api>
- Updated rate limits for unauthenticated requests (2025-05-08) — covers clone / `raw.githubusercontent.com`,
  not `ghcr.io`:
  <https://github.blog/changelog/2025-05-08-updated-rate-limits-for-unauthenticated-requests/>

**Host: Koyeb**

- Private Container Registry Secrets — *"we support GitHub Container Registry and not the older GitHub
  Packages Docker registry"*, the `ghcr.io` `auths` JSON secret, and `write:packages` to push vs
  `read:packages` to deploy:
  <https://www.koyeb.com/docs/build-and-deploy/private-container-registry-secrets>
- Pre-Built Docker Images — the `--docker` / `--docker-private-registry-secret` deploy flags:
  <https://www.koyeb.com/docs/build-and-deploy/prebuilt-docker-images>

**Host: Google Cloud Run**

- Deploy container images to Cloud Run services — *"You can directly use container images stored in
  Artifact Registry, or public images from Docker Hub or GitHub Container Registry"*; private GHCR
  requires *"setting up an Artifact Registry remote repository"*; *"Public images from GitHub Container
  Registry and Docker Hub images are cached for up to one hour"*:
  <https://docs.cloud.google.com/run/docs/deploying>
- Deploying to Cloud Run from Artifact Registry:
  <https://docs.cloud.google.com/artifact-registry/docs/integrate-cloud-run>

**Host: Fly.io**

- Managing Docker Images with Fly.io's Private Registry — `fly auth docker`, pushing to
  `registry.fly.io/<app>:<tag>`, `fly deploy --image`, per-organization scoping, and public images via
  `fly.toml` `[build] image`; **no mention of private third-party registries**:
  <https://fly.io/docs/blueprints/using-the-fly-docker-registry/>
- Working with Docker on Fly.io — the same two documented cases and no third:
  <https://fly.io/docs/blueprints/working-with-docker/>
- `fly auth docker` reference: <https://fly.io/docs/flyctl/auth-docker/>

**Redeploy mechanisms**

- Watchtower arguments — `--interval` / `WATCHTOWER_POLL_INTERVAL` **default 86400**, the
  `--http-api-update` description, `--run-once`: <https://containrrr.dev/watchtower/arguments/>
- Watchtower HTTP API mode — **port 8080**, `/v1/update`, `WATCHTOWER_HTTP_API_TOKEN` bearer auth:
  <https://containrrr.dev/watchtower/http-api-mode/>
- Watchtower private registries — mounting `config.json`, `credsStore` / `credHelpers`:
  <https://containrrr.dev/watchtower/private-registries/>
- Watchtower quick start — the required `-v /var/run/docker.sock:/var/run/docker.sock`:
  <https://containrrr.dev/watchtower/>
- `docker buildx imagetools inspect` — *"Show details of an image in the registry"*, the `--format
  '{{.Manifest}}'` / `--raw` output, digest retrieved without pulling:
  <https://docs.docker.com/reference/cli/docker/buildx/imagetools/inspect/>
- `docker login` — credential storage in `~/.docker/config.json` and `--password-stdin`:
  <https://docs.docker.com/reference/cli/docker/login/>
- `systemd.timer` — `OnCalendar`, `OnUnitActiveSec`, `Persistent=`:
  <https://www.freedesktop.org/software/systemd/man/latest/systemd.timer.html>

**Fallback registry: Docker Hub**

- Docker Hub usage and limits — unauthenticated **100 per 6 hours per IPv4 address or IPv6 /64 subnet**,
  Personal **200 per 6 hours**, Pro/Team/Business unlimited, and the throttle-or-charge reservation:
  <https://docs.docker.com/docker-hub/usage/>
- Docker Hub pulls — the same limits on the pull-specific page:
  <https://docs.docker.com/docker-hub/usage/pulls/>

**This repo**

- `.github/workflows/ci-cd.yml` — the ACR build-and-push + `az containerapp update --image` pipeline being
  replaced, and the existing `permissions: { id-token: write, contents: read }` pattern.
- `Dockerfile` — the distroless static image; **no `org.opencontainers.image.source` label today**.
- [ADR-0006 — user-assigned identity to break the ACR-pull deadlock](../adr/0006-user-assigned-identity-for-acr-pull.md)
  — the no-stored-registry-credential property this research tries to carry forward; note its explicit
  rejection of "ACR admin username/password as a registry secret … Rejected as a security regression."
