# Deploy to Azure Container Apps via Bicep + GitHub Actions

Life Ledger is deployed to **Azure Container Apps** (ACA) in **West Europe**, provisioned by **Bicep** in `infra/` and shipped by **GitHub Actions**. Earlier ADRs settled the runtime shape (Go static binary on ACA, SQLite on a mounted Azure Files volume — [ADR-0003](0003-persistence-and-storage.md); in-app auth — [ADR-0004](0004-access-model-auth.md)) but explicitly deferred *deployment*. This ADR closes that gap. The end-to-end topology lives in [docs/architecture.md](../architecture.md); this records the decisions and the trade-offs behind them.

## Decisions

| Aspect | Decision |
| --- | --- |
| Compute | Azure Container Apps, West Europe, **min 1 / max 1** replicas (always warm). |
| Registry | **Azure Container Registry (Basic)**; the app pulls via its **system-assigned managed identity** (AcrPull), no stored pull credential. |
| Infra as code | All resources in **Bicep** (`infra/main.bicep`), applied by a **separate** workflow (`infra.yml`) on manual dispatch or changes under `infra/`. |
| App deploy | **One gated workflow** (`ci-cd.yml`): a `test` job on every PR and push-to-main; a `deploy` job that `needs: test` and runs only on push-to-main. |
| CI→Azure auth | **OIDC federated identity** (`azure/login@v2`) — no long-lived secret in GitHub. |
| Image tags | CD pushes `:latest` **and** `:<git-sha>`, then rolls the app onto the sha. Bicep pins `:latest`, so reapplying infra never reverts what CD deployed. |
| App secrets | `LIFELEDGER_PASSWORD_HASH` and `LIFELEDGER_SESSION_KEY` are GitHub Secrets, passed as `@secure()` Bicep params and stored as ACA secrets. |
| Timezone | Binary embeds tzdata (`import _ "time/tzdata"`); `TZ=Europe/Madrid` set in Bicep. |
| Rollback | ACA revisions. |

## Rationale / deviations worth recording

- **ACR Basic over GHCR, accepting ~$5/month.** The stack was chosen partly for a near-$0 cost (scale-to-zero free grant — see the stack research). ACR Basic and an always-warm replica consciously give that up in exchange for the cleanest Azure integration: managed-identity pull with **no registry credential stored anywhere**. GHCR would have been free but needs a GitHub PAT held as an ACA registry secret.
- **max 1 replica is not a tuning choice — it is a correctness constraint.** The SQLite database is one file on one Azure Files share and is single-writer ([ADR-0003](0003-persistence-and-storage.md)). More than one replica would mean concurrent writers over SMB. The scale cap enforces the single-writer invariant the persistence design depends on.
- **min 1 (always warm) over scale-to-zero.** Scale-to-zero would restore the ~$0 story but adds a cold start on the first request after idle. For a personal app used in short bursts, always-warm was preferred for responsiveness. Revisit if cost matters more than latency.
- **Revision-swap two-writer window (known risk, not a blocker).** In single-revision mode a deploy can briefly run the old and new replica together, both mounting the same volume — a momentary two-writer window. At this app's write profile (one user, a handful of writes a day) it is bounded by WAL + `busy_timeout=5000` ([ADR-0003](0003-persistence-and-storage.md)). If write volume ever grows, graduate to a managed database rather than hardening around this.
- **Infra separate from app deploy.** Push-to-main ships the app fast (build image, roll revision); infra is a deliberate, reviewed act. Trade-off: after merging a Bicep change you must let `infra.yml` run (it auto-runs on `infra/**` changes, or dispatch it).
- **OIDC over a service-principal secret.** Federated identity means no long-lived credential to store or rotate. The three identifiers it needs (`AZURE_CLIENT_ID` / `AZURE_TENANT_ID` / `AZURE_SUBSCRIPTION_ID`) are not themselves sensitive but are kept as GitHub Secrets for uniformity.
- **`:latest` + `:sha` convention.** CD mutates the running image imperatively (`az containerapp update`), which would otherwise drift from the Bicep-declared image. Pinning Bicep to `:latest` — which CD always pushes alongside the sha — makes an infra reapply a no-op on the image, while the sha tag gives an auditable, rollback-able revision trail. Mild `:latest` smell, accepted for the drift-freedom.
- **tzdata embedded in the binary.** The server resolves "today" from the local wall clock; the distroless runtime ships no zoneinfo, so without the blank `time/tzdata` import a container would silently use UTC and file a late-evening expense under the wrong day. The import looks unused — **do not remove it.**
- **First-deploy bootstrap.** ACR is empty until CD runs, but the app can't be created pointing at a non-existent image. The first `infra.yml` run is given a public placeholder image (`mcr.microsoft.com/k8se/quickstart:latest`) via the `containerImage` input, with a bootstrap probe override so the placeholder revision reaches healthy in minutes; CD then supersedes it and the probe reverts to `/health:8080`. Encapsulated in [`scripts/first-deploy.ps1`](../../scripts/first-deploy.ps1) and documented in [docs/deployment/first-deploy.md](../deployment/first-deploy.md).

## Later upgrades left open

- Scale-to-zero if the cost of always-warm is not justified by usage.
- Azure Key Vault for the app secrets (instead of GitHub Secrets → Bicep params) if secret handling needs to move out of GitHub.
- Building the image in CI (not just at deploy) so a broken `Dockerfile` fails a PR rather than the deploy step.

Depends on the stack ([issue #2](https://github.com/emepetres/life-ledger/issues/2)), persistence ([ADR-0003](0003-persistence-and-storage.md)), and auth ([ADR-0004](0004-access-model-auth.md)).
