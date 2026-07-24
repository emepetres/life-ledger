# Deployment setup: first deploy

This guide stands up the entire Life Ledger deployment on Azure (ADR-0005): the
OIDC trust between GitHub and Azure, the repository secrets/variables, and the
one-time bootstrap. The whole runbook is encapsulated in
[`scripts/first-deploy.ps1`](../../scripts/first-deploy.ps1) — run it once, then
pushing to `main` deploys automatically.

Prerequisites: the [`az`](https://learn.microsoft.com/cli/azure/install-azure-cli)
and [`gh`](https://cli.github.com/) CLIs, logged in (`az login`, `gh auth login`),
with permission to create an Entra app registration and assign roles on the
subscription.

## Quick start

Copy the template, fill in your values, and run the script:

```pwsh
Copy-Item .env.example .env
# edit .env — see the table below
pwsh scripts/first-deploy.ps1
```

`.env` is gitignored; only `.env.example` is committed. The script is
**idempotent** — running it twice creates no second app registration, no
duplicate federated credential, and converges the repo secrets/variables to the
same state — so it is safe to re-run after fixing a value or a transient failure.
Pass `-Watch` to follow the bootstrap infra run to completion instead of
returning after dispatching it.

### `.env` values

| Value                      | Meaning                                                                                                   |
| -------------------------- | -------------------------------------------------------------------------------------------------------- |
| `SUBSCRIPTION_ID`          | Azure subscription for the resource group and all resources.                                              |
| `RESOURCE_GROUP`           | Resource group to create/reuse; the deploy identity is scoped here (least privilege).                     |
| `LOCATION`                 | Azure region (e.g. `westeurope`).                                                                          |
| `REPO`                     | GitHub repo as `owner/repo`.                                                                              |
| `APP_REG_NAME`             | Display name of the Entra app registration; looked up by name and reused on re-run.                       |
| `LIFELEDGER_PASSWORD_HASH` | bcrypt hash of the login password. **Leave empty** to have the script hash a password you type (first run only). |
| `LIFELEDGER_SESSION_KEY`   | Session-cookie signing secret. **Leave empty** to have the script mint a random 32-byte key (first run only). |

## What the script does

Each step below is idempotent; the section headings map to what you'd repair by
hand if a step needs attention.

### 1. Resource group + provider registration

Creates the resource group (so the deploy identity can be scoped to it rather
than the whole subscription) and registers the `Microsoft.App` namespace, which
Azure Container Apps resources require or they fail with
`MissingSubscriptionRegistration`. Registration is once-per-subscription and can
take a couple of minutes; the script polls until it reports `Registered`.

### 2. Entra app registration + service principal + OIDC federation

GitHub Actions authenticates to Azure with **OIDC federated identity** — no
client secret is ever stored (ADR-0005). The script looks up the app registration
by `APP_REG_NAME` and reuses its `appId` (so re-runs never create a duplicate
identity), ensures a service principal exists, and adds a **federated credential**
trusting workflows on the `main` branch. That one subject covers both the
`deploy` job (push to main) and the `infra` job (push and manual dispatch, both on
`main`).

Repositories created after 2026-07-15 emit **immutable OIDC subjects** that embed
the owner and repository IDs (so a recycled repo/owner name can't impersonate the
original). The script fetches those IDs from the GitHub API and builds the subject
to match the exact token GitHub presents:
`repo:OWNER@OWNER-ID/REPO@REPO-ID:ref:refs/heads/main`. It looks the credential up
by name (`life-ledger-main`) and skips creation if present — the one resource that
errors on duplicate.

> If you later deploy from GitHub **Environments** or tags, add more federated
> credentials with the matching subject, using the same
> `OWNER@OWNER-ID/REPO@REPO-ID` form for the repo segment, e.g.
> `…:environment:production`.

### 3. Role grants on the resource group

The identity must create/manage resources **and** create the AcrPull and Blob
role assignments the Bicep template declares — that second part needs
role-assignment rights, so `Contributor` alone is not enough. The script grants
`Contributor` **and** `User Access Administrator`, scoped to the resource group.
(For a personal project you may instead grant a single `Owner` on the group.)

### 4. App secrets (ADR-0004)

Both runtime secrets are required in production:

- **`LIFELEDGER_PASSWORD_HASH`** — bcrypt hash of your login password; the
  plaintext is never stored. Set it in `.env`, or leave it empty and the script
  prompts for a password and hashes it (via `go run ./cmd/hashpw`, same as
  `make hash-password`).
- **`LIFELEDGER_SESSION_KEY`** — the session-cookie signing secret. Set it in
  `.env`, or leave it empty and the script mints a random 32-byte key. Rotating it
  later logs you out everywhere.

For an unpinned (empty) secret the script only generates it when the repo secret
is **absent** — on a re-run it leaves the already-set secret in place. That is
what keeps re-runs convergent: bcrypt re-hashes with a fresh salt and a new random
session key would differ on every run (and rotating the session key logs everyone
out). Pin a value in `.env` to force it to a specific known value instead.

### 5. Repository secrets and variables

**Secrets** (sensitive): `AZURE_CLIENT_ID` (the app's `appId`), `AZURE_TENANT_ID`,
`AZURE_SUBSCRIPTION_ID`, `LIFELEDGER_PASSWORD_HASH`, `LIFELEDGER_SESSION_KEY`.

**Variables** (non-sensitive config): `AZURE_RESOURCE_GROUP`, `AZURE_LOCATION`.

All are set via `gh secret set` / `gh variable set`, which are create-or-update,
so re-running the script just overwrites them with the current values.

### 6. Bootstrap infra run

ACR is empty until CD runs, but the Container App can't be created pointing at an
image that doesn't exist yet. So the **first** infra run uses a public placeholder
image (`mendhak/http-https-echo`) that lives on Docker Hub. Crucially, that image
listens on `:8080` and answers **any** path (including `/health`) with `200`, so
it satisfies the *same* probe the real image does. The first revision reaches
**healthy in minutes** on the production `/health:8080` probe, with no probe
override needed.

The placeholder makes the app *bootable* before ACR has an image, but it does not
avoid ACR authentication: ACA still constructs the pull secret for the ACR entry
in the app's `registries` block at provision time, whatever image runs. That is
why the app pulls with a **user-assigned** managed identity granted `AcrPull`
*before* it is created ([ADR-0006](../adr/0006-user-assigned-identity-for-acr-pull.md)) —
a system-assigned identity deadlocked the first deploy.

Because the probe never changes, CD's later image swap "just works": `ci-cd.yml`
runs `az containerapp update --image <real>`, which touches only the image and
leaves the probe at `/health:8080` — which the real image serves. No infra re-run
is needed to make the real app healthy, and production health checking is never
weakened. (This is why a health-serving placeholder is used rather than bending
the probe to the placeholder: a probe concession would break across the CD image
swap, moving the readiness failure from the infra step to the CD step.)

After the script dispatches the run:

1. **Watch it provision** (ACR, storage, environment, Container App on the
   placeholder, AcrPull + Blob role assignments):

   ```pwsh
   gh run watch --repo $REPO
   ```

2. **Deploy the real app.** Push to `main` (which runs `ci-cd.yml`). CD builds the
   image, pushes `:latest` + `:<sha>` to ACR, and rolls the app onto it. The probe
   stays `/health:8080` throughout, so the swap comes up healthy directly.

3. **Verify.** The app URL is an output of the infra deployment:

   ```pwsh
   az deployment group show -g $RESOURCE_GROUP -n main `
     --query properties.outputs.appUrl.value -o tsv
   ```

From here on, every push to `main` deploys automatically (gated on tests), and
infra changes apply when `infra/**` changes or you dispatch `infra.yml` — with an
**empty** `containerImage`, so it keeps the image CD deployed.

## Rotating secrets later

- **Password / session key**: update the GitHub Secret (or re-run the script with
  new `.env` values), then re-run `infra.yml` (it rewrites the ACA secrets from
  the Bicep params). Rotating the session key logs you out everywhere — that is
  the intended "log out everywhere" lever.
- **Azure identity**: add or replace the federated credential (re-run the script,
  or step 2 by hand); no secret to rotate because none is stored.
