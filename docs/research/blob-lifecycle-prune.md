# Can Blob lifecycle management express the 7-day daily prune?

Research for [issue #33](https://github.com/emepetres/life-ledger/issues/33), under map
[#31](https://github.com/emepetres/life-ledger/issues/31) (scheduled retained backup of the blob
database). **Advisory** input to the ADR and implementation spec — this document establishes what
Azure does and recommends one option; it does not amend the map's fixed constraints.

Facts current as of **2026-07-25**; every behavioural claim is cited to primary Microsoft
documentation, linked inline. Nothing here was tested against a live storage account — the citations
are the evidence, and the two places where the docs are *silent* are called out as such rather than
guessed at.

## The rule we need to express

From map constraint #6: **delete dailies older than 7 days; never touch month-ends.** Month-ends are
kept forever (constraint #6) and are the most valuable objects in the account, so the prune's
correctness requirement is asymmetric: deleting a daily one day late costs nothing; deleting a
month-end costs everything.

Today the container `ledgerbackup` holds exactly one blob, `expenses.db`, overwritten after every
write by `internal/blobbackup` (`Sink.Save` → `UploadFile`, i.e. `Put Blob`/`Put Block List`) and
read back on cold boot ([ADR-0003](../adr/0003-persistence-and-storage.md)). The nightly effort adds
dated snapshot blobs alongside it via a server-side blob-to-blob copy (constraint #4).

## Answer, in one line

**Yes — and it is the right choice.** A single lifecycle rule filtered on the daily prefix with
`daysAfterCreationGreaterThan: 7` expresses the rule exactly, is declarable in Bicep next to the
storage account, and costs nothing. The price is precision: "older than 7 days" becomes "at least
7 days, best-effort, with no deadline", and a policy edit takes **up to 24 hours** to take effect.

---

## 1. Prefix filters combined with an age condition — yes

A lifecycle rule is a filter set plus an action set. Filters are **include-only** and AND-ed
together: *"Filters limit rule actions to a subset of blobs within the storage account by using path
prefixes and blob tags. If more than one filter is defined, a logical AND runs on all filters. You
can use a filter to specify which blobs to include. A filter provides no means to specify which
blobs to exclude."*
([lifecycle-management-overview](https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-overview))

Include-only is normally the awkward part of lifecycle policies — but it is a non-issue here,
**provided the two snapshot classes live under disjoint literal prefixes**. "Never touch month-ends"
is then not a rule that has to be expressed at all; it is a consequence of the daily prefix simply
not matching them.

`prefixMatch` semantics, from
[lifecycle-management-policy-structure](https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-policy-structure)
and the [blob FAQ](https://learn.microsoft.com/en-us/azure/storage/blobs/storage-blob-faq#lifecycle-management-policies):

- *"A prefix string must start with a container name."* The format is `[container name]/[blob name]`.
- Up to **10 case-sensitive prefixes per rule**; *"Prefix strings don't support wildcard matching.
  Characters such as `*` and `?` are treated as string literals."*
- The trailing slash is load-bearing: *"A prefix match string like `container1/` applies to all blobs
  in the container named `container1`. A prefix match string of `container1`, without the trailing
  forward slash character (/), applies to all blobs in all containers where the container name
  begins with the string `container1`."*
- *"A prefix match string of `container1/sub1/` applies to all blobs in the container named
  `container1` that begin with the string `sub1/`."*
- *"The prefix matching operates in a case-sensitive manner."*
- If no `prefixMatch` is given, **the rule applies to every blob in the storage account.**

> **The one dangerous mis-configuration.** The live `expenses.db` sits at the container root. A
> prefix of `ledgerbackup/` — or an omitted `prefixMatch` — would match it, and the policy would
> delete the live database 7 days after its last write. That is a silent, unrecoverable data loss
> with no soft delete enabled (§4). The prefix must be `ledgerbackup/daily/`, with the trailing
> slash, and that line deserves an explicit review checkpoint in the spec.

Limits worth knowing: **up to 100 rules per policy**; *"A lifecycle management policy must be read or
written in full. Partial updates aren't supported."* The policy is a single account-scoped resource,
so the Bicep file becomes the sole source of truth for the whole account's lifecycle configuration —
anything added later in the portal is clobbered on the next `infra.yml` run. For an
apply-Bicep-idempotently repo that is a feature, not a hazard, but it should be written down.

## 2. Which age condition, and what a copy resets

Both conditions are legal on `baseBlob.delete` for block blobs. From the run-conditions table in
[lifecycle-management-policy-structure](https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-policy-structure):

| Condition | Description (verbatim) |
| --- | --- |
| `daysAfterModificationGreaterThan` | *"The age in days after the last modified time blob. Applies to actions on a current version of a blob."* |
| `daysAfterCreationGreaterThan` | *"The age in days after the creation time. Applies to actions on the current version of a blob, the previous version of a blob or a blob snapshot."* |

For a write-once dated blob the two timestamps are the same instant, so the choice looks arbitrary.
It is not — **use `daysAfterCreationGreaterThan`**, because the two properties have different
sensitivity to being disturbed
([Get Blob Properties](https://learn.microsoft.com/en-us/rest/api/storageservices/get-blob-properties)):

- `Last-Modified`: *"The date/time that the blob was last modified. … **Any operation that modifies
  the blob, including an update of the blob's metadata or properties, changes the last modified time
  of the blob.**"*
- `x-ms-creation-time`: *"The date/time when the blob was created."* — no such clause.

Under the modification clock, a stray `Set Blob Metadata` (tagging a snapshot as verified, say) would
silently reset a daily's retention to another 7 days. Creation time is the property that actually
means *"when this snapshot was taken"*, which is the thing the rule is about.

**Does a server-side copy reset them? Yes — on the destination, which is exactly what we want.**
From [Copy Blob](https://learn.microsoft.com/en-us/rest/api/storageservices/copy-blob):

- The `Last-Modified` response header *"Returns the date/time that the copy operation to the
  destination blob finished."*
- The doc enumerates precisely which system properties are inherited by the destination:
  `Content-Type`, `Content-Encoding`, `Content-Language`, `Content-Length`, `Cache-Control`,
  `Content-MD5`, `Content-Disposition`, plus blob-type-specific ones. **Neither `Last-Modified` nor
  creation time is on that list** — the destination does not inherit the source's timestamps.
- *"The `Copy Blob` operation only reads from the source blob"* — copying does not disturb the live
  `expenses.db`'s own timestamps.

So the 7-day clock on a daily starts when the nightly copy lands, not whenever the live database
happened to last be written. That is the behaviour the design needs, and it is the same under either
condition.

> **Gap in the docs, stated rather than guessed.** I could not find a primary statement on whether
> *overwriting an existing blob* (Put Blob or Copy Blob onto an existing name) resets
> `x-ms-creation-time`. It never arises in the happy path, because each daily is written to a unique
> dated name — but it does arise if the workflow is re-dispatched manually on a day that already has
> a snapshot. The spec should not depend on either behaviour: a same-day re-run may or may not
> restart that blob's retention clock, and since the window is best-effort anyway (§3) it does not
> matter. Do not build logic on it.

## 3. Evaluation latency and guarantees — the part that must go in the spec

This is the real cost of choosing lifecycle management, and the ticket is right that it must be
stated up front rather than discovered later. Every quote below is from
[lifecycle-management-overview](https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-overview),
[lifecycle-management-performance-characteristics](https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-performance-characteristics),
or the [blob FAQ](https://learn.microsoft.com/en-us/azure/storage/blobs/storage-blob-faq#lifecycle-management-policies).

**A policy change takes up to a day to matter.**
*"When you add or edit the rules of a lifecycle policy, it can take up to 24 hours for changes to go
into effect and for the first execution to start."* The FAQ repeats it for updates: *"The updated
policy takes up to 24 hours to go into effect."*

**There is no schedule, and no way to observe or trigger one.**
*"An active policy processes objects periodically."* The FAQ is blunt: *"Unfortunately, there's no
way to track the time at which the policy will be executing, as it's a background scheduling
process. … Policies process objects continuously in the background, as required. Priority is given
to requests from workloads."*

**Runs are serialised, with a gap between them.**
*"A new lifecycle management policy run begins only after the ongoing run completes."* And:
*"Lifecycle management can take up to 24 hours to begin processing after the prior run is
completed."*

**A single run does not necessarily finish the job.**
*"In some cases, it can take multiple days to finish processing all the objects in the storage
account."* And: *"Policy conditions are assessed on each object only once during a policy run. In
some cases, an object might meet the condition after it was already assessed by a run. Such objects
are processed in subsequent runs."* The FAQ: *"Depending on the size and the number of objects that
are in a storage account, more than one run might be required to process all of the objects."*

**Microsoft explicitly warns against short windows.**
*"Avoid policy conditions that use a short duration between object creation, modification or last
access time, and the intended operation by the policy."*

### What this means concretely here

`daysAfterCreationGreaterThan: 7` means **"deleted at some point after 7 days"**, not "deleted on day
8". Realistically for this account the dominant term is the up-to-24 h inter-run gap, so expect
deletion around **day 7–9** and a container that usually holds **7–9 dailies, occasionally more**.
The multi-day-run scenarios do not apply — this account holds a handful of tiny objects, and the
whole rule is scoped by prefix, which is the doc's own recommended way to keep runs fast
(*"Narrow the scope of the lifecycle management policy"*).

Three consequences for the spec and runbook:

1. Write **"at least 7 days, best-effort, typically 7–9"** into the spec. Do not write "7 days".
   Nothing downstream — restore runbook, rollback expectations, monitoring — may assume an exact
   count of retained dailies.
2. The 7-day window is comfortably above the 24 h granularity, so the doc's short-duration warning
   does not bite. A 1–2 day window would have been unworkable; 7 is fine.
3. **The rehearsal cannot verify the prune.** After `infra.yml` applies the policy, the first
   execution may be a day away and the first *observable* deletion a week and a day away. The
   rehearsal must verify the **policy resource** (that the rule exists, is enabled, and names the
   right prefix), and schedule one later spot-check of the container contents. Anyone expecting to
   watch a blob disappear in the same session will conclude, wrongly, that it is broken.

Observability, if it is ever wanted: *"You can monitor the outcome of a policy execution by
subscribing to the **LifecyclePolicyCompleted** event and diagnose errors by using metrics and
logs."* That needs Event Grid — a new resource, which map constraint #9 rules out. Not recommended
now; noted as the escape hatch.

## 4. Blob versioning and soft delete — neither helps, and one is a cost trap

The ticket's suspicion is confirmed by the docs, emphatically.

**Both features are account-wide, not per-container.** Versioning is a property of the blob *service*
(`Microsoft.Storage/storageAccounts/blobServices` → `isVersioningEnabled`), enabled *"for the storage
account"*
([versioning-enable](https://learn.microsoft.com/en-us/azure/storage/blobs/versioning-enable)); soft
delete likewise sets a retention policy on the same account-level resource
([soft-delete-blob-overview](https://learn.microsoft.com/en-us/azure/storage/blobs/soft-delete-blob-overview)).
There is no way to turn either on "just for the snapshot prefix". Life Ledger has one storage
account, and it holds the hot `expenses.db`.

**Versioning on this account would be expensive and pointless.** From
[versioning-overview](https://learn.microsoft.com/en-us/azure/storage/blobs/versioning-overview):

- *"After you enable blob versioning for a storage account, **every write operation to a blob in that
  account results in the creation of a new version.** For this reason, enabling blob versioning may
  result in additional costs."* The app writes after **every mutation** (ADR-0003) — so every expense
  entered or edited mints a version.
- It is not cheap deduplicated versioning, because `VACUUM INTO` rewrites the whole file and
  `UploadFile` replaces the whole blob: *"The `Put Blob` operation … replaces the entire contents of
  a blob and so may lead to additional charges"*, and in the doc's scenario 4, *"the current version
  has been completely updated and contains none of its original blocks. As a result, the account is
  charged for all eight unique blocks."* **Each version costs a full copy of the database.**
- *"Microsoft recommends maintaining fewer than 1000 versions per blob"*, and *"Having a large number
  of versions per blob can increase the latency for blob listing operations."* At a handful of writes
  a day that ceiling arrives in months.
- Microsoft's own mitigation is the shape of the answer: *"Enabling versioning for data that is
  frequently overwritten may result in increased storage capacity charges and increased latency
  during listing operations. To mitigate these concerns, **store frequently overwritten data in a
  separate storage account with versioning disabled.**"*

Versioning also cannot express the retention rule even if it were free: version delete conditions are
creation-age only and uniform across all versions of a blob, so there is no way to say "these forever,
those for 7 days". And versions are unlabelled and ungated — they would faithfully capture a corrupt
write, which is precisely what map constraint #5 (gated copy, integrity check first) exists to
prevent. Versioning is *automatic history*; the effort needs *curated, verified, date-labelled
snapshots*. Different things.

**Soft delete has the same overwrite trap, plus one useful property.** From
[soft-delete-blob-overview](https://learn.microsoft.com/en-us/azure/storage/blobs/soft-delete-blob-overview):
*"When blob soft delete is enabled, overwriting a blob automatically creates a soft-deleted snapshot
of the blob's state prior to the write operation"*; *"All soft-deleted data is billed at the same
rate as active data"*; and *"Enabling soft delete for frequently overwritten data may result in
increased storage capacity charges and increased latency when listing blobs."* Retention is 1–365
days. The genuinely useful property is that it is an undo for an accidental delete — including a
lifecycle delete: *"Lifecycle management policies that delete objects in a storage account with
soft-delete enabled will put the object in a soft-deleted state that is retained for the duration of
the soft-delete."*

**Recommendation: leave both off**, and record the reasoning so it is not relitigated. If a later
ticket wants soft delete as a safety net over the snapshot container, its precondition is moving the
hot `expenses.db` onto its own storage account — which is Microsoft's own advice above, and which
folds neatly into the map's open "container topology" question.

**One operational gotcha to carry forward either way**, from the FAQ: *"I don't see capacity changes
even though the policy is executing and deleting the blobs — Check to see if data protection features
such as soft delete or versioning are enabled."* If either is ever switched on, the prune stops
actually freeing anything. Also note *"The delete action of a lifecycle management policy won't work
with any blob that is in a soft-deleted state."*

## 5. Bicep — yes, natively

`Microsoft.Storage/storageAccounts/managementPolicies` is a first-class child resource of the storage
account
([template reference](https://learn.microsoft.com/en-us/azure/templates/microsoft.storage/storageaccounts/managementpolicies)).
The name **must** be exactly `'default'`. `blobTypes` is required; `prefixMatch` is optional. API
versions run from `2018-03-01-preview` to `2026-04-01`; `2023-05-01` exists and matches what
`infra/main.bicep` already pins for its storage resources.

Dropped in next to the existing `backupContainer`:

```bicep
// Prune rule (map #31 constraint 6): dailies age out after 7 days; month-ends are
// never matched, because the filter names only the daily prefix and lifecycle
// filters are include-only. The trailing slash is load-bearing — 'ledgerbackup/'
// would match the live expenses.db at the container root and delete it.
resource lifecycle 'Microsoft.Storage/storageAccounts/managementPolicies@2023-05-01' = {
  parent: storage
  name: 'default'
  properties: {
    policy: {
      rules: [
        {
          name: 'pruneDailySnapshots'
          type: 'Lifecycle'
          enabled: true
          definition: {
            filters: {
              blobTypes: [ 'blockBlob' ]
              prefixMatch: [ '${backupContainerName}/daily/' ]
            }
            actions: {
              baseBlob: {
                delete: {
                  daysAfterCreationGreaterThan: 7
                }
              }
            }
          }
        }
      ]
    }
  }
}
```

This implies a blob layout with **disjoint literal prefixes** — e.g. `daily/2026-07-25.db` and
`monthly/2026-06.db` (the month-end labelled by the month it represents, per constraint #7), with the
live `expenses.db` left at the container root. The lifecycle rule then only ever names `daily/`.

**Permissions: none new.** This is a control-plane resource, deployed by `infra.yml` with the same
OIDC identity that already holds `Contributor` + `User Access Administrator` on the resource group
([first-deploy.md](../deployment/first-deploy.md)). **Cost: none.** *"Lifecycle management policies
are free of charge. … Delete operations are free."*

Verification after a deploy (the policy resource, not its effect — see §3):

```pwsh
az storage account management-policy show `
  --account-name $StorageAccount `
  --resource-group $ResourceGroup `
  --query "policy.rules[].{rule:name, prefixes:definition.filters.prefixMatch, deleteAfterDays:definition.actions.baseBlob.delete.daysAfterCreationGreaterThan, enabled:enabled}" `
  -o table
```

Later spot-check of what actually survived:

```pwsh
az storage blob list `
  --account-name $StorageAccount `
  --container-name ledgerbackup `
  --prefix "daily/" `
  --auth-mode login `
  --query "sort_by([].{name:name, created:properties.creationTime}, &created)" `
  -o table
```

## 6. The alternative: pruning inside the nightly workflow

The CLI supports it directly
([az storage blob delete-batch](https://learn.microsoft.com/en-us/cli/azure/storage/blob#az-storage-blob-delete-batch)):

```pwsh
$cutoff = (Get-Date).ToUniversalTime().AddDays(-7).ToString("yyyy-MM-ddTHH:mmZ")
az storage blob delete-batch `
  --source ledgerbackup `
  --account-name $StorageAccount `
  --pattern "daily/*" `
  --if-unmodified-since $cutoff `
  --auth-mode login
```

**Permissions.** Blob deletion is a *data-plane* operation, and Resource Manager roles do not grant
it: *"When you create an Azure Storage account, you aren't automatically assigned permissions to
access data via Microsoft Entra ID. You must explicitly assign yourself an Azure role for Azure
Storage."*
([assign-azure-role-data-access](https://learn.microsoft.com/en-us/azure/storage/blobs/assign-azure-role-data-access))
The needed action is `Microsoft.Storage/storageAccounts/blobServices/containers/blobs/delete`, whose
least-privileged built-in role is **Storage Blob Data Contributor**
(`ba92f5b4-2d11-453d-a403-e96b0029c9fe`)
([built-in roles](https://learn.microsoft.com/en-us/azure/role-based-access-control/built-in-roles/storage)).

Note honestly that this is **not** a differentiator between the two options: the nightly copy itself
already needs data-plane access — Copy Blob's destination requires `blobs/write` (or `blobs/add`) and
the source `blobs/read`, *"Least privileged built-in role: Storage Blob Data Contributor"* — and
there is no built-in blob role that grants write without delete. Whichever prune we pick, the CI
identity ends up holding a role that *could* delete. What differs is whether a script actually
exercises that verb every night.

The one thing to avoid: dropping `--auth-mode login`. The default *"legacy 'key' mode … will attempt
to query for an account key"*, which the `Contributor` identity can retrieve — reintroducing account
keys into CI, which this project has deliberately avoided everywhere (`main.bicep`: *"no account key
is ever handled"*).

**Failure modes.**

- **A glob or date bug is unrecoverable.** The CLI's `--pattern` is fnmatch, and the doc warns:
  *"When you use `*` in --pattern, it will match any character **including the directory separator
  `/`**."* So `daily*` also matches `daily-archive/...`, and a bare `*` matches the month-ends and the
  live database. With no soft delete on (§4), a bad pattern shipped at 01:00 UTC destroys the
  forever-snapshots. The threat this whole effort exists to counter (constraint #1) is
  *development-induced data damage* — a nightly delete loop written by that same development is an
  odd shape for the mitigation to take.
- **`--dryrun` does not preview the real selection**: *"If specified, it will ignore all the
  Precondition Arguments that include --if-modified-since and --if-unmodified-since."* So the safe
  rehearsal shows a *superset* of what the real command would delete — the exact opposite of what a
  dry run is for.
- **`--if-unmodified-since` keys off last-modified, not creation**, so it carries the metadata-touch
  sensitivity from §2, plus a UTC cutoff the workflow must compute correctly in both seasons.
- **Shared failure domain with the copy.** If the nightly run stops — including via the map's named
  accepted risk #8, GitHub disabling `schedule:` after 60 days of repo inactivity — the prune stops
  with it. That direction is benign (dailies accumulate; storage is cents). The bad direction is
  ordering: a prune that runs when the copy failed shrinks the window. Copy first, prune only on
  success.

**What it buys.** Determinism and immediacy: exactly 7 dailies, deleted the moment they age out,
visible in the run log, testable against a scratch container, with no 24-hour opacity and no
dependence on a background service that cannot be observed or triggered.

## Recommendation

**Use a Blob lifecycle management policy, declared in Bicep, filtered on `ledgerbackup/daily/` with
`daysAfterCreationGreaterThan: 7`.**

Why:

1. **It expresses the rule exactly and declaratively** — one rule, one literal prefix, one integer,
   reviewed once in a pull request. Include-only filtering turns "never touch month-ends" from a
   thing the code must remember into a thing the configuration cannot express.
2. **It keeps `delete` out of the nightly workflow.** The workflow's only verb against the backup
   container becomes *write*. The CI identity will hold a role that could delete either way, but the
   blast radius of a bug is bounded by what the code does, not by what the token permits — and the
   threat model that motivates this whole effort is exactly "development broke something at 3 a.m.".
3. **It is free and adds no resource** — no new service, no networking, ~$0, satisfying constraint
   #9. Lifecycle policies are free of charge and delete actions are free.
4. **It fails independently of GitHub.** If the cron is disabled after 60 days of inactivity, the
   prune keeps working; if lifecycle is slow, dailies merely accumulate at a cost of cents.

**The trade-off, plainly:** you give up precision and immediacy. "Older than 7 days" becomes "at
least 7 days, typically 7–9, with no guaranteed deadline", and any change to the policy takes up to
24 hours to take effect — so the prune can never be verified in the same session that deploys it. The
spec must state the window as approximate, and the rehearsal must check the policy resource plus a
deferred spot-check rather than an immediate deletion. If a future requirement ever demands an exact
retained-daily count, that is the moment to move the prune into the workflow — not before.

**And leave blob versioning and soft delete off**, for the reasons in §4, recording that decision so
the next session does not reopen it.

## Sources

All Microsoft Learn, retrieved 2026-07-25:

- [Blob Storage lifecycle management overview](https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-overview)
- [Lifecycle management policy structure](https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-policy-structure)
- [Lifecycle management performance characteristics](https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-performance-characteristics)
- [Azure Blob Storage FAQ — lifecycle management policies](https://learn.microsoft.com/en-us/azure/storage/blobs/storage-blob-faq#lifecycle-management-policies)
- [Microsoft.Storage/storageAccounts/managementPolicies template reference](https://learn.microsoft.com/en-us/azure/templates/microsoft.storage/storageaccounts/managementpolicies)
- [Blob versioning](https://learn.microsoft.com/en-us/azure/storage/blobs/versioning-overview) and [Enable and manage blob versioning](https://learn.microsoft.com/en-us/azure/storage/blobs/versioning-enable)
- [Soft delete for blobs](https://learn.microsoft.com/en-us/azure/storage/blobs/soft-delete-blob-overview)
- [Copy Blob (REST API)](https://learn.microsoft.com/en-us/rest/api/storageservices/copy-blob)
- [Get Blob Properties (REST API)](https://learn.microsoft.com/en-us/rest/api/storageservices/get-blob-properties)
- [az storage blob delete-batch](https://learn.microsoft.com/en-us/cli/azure/storage/blob#az-storage-blob-delete-batch)
- [Azure built-in roles for Storage](https://learn.microsoft.com/en-us/azure/role-based-access-control/built-in-roles/storage)
- [Assign an Azure role for blob data access](https://learn.microsoft.com/en-us/azure/storage/blobs/assign-azure-role-data-access)

Repo context: [ADR-0003](../adr/0003-persistence-and-storage.md),
[`internal/blobbackup/blobbackup.go`](../../internal/blobbackup/blobbackup.go),
[`infra/main.bicep`](../../infra/main.bicep),
[`docs/deployment/first-deploy.md`](../deployment/first-deploy.md).
