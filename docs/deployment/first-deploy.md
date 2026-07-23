# Deployment setup: GitHub Secrets & first deploy

This guide configures everything the CI/CD and infra workflows need to deploy
Life Ledger to Azure (ADR-0005): the OIDC trust between GitHub and Azure, the
repository secrets/variables, and the one-time bootstrap. Run the steps once;
after that, pushing to `main` deploys automatically.

Prerequisites: the [`az`](https://learn.microsoft.com/cli/azure/install-azure-cli)
and [`gh`](https://cli.github.com/) CLIs, logged in (`az login`, `gh auth login`),
with permission to create an Entra app registration and assign roles on the
subscription.

Replace the placeholders as you go:

```pwsh
$SUBSCRIPTION_ID = "<your-subscription-id>"
$RESOURCE_GROUP   = "life-ledger-rg"
$LOCATION         = "westeurope"
$REPO             = "emepetres/life-ledger"     # owner/repo
$APP_REG_NAME     = "jcarnero-life-ledger-github"
```

## 1. Resource group

Created once so the deploy identity can be scoped to it (least privilege) rather
than the whole subscription:

```pwsh
az account set --subscription $SUBSCRIPTION_ID
az group create --name $RESOURCE_GROUP --location $LOCATION
```

Register the `Microsoft.App` namespace on the subscription. The template deploys
Azure Container Apps resources, which fail with `MissingSubscriptionRegistration`
until the namespace is registered. This is a once-per-subscription operation that
can take a couple of minutes:

```pwsh
az provider register --namespace Microsoft.App
az provider show --namespace Microsoft.App --query "registrationState"   # expect "Registered"
```

## 2. Entra app registration + OIDC federation

GitHub Actions authenticates to Azure with **OIDC federated identity** — no
client secret is ever stored (ADR-0005). Create an app registration and a service
principal for it (If you don't have permission to create an app registration, ask your Azure admin to do this step for you):

```pwsh
$APP_ID    = az ad app create --display-name $APP_REG_NAME --query appId -o tsv
az ad sp create --id $APP_ID
$TENANT_ID = az account show --query tenantId -o tsv
```

Add a **federated credential** trusting workflows that run on the `main` branch
of the repo. This one subject covers the `deploy` job (push to main) and the
`infra` job (push to main and manual dispatch, both of which run on `main`):

```pwsh
# Repositories created after 2026-07-15 emit immutable OIDC subjects that
# include the owner and repository IDs (so a recycled repo/owner name can't be
# reused to impersonate). Fetch those IDs from the GitHub API and build the
# subject to match the exact token GitHub will present:
#   repo:OWNER@OWNER-ID/REPO@REPO-ID:ref:refs/heads/main
$repoOwner, $repoName = $REPO -split '/', 2
$ownerId = gh api "repos/$REPO" --jq '.owner.id'
$repoId  = gh api "repos/$REPO" --jq '.id'
# Use $($repoId) so the ':' after it isn't parsed as a PSDrive scope-qualifier
# (like $env:PATH) — bare $repoId:ref... would resolve to empty.
$subject = "repo:$repoOwner@$ownerId/$repoName@$($repoId):ref:refs/heads/main"

$body = @'
{
  "name": "life-ledger-main",
  "issuer": "https://token.actions.githubusercontent.com",
  "subject": "__SUBJECT__",
  "audiences": ["api://AzureADTokenExchange"]
}
'@ -replace '__SUBJECT__', $subject

$body | Out-File -FilePath .\fcred.json -Encoding utf8 -NoNewline
az ad app federated-credential create --id $APP_ID --parameters "@.\fcred.json"
Remove-Item .\fcred.json
```

> If you later deploy from GitHub **Environments** or tags, add more federated
> credentials with the matching subject. Use the same
> `OWNER@OWNER-ID/REPO@REPO-ID` form for the repo segment, e.g.
> `repo:$repoOwner@$ownerId/$repoName@$repoId:environment:production`.

## 3. Grant the identity rights on the resource group

The identity must create/manage resources **and** create the AcrPull role
assignment the Bicep template declares. That second part needs role-assignment
rights, so `Contributor` alone is not enough:

```pwsh
$SP_OBJECT_ID = az ad sp show --id $APP_ID --query id -o tsv
$SCOPE = "/subscriptions/$SUBSCRIPTION_ID/resourceGroups/$RESOURCE_GROUP"

# Create and manage resources.
az role assignment create --assignee-object-id $SP_OBJECT_ID `
  --assignee-principal-type ServicePrincipal `
  --role "Contributor" --scope $SCOPE

# Create the app's AcrPull role assignment (Bicep does this on deploy).
az role assignment create --assignee-object-id $SP_OBJECT_ID `
  --assignee-principal-type ServicePrincipal `
  --role "User Access Administrator" --scope $SCOPE
```

(For a personal project you may instead grant a single `Owner` on the group.)

## 4. Generate the app secrets

Both are required in production (ADR-0004):

- **`LIFELEDGER_PASSWORD_HASH`** — bcrypt hash of your chosen login password. The
  plaintext is never stored:

  ```pwsh
  make hash-password     # prompts for the password, prints the hash
  ```

- **`LIFELEDGER_SESSION_KEY`** — the session-cookie signing secret. Any long
  random string; rotating it later logs you out everywhere:

  ```pwsh
  # Generate 32 cryptographically random bytes and return as Base64.
  [Convert]::ToBase64String(
    [System.Security.Cryptography.RandomNumberGenerator]::GetBytes(32))
  ```

## 5. Set repository secrets and variables

**Secrets** (sensitive; `gh secret set` reads the value from the prompt/stdin):

| Secret                     | Value                        |
| -------------------------- | ---------------------------- |
| `AZURE_CLIENT_ID`          | the `$APP_ID` from step 2    |
| `AZURE_TENANT_ID`          | the `$TENANT_ID` from step 2 |
| `AZURE_SUBSCRIPTION_ID`    | your `$SUBSCRIPTION_ID`      |
| `LIFELEDGER_PASSWORD_HASH` | the bcrypt hash from step 4  |
| `LIFELEDGER_SESSION_KEY`   | the random key from step 4   |

**Variables** (non-sensitive config):

| Variable               | Value                                     |
| ---------------------- | ----------------------------------------- |
| `AZURE_RESOURCE_GROUP` | `life-ledger-rg` (your `$RESOURCE_GROUP`) |
| `AZURE_LOCATION`       | `westeurope` (your `$LOCATION`)           |

```pwsh
# Pipe variables directly to stdout; gh secret set reads the value from stdin.
$APP_ID          | gh secret set AZURE_CLIENT_ID       --repo $REPO
$TENANT_ID       | gh secret set AZURE_TENANT_ID       --repo $REPO
$SUBSCRIPTION_ID | gh secret set AZURE_SUBSCRIPTION_ID --repo $REPO
gh secret set LIFELEDGER_PASSWORD_HASH --repo $REPO   # paste the hash
gh secret set LIFELEDGER_SESSION_KEY   --repo $REPO   # paste the key

gh variable set AZURE_RESOURCE_GROUP --repo $REPO --body $RESOURCE_GROUP
gh variable set AZURE_LOCATION       --repo $REPO --body $LOCATION
```

## 6. First deploy (bootstrap order)

The registry is empty until CD runs, but the Container App can't start on an
image that doesn't exist yet. So the **first** infra run uses a public placeholder
image; CD then replaces it (ADR-0005).

1. **Provision infra with the placeholder.** Run the `infra` workflow manually,
   setting the bootstrap input:

   ```pwsh
   gh workflow run infra.yml --repo $REPO `
     -f containerImage="mcr.microsoft.com/k8se/quickstart:latest"
   ```

   This creates the ACR, storage, environment, and the Container App (running the
   placeholder), and assigns AcrPull to the app's identity.

2. **Deploy the real app.** Push to `main` (or re-run `ci-cd.yml`). CD builds the
   image, pushes `:latest` + `:<sha>` to ACR, and rolls the app onto it.

3. **Verify.** The app URL is an output of the infra deployment:

   ```pwsh
   az deployment group show -g $RESOURCE_GROUP -n main `
     --query properties.outputs.appUrl.value -o tsv
   ```

From here on, every push to `main` deploys automatically (gated on tests), and
infra changes apply when `infra/**` changes or you dispatch `infra.yml` — with an
**empty** `containerImage`, so it keeps the image CD deployed.

## Rotating secrets later

- **Password / session key**: update the GitHub Secret, then re-run `infra.yml`
  (it rewrites the ACA secrets from the Bicep params). Rotating the session key
  logs you out everywhere — that is the intended "log out everywhere" lever.
- **Azure identity**: add or replace the federated credential (step 2); no secret
  to rotate because none is stored.
