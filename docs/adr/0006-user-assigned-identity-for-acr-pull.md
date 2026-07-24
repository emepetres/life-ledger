# User-assigned identity to break the ACR-pull deadlock

**Status:** accepted — supersedes the *"pulls via its system-assigned managed identity"* decision in [ADR-0005](0005-deploy-azure-cicd.md).

The Container App authenticates to ACR (image pull) and to Blob (backup) with a **single user-assigned managed identity**, granted `AcrPull` and `Storage Blob Data Contributor` **before** the app is created. ADR-0005 specified a *system-assigned* identity; that choice makes the very first deploy impossible, which is what this ADR fixes.

## The problem: a first-deploy deadlock

The first `infra.yml` run failed after ~20–30 minutes with `Failed to provision revision … Operation expired`. The system logs showed the real cause, repeating throughout the app's provisioning:

```
FetchingKeyVaultSecretFailed | Failed to construct registry secret for registry
'acr….azurecr.io' … Error: ACR token exchange endpoint returned error status: 401
```

Azure Container Apps constructs a pull secret for **every** registry in the app's `registries` block at **revision-provision time** — regardless of which image the container actually runs. So even the public Docker Hub bootstrap placeholder (which never touches ACR) triggered an ACR token exchange, and it 401'd because the identity had no `AcrPull`.

With a **system-assigned** identity this cannot be fixed within one deployment, because:

1. The `AcrPull` role assignment references `app.identity.principalId`, so it can only be created **after** the app exists.
2. The app can't finish its first provision until the ACR pull secret constructs, which needs `AcrPull`.

The grant necessarily follows the provision it is supposed to unblock. The app times out and reaches terminal `Failed`, and the dependent role assignments are never deployed (the identity's role list stays empty — the confirming symptom). The placeholder-image trick from ADR-0005 does **not** dodge this: the `registries` *config*, not the image reference, is what triggers the doomed token exchange.

## Decision

Introduce a **user-assigned managed identity** (`<baseName>-id`) as a standalone resource:

- It pre-exists the app, so `AcrPull` (on ACR) and `Storage Blob Data Contributor` (on the storage account) are granted to it with **no dependency on the app**. The app then `dependsOn` those grants, so by its first provision the identity already holds `AcrPull` and the pull secret constructs on the first try.
- The app references the UAMI for both `identity` and `registries[].identity`.
- One identity carries both roles (not a split UAMI-for-ACR / system-assigned-for-Blob), so the app has a single, unambiguous identity. The Blob backup's `DefaultAzureCredential` (`internal/blobbackup/blobbackup.go`) is pointed at it via `AZURE_CLIENT_ID=<uami.clientId>`, set in Bicep — no Go change.

The placeholder bootstrap image and the `/health:8080` probe are unchanged and still needed: ACR holds no image on the first apply, so the app still boots on the public placeholder until CD pushes the real image.

## Considered options

- **Conditional `registries` (keep system-assigned).** Omit ACR from `registries` while bootstrapping so the placeholder app provisions with no ACR secret to construct; the grants then land, and a later step wires ACR + the real image. Rejected: it forces the first deploy to be explicitly two-phase and pushes registry-credential wiring into CD or a second infra apply — the opposite of ADR-0005's "CD only touches the image."
- **ACR admin username/password as a registry secret.** Works immediately, but re-introduces a stored registry credential — exactly what ADR-0005 chose managed identity to avoid. Rejected as a security regression.
- **Anonymous pull on ACR.** Turns a private registry public-read. Rejected.

## Consequences

- The app has one user-assigned identity instead of a system-assigned one. `AZURE_CLIENT_ID` must stay set to the UAMI's client id, or the Blob `DefaultAzureCredential` breaks.
- RBAC propagation can lag a short while after the grant is created; ACA retries the token exchange, so the first provision absorbs it. The deadlock is gone because the grant no longer *follows* the app.
- Deleting and recreating the app keeps the same identity and grants (the UAMI is a separate resource), so a future app rebuild doesn't re-hit the cycle.
