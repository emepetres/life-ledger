#!/usr/bin/env pwsh
#
# first-deploy.ps1 — stand up the entire Life Ledger deployment from one command.
#
# Encapsulates the one-time bootstrap runbook (ADR-0005): resource group +
# provider registration, the Entra app registration + service principal + OIDC
# federated credential that lets GitHub Actions log in to Azure without a stored
# secret, the role grants, the app secrets, the repo secrets/variables, and the
# first (bootstrap) infra run. Driven entirely by a gitignored `.env` at the repo
# root — copy `.env.example`, fill it in, then run this script.
#
#     pwsh scripts/first-deploy.ps1
#
# Idempotent by construction: the app registration is looked up by name and
# reused (no duplicate identities), the federated credential is skipped if it
# already exists (the one resource that errors on duplicate), and everything else
# (resource group, provider registration, role assignments, gh secret/variable)
# is naturally create-or-update. Running it twice converges to the same state.
#
# Prerequisites: the `az` and `gh` CLIs, both logged in (`az login`,
# `gh auth login`), with permission to create an Entra app registration and
# assign roles on the subscription. See docs/deployment/first-deploy.md.

[CmdletBinding()]
param(
    # Watch the bootstrap infra run to completion instead of returning after
    # dispatching it.
    [switch]$Watch
)

# Fail fast: stop on the first error, and treat a non-zero exit from az/gh as a
# terminating error too (pwsh 7.4+), so a failed step never silently continues.
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $true

$RepoRoot = Split-Path -Parent $PSScriptRoot
$FederatedCredentialName = 'life-ledger-main'
# Public placeholder for the first infra run, before any image exists in ACR.
# It listens on :8080 and answers any path (incl. /health) with 200, so it
# satisfies the SAME probe the real image does (/health:8080) — the first
# revision goes healthy in minutes and, crucially, CD's later image swap keeps
# the same probe, so it stays healthy with no probe change or infra re-run. It
# only makes the app bootable before ACR has an image; ACR pull auth is handled
# by the user-assigned identity granted AcrPull before the app (ADR-0006).
$BootstrapImage = 'mendhak/http-https-echo:latest'

function Read-DotEnv {
    param([Parameter(Mandatory)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path)) {
        throw "No .env found at $Path. Copy .env.example to .env and fill it in."
    }

    $values = @{}
    foreach ($line in Get-Content -LiteralPath $Path) {
        $trimmed = $line.Trim()
        if ($trimmed -eq '' -or $trimmed.StartsWith('#')) { continue }
        $idx = $trimmed.IndexOf('=')
        if ($idx -lt 1) { continue }
        $key = $trimmed.Substring(0, $idx).Trim()
        $val = $trimmed.Substring($idx + 1).Trim()
        if ($val.Length -ge 2 -and
            (($val[0] -eq '"' -and $val[-1] -eq '"') -or ($val[0] -eq "'" -and $val[-1] -eq "'"))) {
            $val = $val.Substring(1, $val.Length - 2)
        }
        $values[$key] = $val
    }
    return $values
}

function Write-Step {
    param([Parameter(Mandatory)][string]$Message)
    Write-Host "`n==> $Message" -ForegroundColor Cyan
}

# Decide the value a repo secret should take, preserving idempotency:
#   - pinned in .env      -> use it (write, converging to that value)
#   - unpinned, absent    -> generate it via $Generate (first run)
#   - unpinned, present    -> return $null (leave the existing secret untouched)
function Resolve-Secret {
    param(
        [Parameter(Mandatory)][string]$Name,
        [string]$PinnedValue,
        [string[]]$ExistingSecrets,
        [Parameter(Mandatory)][scriptblock]$Generate
    )
    if (-not [string]::IsNullOrWhiteSpace($PinnedValue)) {
        Write-Host "  ${Name}: using value from .env"
        return $PinnedValue
    }
    if ($ExistingSecrets -contains $Name) {
        Write-Host "  ${Name}: already set on the repo, leaving as-is"
        return $null
    }
    Write-Host "  ${Name}: not in .env and not yet set — generating"
    return (& $Generate)
}

# Set a repo secret from a variable, skipping when the value is $null (which
# Resolve-Secret returns when it left an existing secret in place).
function Set-Secret {
    param(
        [Parameter(Mandatory)][string]$Name,
        [AllowNull()][string]$Value
    )
    if ([string]::IsNullOrEmpty($Value)) { return }
    $Value | gh secret set $Name --repo $Repo
    Write-Host "  ${Name}: set"
}

# --- load and validate config ------------------------------------------------

$dotenv = Read-DotEnv -Path (Join-Path $RepoRoot '.env')

$required = 'SUBSCRIPTION_ID', 'RESOURCE_GROUP', 'LOCATION', 'REPO', 'APP_REG_NAME'
$missing = $required | Where-Object { [string]::IsNullOrWhiteSpace($dotenv[$_]) }
if ($missing) {
    throw "Missing required .env values: $($missing -join ', '). See .env.example."
}

$SubscriptionId = $dotenv['SUBSCRIPTION_ID']
$ResourceGroup = $dotenv['RESOURCE_GROUP']
$Location = $dotenv['LOCATION']
$Repo = $dotenv['REPO']
$AppRegName = $dotenv['APP_REG_NAME']
$PasswordHash = $dotenv['LIFELEDGER_PASSWORD_HASH']
$SessionKey = $dotenv['LIFELEDGER_SESSION_KEY']

if ($Repo -notmatch '^[^/]+/[^/]+$') {
    throw "REPO must be in owner/repo form (got '$Repo')."
}
$repoOwner, $repoName = $Repo -split '/', 2

# --- preflight: CLIs present and logged in -----------------------------------

Write-Step "Checking az and gh are logged in"
az account show --output none
gh auth status | Out-Null

# --- 1. resource group + provider registration -------------------------------

Write-Step "Resource group '$ResourceGroup' in $Location"
az account set --subscription $SubscriptionId
az group create --name $ResourceGroup --location $Location --output none

Write-Step "Registering the Microsoft.App provider (once per subscription)"
az provider register --namespace Microsoft.App --output none
for ($i = 0; $i -lt 30; $i++) {
    $state = az provider show --namespace Microsoft.App --query registrationState -o tsv
    if ($state -eq 'Registered') { break }
    Write-Host "  registrationState=$state, waiting..."
    Start-Sleep -Seconds 10
}
if ($state -ne 'Registered') {
    Write-Warning "Microsoft.App is '$state', not yet 'Registered'. The infra run may fail with MissingSubscriptionRegistration until it finishes; re-run this script or the infra workflow shortly."
}

# --- 2. Entra app registration + service principal + OIDC ---------------------

Write-Step "Entra app registration '$AppRegName' (looked up by name, reused if present)"
$appId = az ad app list --display-name $AppRegName --query "[0].appId" -o tsv
if ([string]::IsNullOrWhiteSpace($appId)) {
    Write-Host "  creating new app registration"
    $appId = az ad app create --display-name $AppRegName --query appId -o tsv
} else {
    Write-Host "  reusing appId $appId"
}

Write-Step "Service principal for the app (reused if present)"
$spObjectId = az ad sp list --filter "appId eq '$appId'" --query "[0].id" -o tsv
if ([string]::IsNullOrWhiteSpace($spObjectId)) {
    Write-Host "  creating service principal"
    $spObjectId = az ad sp create --id $appId --query id -o tsv
} else {
    Write-Host "  reusing service principal $spObjectId"
}

$tenantId = az account show --query tenantId -o tsv

Write-Step "OIDC federated credential '$FederatedCredentialName' (skipped if present)"
$existingCred = az ad app federated-credential list --id $appId `
    --query "[?name=='$FederatedCredentialName'] | [0].name" -o tsv
if ([string]::IsNullOrWhiteSpace($existingCred)) {
    # Repositories created after 2026-07-15 emit immutable OIDC subjects that
    # embed the owner and repository IDs, so a recycled repo/owner name can't be
    # reused to impersonate. Build the subject from those IDs to match the exact
    # token GitHub presents: repo:OWNER@OWNER-ID/REPO@REPO-ID:ref:refs/heads/main
    $ownerId, $repoId = gh api "repos/$Repo" --jq '.owner.id, .id'
    # $($repoId) so the trailing ':' isn't parsed as a PSDrive scope-qualifier.
    $subject = "repo:$repoOwner@$ownerId/$repoName@$($repoId):ref:refs/heads/main"

    $body = @'
{
  "name": "__NAME__",
  "issuer": "https://token.actions.githubusercontent.com",
  "subject": "__SUBJECT__",
  "audiences": ["api://AzureADTokenExchange"]
}
'@ -replace '__NAME__', $FederatedCredentialName -replace '__SUBJECT__', $subject

    $credFile = Join-Path ([System.IO.Path]::GetTempPath()) "fcred-$([System.IO.Path]::GetRandomFileName()).json"
    try {
        $body | Out-File -FilePath $credFile -Encoding utf8 -NoNewline
        az ad app federated-credential create --id $appId --parameters "@$credFile" --output none
        Write-Host "  created for subject $subject"
    } finally {
        Remove-Item -LiteralPath $credFile -ErrorAction SilentlyContinue
    }
} else {
    Write-Host "  already present, skipping"
}

# --- 3. role grants on the resource group ------------------------------------

# The identity must create/manage resources (Contributor) AND create the AcrPull
# + Blob role assignments the Bicep template declares (User Access Administrator).
# az role assignment create is idempotent: it returns the existing assignment.
Write-Step "Granting Contributor + User Access Administrator on the resource group"
$scope = "/subscriptions/$SubscriptionId/resourceGroups/$ResourceGroup"
foreach ($role in 'Contributor', 'User Access Administrator') {
    az role assignment create --assignee-object-id $spObjectId `
        --assignee-principal-type ServicePrincipal `
        --role $role --scope $scope --output none
    Write-Host "  $role granted"
}

# --- 4. app secrets (ADR-0004) -----------------------------------------------
#
# Idempotency: a value pinned in .env is always written (converges to that
# value). An unpinned value is generated/collected only when the repo secret is
# ABSENT — otherwise the existing secret is left untouched. This is what keeps
# re-runs convergent: bcrypt re-hashes with a fresh salt and a new random session
# key would differ every run (and rotating the session key logs everyone out), so
# the recommended empty-value path must not regenerate an already-set secret.

Write-Step "App secrets (ADR-0004)"
$existingSecrets = gh secret list --repo $Repo --json name --jq '.[].name'

$PasswordHash = Resolve-Secret -Name 'LIFELEDGER_PASSWORD_HASH' -PinnedValue $PasswordHash `
    -ExistingSecrets $existingSecrets -Generate {
    $secure = Read-Host "  Enter the login password to hash" -AsSecureString
    $plain = [System.Net.NetworkCredential]::new('', $secure).Password
    if ([string]::IsNullOrWhiteSpace($plain)) { throw "Password must not be empty." }
    Push-Location $RepoRoot
    try {
        # hashpw reads the password from stdin and prints the bcrypt hash to
        # stdout (ADR-0004); its "Enter password:" prompt on stderr is silenced.
        $hash = ($plain | go run ./cmd/hashpw 2>$null)
    } finally {
        Pop-Location
    }
    if ([string]::IsNullOrWhiteSpace($hash)) { throw "Failed to hash the password." }
    $hash
}

$SessionKey = Resolve-Secret -Name 'LIFELEDGER_SESSION_KEY' -PinnedValue $SessionKey `
    -ExistingSecrets $existingSecrets -Generate {
    [Convert]::ToBase64String(
        [System.Security.Cryptography.RandomNumberGenerator]::GetBytes(32))
}

# --- 5. repo secrets and variables -------------------------------------------

# The three Azure identity values are deterministic, so setting them every run is
# already convergent. Set-Secret skips a null $PasswordHash/$SessionKey, which
# Resolve-Secret returns when it left an existing secret in place.
Write-Step "Setting GitHub repo secrets and variables on $Repo"
Set-Secret -Name AZURE_CLIENT_ID          -Value $appId
Set-Secret -Name AZURE_TENANT_ID          -Value $tenantId
Set-Secret -Name AZURE_SUBSCRIPTION_ID    -Value $SubscriptionId
Set-Secret -Name LIFELEDGER_PASSWORD_HASH -Value $PasswordHash
Set-Secret -Name LIFELEDGER_SESSION_KEY   -Value $SessionKey

gh variable set AZURE_RESOURCE_GROUP --repo $Repo --body $ResourceGroup
gh variable set AZURE_LOCATION       --repo $Repo --body $Location
Write-Host "  variables set"

# --- 6. bootstrap infra run --------------------------------------------------

# ACR is empty until CD runs, but the Container App can't be created on an image
# that doesn't exist yet. So the first infra run uses a public placeholder that
# answers /health on :8080, so the revision reaches healthy in minutes on the
# SAME probe production uses. CD then swaps in the real image without touching the
# probe, so it stays healthy — no probe override, no infra re-run needed. ACR pull
# auth is via the user-assigned identity, granted AcrPull before the app (ADR-0006).
Write-Step "Kicking off the bootstrap infra run"
gh workflow run infra.yml --repo $Repo -f containerImage="$BootstrapImage"
Write-Host "  dispatched infra.yml with the health-serving placeholder"

if ($Watch) {
    Write-Step "Watching the bootstrap infra run"
    # Give GitHub a moment to register the dispatched run before watching it.
    Start-Sleep -Seconds 5
    $runId = gh run list --repo $Repo --workflow infra.yml --limit 1 --json databaseId --jq '.[0].databaseId'
    if (-not [string]::IsNullOrWhiteSpace($runId)) {
        gh run watch $runId --repo $Repo --exit-status
    } else {
        Write-Warning "Could not find the dispatched run to watch; check 'gh run list --repo $Repo'."
    }
}

# --- next steps --------------------------------------------------------------

Write-Host "`nBootstrap infra run dispatched. Next:" -ForegroundColor Green
Write-Host @"

  1. Watch it provision (ACR, storage, environment, Container App on the
     placeholder, AcrPull + Blob role assignments):

       gh run watch --repo $Repo

  2. Deploy the real app. Push to main (or run the ci-cd workflow on push). CD
     builds the image, pushes :latest + :<sha> to ACR, and rolls the app onto
     it. The probe is /health:8080 throughout — the placeholder already answered
     it, so the swap stays healthy with no further infra run.

  3. Get the app URL:

       az deployment group show -g $ResourceGroup -n main ``
         --query properties.outputs.appUrl.value -o tsv
"@
