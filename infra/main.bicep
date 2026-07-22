// Life Ledger infrastructure (ADR-0005).
//
// Deployed at resource-group scope by the infra workflow (.github/workflows/
// infra.yml), separately from app deploys. Declares everything the running app
// needs: an Azure Container Registry, a Log Analytics workspace, a Container Apps
// environment with an Azure Files volume for the SQLite database, and the
// Container App itself — always-warm (min 1 / max 1, single-writer-safe),
// pulling from ACR via its own managed identity.
//
// The two runtime secrets are passed in as @secure() parameters from GitHub
// Secrets by the workflow; they are never committed. See docs/deployment/
// github-secrets.md.

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

// --- derived names -----------------------------------------------------------

var suffix = uniqueString(resourceGroup().id, baseName)
var acrName = 'acr${suffix}'
var storageName = 'st${suffix}'
var shareName = 'ledgerdata'
var envName = '${baseName}-env'
var appName = baseName
var logName = '${baseName}-logs'

// Default the running image to the ACR "latest" tag. CD pushes both :latest and
// :<git-sha>, so reapplying this template never clobbers what CD deployed
// (ADR-0005). A non-empty containerImage param overrides this (first-run bootstrap).
var effectiveImage = empty(containerImage) ? '${acr.properties.loginServer}/life-ledger:latest' : containerImage

// AcrPull built-in role, granted to the app's managed identity so it can pull
// without a stored registry credential.
var acrPullRoleId = '7f951dda-4ed3-4680-a7ca-43fe172d538d'

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

// --- storage: Azure Files share for the SQLite volume ------------------------

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

resource fileService 'Microsoft.Storage/storageAccounts/fileServices@2023-05-01' = {
  parent: storage
  name: 'default'
}

resource share 'Microsoft.Storage/storageAccounts/fileServices/shares@2023-05-01' = {
  parent: fileService
  name: shareName
  properties: {
    shareQuota: 5
    enabledProtocols: 'SMB'
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

// Mount the Azure Files share into the environment so the app can reference it
// as a volume. accessMode ReadWrite: the single writer is the sole replica.
resource envStorage 'Microsoft.App/managedEnvironments/storages@2024-03-01' = {
  parent: env
  name: shareName
  properties: {
    azureFile: {
      accountName: storage.name
      accountKey: storage.listKeys().keys[0].value
      shareName: shareName
      accessMode: 'ReadWrite'
    }
  }
}

// --- container app -----------------------------------------------------------

resource app 'Microsoft.App/containerApps@2024-03-01' = {
  name: appName
  location: location
  identity: {
    type: 'SystemAssigned'
  }
  properties: {
    managedEnvironmentId: env.id
    configuration: {
      // Single revision: at steady state only one replica runs, keeping SQLite
      // single-writer. A revision swap can briefly overlap old+new on the same
      // volume — bounded by WAL + busy_timeout at this write volume (ADR-0005).
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
          identity: 'system'
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
              name: 'TZ'
              value: timeZone
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
          name: 'data'
          storageType: 'AzureFile'
          storageName: envStorage.name
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

// Grant the app's managed identity permission to pull from ACR. On the very
// first deploy the app runs a public placeholder image (containerImage param),
// so it does not pull from ACR before this assignment has propagated; CD then
// updates it to the real ACR image (ADR-0005).
resource acrPull 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(acr.id, app.id, acrPullRoleId)
  scope: acr
  properties: {
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', acrPullRoleId)
    principalId: app.identity.principalId
    principalType: 'ServicePrincipal'
  }
}

// --- outputs -----------------------------------------------------------------

output acrLoginServer string = acr.properties.loginServer
output acrName string = acr.name
output appName string = app.name
output appUrl string = 'https://${app.properties.configuration.ingress.fqdn}'
