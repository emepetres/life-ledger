// Life Ledger infrastructure (ADR-0005).
//
// Deployed at resource-group scope by the infra workflow (.github/workflows/
// infra.yml), separately from app deploys. Declares everything the running app
// needs: an Azure Container Registry, a Log Analytics workspace, a Container Apps
// environment, and the Container App itself — always-warm (min 1 / max 1,
// single-writer-safe), pulling from ACR via its own managed identity. SQLite runs
// on a local EmptyDir volume (which honours its POSIX locks) and the store backs
// the database up to a Blob container the app reaches via its managed identity
// (ADR-0003).
//
// The two runtime secrets are passed in as @secure() parameters from GitHub
// Secrets by the workflow; they are never committed. See docs/deployment/
// first-deploy.md.

targetScope = 'resourceGroup'

@description('Azure region for all resources.')
param location string = 'westeurope'

@description('Short base name used to derive resource names.')
param baseName string = 'lifeledger'

@description('Full container image reference to run. Leave empty to default to the ACR "latest" tag (ADR-0005). Override with a public placeholder image on the very first deploy, before any image has been pushed to ACR.')
param containerImage string = ''

@description('bcrypt hash of the shared password (LIFELEDGER_PASSWORD_HASH).')
@secure()
param passwordHash string

@description('Session-cookie signing secret (LIFELEDGER_SESSION_KEY).')
@secure()
param sessionKey string

@description('IANA timezone selecting the embedded zoneinfo so "today" is the local calendar day (ADR-0005).')
param timeZone string = 'Europe/Madrid'

@description('Object id of the CI/deploy service principal (OIDC). Granted data-plane access to the snapshot store so the nightly backup workflow can copy and prune (ADR-0007).')
param ciPrincipalId string

// --- derived names -----------------------------------------------------------

var suffix = uniqueString(resourceGroup().id, baseName)
var acrName = 'acr${suffix}'
var storageName = 'st${suffix}'
var backupContainerName = 'ledgerbackup'
// Second, private container holding the retained point-in-time snapshot history
// (daily/ + monthly/). Disjoint from ledgerbackup; the app never touches it — only
// the nightly backup workflow's CI principal does (ADR-0007).
var snapshotsContainerName = 'ledgersnapshots'
var envName = '${baseName}-env'
var appName = baseName
var logName = '${baseName}-logs'
var uamiName = '${baseName}-id'

// Default the running image to the ACR "latest" tag. CD pushes both :latest and
// :<git-sha>, so reapplying this template never clobbers what CD deployed
// (ADR-0005). A non-empty containerImage param overrides this (first-run bootstrap).
var effectiveImage = empty(containerImage) ? '${acr.properties.loginServer}/life-ledger:latest' : containerImage

// AcrPull built-in role, granted to the app's managed identity so it can pull
// without a stored registry credential.
var acrPullRoleId = '7f951dda-4ed3-4680-a7ca-43fe172d538d'

// Storage Blob Data Contributor built-in role, granted to the app's managed
// identity so it can read/write the backup blob with no stored account key.
// Also granted to the CI principal on ledgersnapshots (copy dest + prune delete).
var blobContributorRoleId = 'ba92f5b4-2d11-453d-a403-e96b0029c9fe'

// Storage Blob Data Reader built-in role, granted to the CI principal on
// ledgerbackup so the nightly workflow can read the copy source (ADR-0007).
var blobReaderRoleId = '2a2b9908-6ea1-4ae2-8e65-a410df84e7d1'

// --- user-assigned identity --------------------------------------------------

// The app pulls from ACR and reaches Blob with a *user-assigned* managed identity
// (ADR-0006), not a system-assigned one. ACA constructs the pull secret for every
// registry in the app's `registries` block at revision-provision time, so the
// identity must already hold AcrPull when the app first provisions. A
// system-assigned identity can't: its principal only exists once the app is
// created, so the AcrPull grant necessarily follows the very provision it needs
// to unblock — an unbreakable first-deploy deadlock. A user-assigned identity is
// a standalone resource that pre-exists the app and is granted below, before the
// app depends on those grants.
resource uami 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' = {
  name: uamiName
  location: location
}

// --- container registry ------------------------------------------------------

resource acr 'Microsoft.ContainerRegistry/registries@2023-07-01' = {
  name: acrName
  location: location
  sku: {
    name: 'Basic'
  }
  properties: {
    adminUserEnabled: false
  }
}

// --- storage: Blob container for the SQLite backup ---------------------------

resource storage 'Microsoft.Storage/storageAccounts@2023-05-01' = {
  name: storageName
  location: location
  sku: {
    name: 'Standard_LRS'
  }
  kind: 'StorageV2'
  properties: {
    minimumTlsVersion: 'TLS1_2'
    allowBlobPublicAccess: false
  }
}

resource blobService 'Microsoft.Storage/storageAccounts/blobServices@2023-05-01' = {
  parent: storage
  name: 'default'
}

// The single container the store snapshots the database into (backup-on-write)
// and restores from (restore-on-boot). The app authenticates with its managed
// identity, so no account key is ever handled; public access stays off.
resource backupContainer 'Microsoft.Storage/storageAccounts/blobServices/containers@2023-05-01' = {
  parent: blobService
  name: backupContainerName
  properties: {
    publicAccess: 'None'
  }
}

// The retained snapshot history (ADR-0007): daily/YYYY-MM-DD.db kept for a rolling
// 7-day window plus monthly/YYYY-MM.db kept forever, written by the nightly backup
// workflow. Blob versioning and soft delete are deliberately left off — the prune
// is an explicit delete-batch in the workflow, not a lifecycle-management policy
// (#33). Private; the app UAMI has no grant here, only the CI principal does.
resource snapshotsContainer 'Microsoft.Storage/storageAccounts/blobServices/containers@2023-05-01' = {
  parent: blobService
  name: snapshotsContainerName
  properties: {
    publicAccess: 'None'
  }
}

// --- log analytics + container apps environment ------------------------------

resource logs 'Microsoft.OperationalInsights/workspaces@2023-09-01' = {
  name: logName
  location: location
  properties: {
    sku: {
      name: 'PerGB2018'
    }
    retentionInDays: 30
  }
}

resource env 'Microsoft.App/managedEnvironments@2024-03-01' = {
  name: envName
  location: location
  properties: {
    appLogsConfiguration: {
      destination: 'log-analytics'
      logAnalyticsConfiguration: {
        customerId: logs.properties.customerId
        sharedKey: logs.listKeys().primarySharedKey
      }
    }
  }
}

// --- container app -----------------------------------------------------------

resource app 'Microsoft.App/containerApps@2024-03-01' = {
  name: appName
  location: location
  identity: {
    type: 'UserAssigned'
    userAssignedIdentities: {
      '${uami.id}': {}
    }
  }
  // Provision only after the identity holds AcrPull + Blob: ACA validates the
  // ACR registry secret during the app's first provision, which needs AcrPull
  // already granted (ADR-0006).
  dependsOn: [
    acrPull
    blobContributor
  ]
  properties: {
    managedEnvironmentId: env.id
    configuration: {
      // Single revision: at steady state only one replica runs, keeping SQLite
      // single-writer. Each replica has its own EmptyDir database; the store's
      // Blob backup (backup-on-write / restore-on-boot) is what carries data
      // across a reschedule or a revision swap (ADR-0003).
      activeRevisionsMode: 'Single'
      ingress: {
        external: true
        targetPort: 8080
        transport: 'auto'
        allowInsecure: false
      }
      registries: [
        {
          server: acr.properties.loginServer
          identity: uami.id
        }
      ]
      secrets: [
        {
          name: 'password-hash'
          value: passwordHash
        }
        {
          name: 'session-key'
          value: sessionKey
        }
      ]
    }
    template: {
      containers: [
        {
          name: appName
          image: effectiveImage
          resources: {
            cpu: json('0.25')
            memory: '0.5Gi'
          }
          env: [
            {
              name: 'LIFELEDGER_SECURE_COOKIE'
              value: 'true'
            }
            {
              name: 'LIFELEDGER_DB_PATH'
              value: '/data/expenses.db'
            }
            {
              // Container URL of the backup blob store. Setting this wires the
              // store's Blob backup sink (backup-on-write / restore-on-boot); the
              // app reaches it via managed identity, no key (ADR-0003).
              name: 'LIFELEDGER_BACKUP_BLOB_URL'
              value: '${storage.properties.primaryEndpoints.blob}${backupContainer.name}'
            }
            {
              name: 'TZ'
              value: timeZone
            }
            {
              // Selects the user-assigned identity for the Blob backup's
              // DefaultAzureCredential (ADR-0006). Without it the credential
              // would look for a system-assigned identity, which the app no
              // longer has. ACR pull is wired separately via registries[].identity.
              name: 'AZURE_CLIENT_ID'
              value: uami.properties.clientId
            }
            {
              name: 'LIFELEDGER_PASSWORD_HASH'
              secretRef: 'password-hash'
            }
            {
              name: 'LIFELEDGER_SESSION_KEY'
              secretRef: 'session-key'
            }
          ]
          volumeMounts: [
            {
              volumeName: 'data'
              mountPath: '/data'
            }
          ]
          probes: [
            {
              type: 'Liveness'
              httpGet: {
                path: '/health'
                port: 8080
              }
              initialDelaySeconds: 3
              periodSeconds: 10
            }
            {
              type: 'Readiness'
              httpGet: {
                path: '/health'
                port: 8080
              }
              initialDelaySeconds: 3
              periodSeconds: 10
            }
          ]
        }
      ]
      volumes: [
        {
          // Local ephemeral disk (container ext4): it honours SQLite's POSIX file
          // locks, so no SQLITE_BUSY. Durability comes from the store's Blob
          // backup, not this volume (ADR-0003).
          name: 'data'
          storageType: 'EmptyDir'
        }
      ]
      scale: {
        // Always warm, capped at one replica: no cold starts, single-writer safe.
        minReplicas: 1
        maxReplicas: 1
      }
    }
  }
}

// Grant the user-assigned identity permission to pull from ACR. Assigned to the
// UAMI (not the app), so it carries no dependency on the app and is in place
// before the app provisions — which is what breaks the first-deploy deadlock the
// system-assigned identity caused (ADR-0006). The app dependsOn this assignment.
resource acrPull 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(acr.id, uami.id, acrPullRoleId)
  scope: acr
  properties: {
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', acrPullRoleId)
    principalId: uami.properties.principalId
    principalType: 'ServicePrincipal'
  }
}

// Grant the user-assigned identity read/write on the backup container, so the
// store's Blob sink authenticates with the identity instead of an account key.
// Scoped to the ledgerbackup container only — not the whole account — so the app
// cannot see or touch the retained snapshot history in ledgersnapshots it would
// roll back to (ADR-0007, least privilege). On the same UAMI as AcrPull so the
// app carries a single identity (ADR-0006).
resource blobContributor 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(backupContainer.id, uami.id, blobContributorRoleId)
  scope: backupContainer
  properties: {
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', blobContributorRoleId)
    principalId: uami.properties.principalId
    principalType: 'ServicePrincipal'
  }
}

// --- CI principal grants for the nightly backup workflow (ADR-0007) ----------

// The backup workflow copies ledgerbackup/expenses.db (source) into
// ledgersnapshots/{daily,monthly}/ (dest) and prunes old dailies, all under
// `az ... --auth-mode login` as the CI/deploy OIDC principal. It gets exactly the
// two data-plane grants that needs and no more: Reader on the source container,
// Contributor on the dest container. Reader carries generateUserDelegationKey, so
// the CLI can mint the user-delegation SAS the copy source needs (verified #34).
resource ciBackupReader 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(backupContainer.id, ciPrincipalId, blobReaderRoleId)
  scope: backupContainer
  properties: {
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', blobReaderRoleId)
    principalId: ciPrincipalId
    principalType: 'ServicePrincipal'
  }
}

resource ciSnapshotsContributor 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(snapshotsContainer.id, ciPrincipalId, blobContributorRoleId)
  scope: snapshotsContainer
  properties: {
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', blobContributorRoleId)
    principalId: ciPrincipalId
    principalType: 'ServicePrincipal'
  }
}

// --- outputs -----------------------------------------------------------------

output acrLoginServer string = acr.properties.loginServer
output acrName string = acr.name
output appName string = app.name
output appUrl string = 'https://${app.properties.configuration.ingress.fqdn}'
