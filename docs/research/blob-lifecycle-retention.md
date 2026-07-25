# Can Blob lifecycle management express the 7-day daily prune?

Research for [issue #33](https://github.com/emepetres/life-ledger/issues/33), under
[map #31](https://github.com/emepetres/life-ledger/issues/31). **Advisory** input to the backup ADR — this
document establishes what the platform does and recommends one option; the decision is recorded elsewhere.

Facts current as of **2026-07-25**, every claim cited to a primary Microsoft source (learn.microsoft.com,
the Azure REST/Bicep schema reference, or the `Azure/azure-cli` source). Nothing here was verified against a
live storage account; the CLI behaviours are read out of the shipping source, not observed.

## The rule being tested

From map #31, constraint 6: **delete dailies older than 7 days; never touch month-ends.** Naming (from the
open topology ticket [#32](https://github.com/emepetres/life-ledger/issues/32)) is expected to be
`daily/<YYYY-MM-DD>.db` and `monthly/<YYYY-MM>.db` inside the existing `ledgerbackup` container, alongside
the live `expenses.db` that `internal/blobbackup` overwrites after **every** app write
([ADR-0003](../adr/0003-persistence-and-storage.md)). Snapshots are made by a server-side blob-to-blob copy
(`az storage blob copy start`), gated on an integrity check, from a nightly GitHub Actions cron.

**Short answer: yes, lifecycle management can express the rule — and you should still not use it.** The
reason is not expressiveness, it is coupling and observability. See [Recommendation](#recommendation).

## 1. Prefix filters + an age condition — yes, and it is the documented shape

A lifecycle policy is a set of rules, each a **filter set** plus an **action set**. Filters are
include-only: *"Filters limit rule actions to a subset of blobs within the storage account by using path
prefixes and blob tags. If more than one filter is defined, a logical AND runs on all filters. You can use a
filter to specify which blobs to include. A filter provides no means to specify which blobs to exclude."*
([lifecycle-management-overview](https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-overview))

That include-only limitation is **not** a problem here, because `daily/` and `monthly/` are disjoint
prefixes: matching `daily/` never reaches a month-end. Had the two shared a prefix, the rule would have been
inexpressible, since there is no "except" clause. This is the sense in which the prefix scheme is
load-bearing — #32 must keep the two sets prefix-disjoint.

The prefix filter itself:

- *"If you apply the **prefixMatch** filter, then each rule can define up to 10 case-sensitive prefixes.
  **A prefix string must start with a container name.**"*
- *"Prefix strings don't support wildcard matching. Characters such as `*` and `?` are treated as string
  literals."*
  ([lifecycle-management-policy-structure](https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-policy-structure))

So the prefix for our dailies is `ledgerbackup/daily/`, **including the container name**, and the trailing
slash matters: the FAQ warns that *"A prefix match string of `container1`, without the trailing forward
slash character (/), applies to all blobs in all containers where the container name begins with the string
`container1`"*
([storage-blob-faq](https://learn.microsoft.com/en-us/azure/storage/blobs/storage-blob-faq)).

### Which age condition applies to a block blob delete

Both `daysAfterModificationGreaterThan` and `daysAfterCreationGreaterThan` are valid on
`actions.baseBlob.delete`; the Bicep schema types that property as `DateAfterModification`, which carries
`daysAfterCreationGreaterThan`, `daysAfterLastAccessTimeGreaterThan`, `daysAfterLastTierChangeGreaterThan`
and `daysAfterModificationGreaterThan`
([Microsoft.Storage/storageAccounts/managementPolicies](https://learn.microsoft.com/en-us/azure/templates/microsoft.storage/storageaccounts/managementpolicies)).
The semantics:

| Condition | Documented meaning |
| --- | --- |
| `daysAfterModificationGreaterThan` | *"The age in days after the last modified time blob. Applies to actions on a current version of a blob."* |
| `daysAfterCreationGreaterThan` | *"The age in days after the creation time. Applies to actions on the current version of a blob, the previous version of a blob or a blob snapshot."* |

(both from
[lifecycle-management-policy-structure](https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-policy-structure);
`baseBlob` *"refers to the current version of a blob"*, per
[lifecycle-management-policy-delete](https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-policy-delete))

### What a server-side copy resets — and why it doesn't matter here

`Copy Blob` sets the destination's `Last-Modified` to *"the date/time that the copy operation to the
destination blob finished"*
([Copy Blob](https://learn.microsoft.com/en-us/rest/api/storageservices/copy-blob)), and `Last-Modified` in
general is bumped by *"any operation that modifies the blob, including an update of the blob's metadata or
properties"*
([Get Blob Properties](https://learn.microsoft.com/en-us/rest/api/storageservices/get-blob-properties)).
`x-ms-creation-time` is documented only as *"The date/time when the blob was created"* — the REST reference
does **not** state what happens to it when an existing blob is overwritten, so treat that as unspecified
rather than assuming either behaviour.

For **this** design the divergence is moot, and that is worth stating in the spec rather than reasoning
about it again later: each daily blob has a **unique name and is written exactly once**, by a copy into a
name that did not previously exist. Creation time, last-modified time and "the moment the snapshot was
taken" are the same instant, so the two conditions are indistinguishable. The only way they could diverge is
a same-day re-run (`workflow_dispatch`) overwriting `daily/<today>.db` — which moves the age clock by hours,
not days, under either reading.

If a lifecycle rule is used anyway, prefer **`daysAfterModificationGreaterThan`**: its behaviour on
overwrite is documented, and every documented way of touching a dated blob after creation only pushes
deletion *later* (fail-safe), never earlier. The 365-day example in the official delete-policy article uses
the same condition
([lifecycle-management-policy-delete](https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-policy-delete)).

## 2. Evaluation latency: "7 days" really is 7–9 days, and the ceiling is not guaranteed

This is the part that must go in the spec. There are three separate delays, all documented:

1. **Policy activation.** *"When you add or edit the rules of a lifecycle policy, it can take up to 24 hours
   for changes to go into effect and for the first execution to start."*
   ([lifecycle-management-overview](https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-overview))
   The FAQ repeats it for both creation and update: *"Once you configure a policy, it can take up to 24
   hours to go into effect"* / *"The updated policy takes up to 24 hours to go into effect."*
   ([storage-blob-faq](https://learn.microsoft.com/en-us/azure/storage/blobs/storage-blob-faq))
2. **Gap between runs.** *"Lifecycle management can take up to 24 hours to begin processing after the prior
   run is completed."* and *"A new lifecycle management policy run begins only after the ongoing run
   completes."*
   ([lifecycle-management-performance-characteristics](https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-performance-characteristics))
3. **Run duration.** *"In some cases, it can take multiple days to finish processing all the objects in the
   storage account."* and *"Policy conditions are assessed on each object only once during a policy run. In
   some cases, an object might meet the condition after it was already assessed by a run. Such objects are
   processed in subsequent runs."* (same source)

Microsoft explicitly declines to guarantee timing: *"there's no way to track the time at which the policy
will be executing, as it's a background scheduling process… Policies process objects continuously in the
background, as required. Priority is given to requests from workloads."*
([storage-blob-faq](https://learn.microsoft.com/en-us/azure/storage/blobs/storage-blob-faq)) There is no
per-run schedule and no SLA on deletion latency.

**The real number for this account.** The condition is strictly *greater than* 7 days, so the earliest a
daily can be deleted is day 8. Add up to 24 h before the next run picks it up. Run duration is negligible
here — the filtered set is ~7–15 tiny blobs, and Microsoft's own guidance is that a narrow prefix filter is
what keeps runs fast. So: **dailies live 7–9 days in practice, with no documented upper bound.** For this
map that is harmless (constraint 6: rollback granularity of one day, 7-day window is *enough*, month-ends
kept forever), but it means the invariant "there are exactly 7 dailies" is **not** something a lifecycle
policy can hold. It can only hold "there are at least 7, and probably 8 or 9, and occasionally more."

Two consequences worth naming:

- The performance guidance says outright to *"avoid policy conditions that use a short duration between
  object creation, modification or last access time, and the intended operation by the policy."* A 7-day
  window is short by that standard.
- Success is invisible by default. To know a run happened you must subscribe to the
  **`LifecyclePolicyCompleted`** event or read storage resource logs
  ([lifecycle-management-overview](https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-overview)).
  Both are new plumbing that map #31 constraint 9 rules out (no new runtime service), and neither hooks into
  the "fail loudly and open an issue" story of constraint 8.

## 3. Bicep: yes, one `managementPolicies` resource per account, named `default`

The resource type is `Microsoft.Storage/storageAccounts/managementPolicies`, a child of the storage account,
and the schema pins the name: **`name` is `'default'` (required)**. Latest listed API versions run to
`2026-04-01`; `2023-05-01` exists and matches the api-version already used throughout `infra/main.bicep`.
([Microsoft.Storage/storageAccounts/managementPolicies](https://learn.microsoft.com/en-us/azure/templates/microsoft.storage/storageaccounts/managementpolicies))

Because the name is fixed, **there is exactly one policy resource per storage account** and every rule (up
to 100) lives inside it. A partial update is not possible: *"A lifecycle management policy must be read or
written in full."*
([lifecycle-management-overview](https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-overview))
Since Bicep declares the whole resource, that is a natural fit — but it also means the policy is an
**account-scoped delete rule**, not a container-scoped one, which is the blast-radius point in
[#32](https://github.com/emepetres/life-ledger/issues/32): a mistyped prefix (say, dropping the trailing
slash, or `ledgerbackup/dail`) has the whole account in range, including the live `expenses.db`.

Minimal working snippet, sized to this repo (`storage` and `backupContainer` are the existing symbolic names
in `infra/main.bicep`):

```bicep
// Prune dated dailies after 7 days. Month-ends under 'monthly/' are never
// matched — lifecycle filters are include-only, so the two prefixes must stay
// disjoint. The name MUST be 'default': one policy resource per account.
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
              blobTypes: ['blockBlob']
              // Prefix MUST start with the container name; the trailing slash
              // is load-bearing (without it this also matches sibling
              // containers whose names start with 'ledgerbackup').
              prefixMatch: ['${backupContainer.name}/daily/']
            }
            actions: {
              baseBlob: {
                delete: {
                  daysAfterModificationGreaterThan: 7
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

Applying it needs no new CI permission beyond what `infra.yml` already has — it is a control-plane write in
the same resource group, deployed by the existing OIDC principal.

## 4. Versioning and soft delete: one is a trap, the other is an orthogonal safety net

### Blob versioning — do not enable

The caution is on the front of both the versioning and soft-delete articles: *"After you enable blob
versioning for a storage account, **every write operation to a blob in that account results in the creation
of a new version.**"*
([versioning-overview](https://learn.microsoft.com/en-us/azure/storage/blobs/versioning-overview)) It is a
**storage-account-level** switch — it cannot be scoped to `daily/`, so it necessarily applies to the live
`expenses.db` that is overwritten after every single expense entry.

Quantifying the surprise honestly, in two parts:

- **Money: negligible, and that is not the reason to avoid it.** Versions bill *"at the same rate as active
  data"*, on unique blocks. But `internal/blobbackup` uploads a whole `VACUUM INTO` file, and the docs are
  explicit that *"The `Put Blob` operation… replaces the entire contents of a blob and so may lead to
  additional charges"* — scenario 4 in the billing section is exactly this case: *"the current version has
  been completely updated and contains none of its original blocks. As a result, the account is charged for
  all eight unique blocks."* So each version bills at the **full database size**, no block sharing. At a
  ~100 KiB database and a handful of writes a day, a year of versions is on the order of 0.2 GB — cents.
- **Complexity and latency: the actual cost.** *"Having a large number of versions per blob can increase the
  latency for blob listing operations. Microsoft recommends maintaining fewer than 1000 versions per
  blob."* At five writes a day the live blob crosses 1000 versions in roughly seven months, and the
  mitigation Microsoft names is *"store frequently overwritten data in a separate storage account with
  versioning disabled"* — i.e. a new storage account, which is precisely the complexity this effort avoids.

Worse, versioning **breaks the prune rule it is supposed to help with**: *"A lifecycle management policy will
not delete the current version of a blob until any previous versions or snapshots associated with that blob
have been deleted. If blobs in your storage account have previous versions or snapshots, then you must
include previous versions and snapshots when you specify a delete action as part of the policy."*
([lifecycle-management-policy-structure](https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-policy-structure))
The FAQ documents the resulting confusion directly: *"I don't see capacity changes even though the policy is
executing and deleting the blobs — Check to see if data protection features such as soft delete or
versioning are enabled."*
([storage-blob-faq](https://learn.microsoft.com/en-us/azure/storage/blobs/storage-blob-faq)) And deleting a
previous version needs a **stronger role** than deleting a blob:
`…/blobs/deleteBlobVersion/action`, whose least-privileged built-in role is **Storage Blob Data Owner**, not
Contributor
([versioning-overview](https://learn.microsoft.com/en-us/azure/storage/blobs/versioning-overview)) — which
cuts directly against #32's goal of *narrowing* what the CI principal can do.

**Verdict: versioning gives none of the retention story for free and actively interacts badly with the
design.** It protects the live blob (which the dated snapshots already do, deliberately and legibly), and it
would silently accumulate one full-size version per expense entry on an account-wide switch.

### Soft delete — free-ish, orthogonal, and it changes the prune arithmetic

Soft delete *"protects an individual blob, snapshot, or version from accidental deletes **or overwrites** by
maintaining the deleted data in the system for a specified period of time"*, with a retention period
*"of between 1 and 365 days"*
([soft-delete-blob-overview](https://learn.microsoft.com/en-us/azure/storage/blobs/soft-delete-blob-overview)).
That overwrite protection is real and covers a threat the dated snapshots cover only at 24 h granularity:
*"When blob soft delete is enabled, overwriting a blob automatically creates a soft-deleted snapshot of the
blob's state prior to the write operation."* Enabling it is one Bicep property on `blobServices/default`.

But it is not a substitute for the dated snapshots, and it is not free of consequences:

- Same accumulation shape as versioning — one soft-deleted snapshot per overwrite of `expenses.db`, i.e.
  per expense entry — bounded by the retention window instead of forever. *"All soft-deleted data is billed
  at the same rate as active data."* Microsoft's own warning: *"Enabling soft delete for frequently
  overwritten data may result in increased storage capacity charges and increased latency when listing
  blobs."*
- Recovery is not browsable. Soft-deleted objects *"are invisible unless they're explicitly displayed or
  listed"* and are restored via `Undelete Blob`. There is no "the database as of 2026-07-18" you can point
  at — which is the whole product of this effort.
- **It changes what the prune means.** *"Lifecycle management policies that delete objects in a storage
  account with soft-delete enabled will put the object in a soft-deleted state that is retained for the
  duration of the soft-delete"*, and *"The delete action of a lifecycle management policy won't work with
  any blob that is in a soft-deleted state."*
  ([lifecycle-management-overview](https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-overview))
  So a pruned daily is not gone — it lingers, billed, for the retention period. With a 7-day retention the
  effective daily footprint roughly doubles. Cheap here, but it must be stated, not discovered.
- It does **not** cover the account-deletion case: *"Blob soft delete does not protect against the deletion
  of a storage account."*

**Verdict: soft delete is a cheap extra safety net for the live blob, worth a separate yes/no, and it
removes no part of the prune.** If it is adopted, the spec must say that pruned dailies persist for the
retention period.

### Ruled out: `Set Blob Expiry`

A per-blob TTL would be the ideal primitive — set an expiry at copy time and never prune at all. It is
hierarchical-namespace only: `Set Blob Expiry` appears solely in the HNS row of the soft-delete behaviour
table, and `x-ms-expiry-time` is *"Only for accounts with a hierarchical namespace enabled"*
([soft-delete-blob-overview](https://learn.microsoft.com/en-us/azure/storage/blobs/soft-delete-blob-overview),
[Get Blob Properties](https://learn.microsoft.com/en-us/rest/api/storageservices/get-blob-properties)). The
account in `infra/main.bicep` is `StorageV2` with a flat namespace. Not available.

## 5. Pruning in the nightly workflow

### Permissions

Data-plane only, and modest. Per the REST reference's own per-operation permission tables:

| Operation | Azure RBAC data action | Least-privileged built-in role |
| --- | --- | --- |
| [List Blobs](https://learn.microsoft.com/en-us/rest/api/storageservices/list-blobs) | `Microsoft.Storage/storageAccounts/blobServices/containers/blobs/read` | Storage Blob Data Reader |
| [Get Blob Properties](https://learn.microsoft.com/en-us/rest/api/storageservices/get-blob-properties) | `…/containers/blobs/read` | Storage Blob Data Reader |
| [Copy Blob](https://learn.microsoft.com/en-us/rest/api/storageservices/copy-blob) — destination | `…/containers/blobs/write` (or `…/blobs/add/action`) | Storage Blob Data Contributor |
| [Copy Blob](https://learn.microsoft.com/en-us/rest/api/storageservices/copy-blob) — source, same account | `…/containers/blobs/read` | Storage Blob Data Reader |
| [Delete Blob](https://learn.microsoft.com/en-us/rest/api/storageservices/delete-blob) | `…/containers/blobs/delete` | Storage Blob Data Contributor |

So the nightly workflow needs **Storage Blob Data Contributor** — which is the union of read + write +
delete — and nothing stronger. `deleteBlobVersion/action` (Storage Blob Data Owner) is needed **only** if
versioning is enabled, which §4 recommends against; that is one more reason to leave versioning off.
Crucially, that assignment can be **scoped to the container** rather than the account, unlike the lifecycle
policy, which is account-scoped by construction. The role GUID for Storage Blob Data Contributor is
`ba92f5b4-2d11-453d-a403-e96b0029c9fe` — already a `var` in `infra/main.bicep`, though there scoped to the
whole account for the app's identity, which #32 will want to revisit.

The CI principal already authenticates by OIDC (`azure/login@v2`, ADR-0005), so `--auth-mode login` works
with no stored key; the account has `allowBlobPublicAccess: false` and no key is handled anywhere today.

### Failure modes — one of them is severe

**`az storage blob delete-batch` silently succeeds when it fails.** This is a real hazard, not a
theoretical one. In the shipping CLI source, the per-blob delete is wrapped as:

```python
try:
    container_client.delete_blob(**delete_blob_args)
    return blob_name
except HttpResponseError as ex:
    logger.debug(ex.exc_msg)
    return None
```

and the command then reports failures only as a warning —
`'%s of %s blobs not deleted due to "Failed Precondition"'` — before returning normally
([`azure-cli` `.../storage/operations/blob.py`, `storage_blob_delete_batch`](https://github.com/Azure/azure-cli/blob/dev/src/azure-cli/azure/cli/command_modules/storage/operations/blob.py)).
`HttpResponseError` covers **403 Forbidden** and **5xx** as well as the 412 the message names, and the
detail goes to `logger.debug`. A missing role assignment therefore produces a green step with a misleading
warning and **exit code 0**. Under a "fail loudly" mandate (map #31 constraint 8) that is disqualifying on
its own.

Other failure modes, in rough order of how likely they are to bite:

- **Glob semantics are not path-aware.** In `--pattern`, *"`*` matches any character including directory
  separator `/`"*
  ([az storage blob delete-batch](https://learn.microsoft.com/en-us/cli/azure/storage/blob)). A pattern
  intended as "one level under `daily/`" happily descends further; a pattern with a leading `*` reaches
  outside `daily/` entirely. (The CLI does derive a server-side `name_starts_with` from the literal leading
  segment of the pattern, so `daily/*` at least lists efficiently — but the matching itself is still a plain
  glob.)
- **`--dryrun` does not rehearse what will run.** The docs state it *"ignores time-based preconditions"*,
  and the source confirms the dry-run path filters on `last_modified` client-side instead of exercising the
  delete. A clean `--dryrun` is therefore weak evidence.
- **Clock and timezone.** The cutoff must be computed in UTC. The cron is UTC (constraint 3) and blob
  timestamps are UTC; a local-time cutoff on a Madrid-configured runner is a silent ±1–2 h error, which at
  day granularity can prune one day early.
- **Partial completion.** A run that dies mid-loop leaves extra dailies. Harmless (they are pruned next
  night) but it means "exactly 7" is an eventual property, not an invariant — the same caveat lifecycle has,
  only with a much shorter tail.
- **A wrong cutoff deletes too much.** The one genuinely dangerous outcome, and the one the workflow can
  defend against and a lifecycle policy cannot: refuse to delete anything outside the `daily/` prefix, and
  refuse to delete more than a sanity bound in one run.

### Shape of the safe version

List, filter in the runner, delete one at a time, check each exit code — the opposite of `delete-batch`'s
swallow-and-continue. Sketch (PowerShell Core, per AGENTS.md):

```pwsh
$ErrorActionPreference = 'Stop'
$Account   = $Env:STORAGE_ACCOUNT
$Container = 'ledgerbackup'
$Cutoff    = (Get-Date).ToUniversalTime().AddDays(-7)

# Server-side prefix filter: month-ends under monthly/ are never listed.
$Dailies = az storage blob list `
  --account-name $Account --container-name $Container `
  --prefix 'daily/' --auth-mode login -o json | ConvertFrom-Json

$Stale = $Dailies | Where-Object { [datetime]$_.properties.lastModified -lt $Cutoff }

# Guard rails: never leave the prefix, never mass-delete on a bad cutoff.
if ($Stale.Count -gt 10) { throw "refusing to prune $($Stale.Count) dailies" }
foreach ($b in $Stale) {
  if (-not $b.name.StartsWith('daily/')) { throw "refusing to delete $($b.name)" }
  az storage blob delete `
    --account-name $Account --container-name $Container `
    --name $b.name --auth-mode login
  if ($LASTEXITCODE -ne 0) { throw "delete failed for $($b.name)" }
}
```

Roughly a dozen lines, no new dependency, one exit code, and it runs inside the same gated nightly job that
already knows how to fail loudly.

## Recommendation

**Prune inside the nightly workflow. Do not use a lifecycle management policy.**

Lifecycle management *can* express the rule — prefix filter plus a 7-day age condition, in ~20 lines of
declarative Bicep, applied by the workflow that already exists. On expressiveness it wins. It loses on four
things that matter more to this specific map:

1. **Coupling — the decisive argument.** A lifecycle policy deletes on the platform's clock, unconditionally
   and forever. The nightly *creation* of snapshots does not. Map #31 already names the failure that makes
   this bite: GitHub disables `schedule:` triggers after 60 days of repo inactivity (constraint 8, an
   accepted risk). With lifecycle pruning, a dead cron means snapshots stop being created while the platform
   keeps deleting them — seven days later there are **zero** dailies, silently. With workflow pruning, a
   dead cron means no prune either: you are left with seven stale-but-present snapshots. One failure mode
   degrades to "old backups", the other to "no backups". For a feature whose entire purpose is rollback,
   only one of those is acceptable.
2. **Observability.** The workflow prune reports through an exit code, inside the run that constraint 8
   already wires to "fail loudly and open an issue". Lifecycle reports through `LifecyclePolicyCompleted`
   Event Grid events or resource logs — new plumbing, ruled out by constraint 9, and Microsoft states
   outright that there is no way to know when a policy will run.
3. **Determinism.** Lifecycle gives 7–9 dailies with no documented upper bound and an explicit warning
   against short windows. The workflow gives exactly the intended set, at a known instant, immediately after
   the copy that justified it.
4. **Blast radius.** The `managementPolicies` resource is `name: 'default'`, account-scoped, and a prefix
   typo puts the live `expenses.db` in range with no second gate. The workflow's data-plane role can be
   container-scoped, and the loop can assert the prefix before every delete — a guard a policy has no way to
   express.

**The trade-off, stated plainly.** Lifecycle is genuinely less code and zero runtime logic: no loop to get
wrong, no cutoff arithmetic, no CLI quirks, and it keeps working if the workflow is deleted. Choosing the
workflow means owning ~12 lines of `pwsh` that delete data — the highest-consequence code in this effort —
plus the discovery that `az storage blob delete-batch` cannot be used as-is because it exits 0 on
authorization failures. That is a real cost. It is worth paying because the prune must be *conditional on
the backup still happening*, and a lifecycle policy has no way to express that condition.

**Also decided by the above:** leave **blob versioning off** (account-wide, one full-size version per app
write, and it blocks base-blob deletes unless a matching version rule is added — it buys nothing here), and
treat **soft delete** as a separate, optional safety net for the live blob rather than part of the retention
story; if it is enabled, record that pruned dailies persist, billed, for its retention period.

## Sources

- Lifecycle management overview (filters are include-only; 24 h to take effect; delete is free; soft-delete
  and immutability limitations; `LifecyclePolicyCompleted`):
  <https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-overview>
- Lifecycle management policy structure (prefixMatch must start with the container name, 10 prefixes,
  case-sensitive, no wildcards; run-condition table; delete blocked by versions/snapshots):
  <https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-policy-structure>
- Lifecycle management policies that delete blobs (`baseBlob` = current version; `daysAfterModification`
  example; version-delete example with `prefixMatch`):
  <https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-policy-delete>
- Lifecycle management performance characteristics (up to 24 h between runs; runs can take multiple days;
  avoid short durations; narrow the prefix):
  <https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-performance-characteristics>
- Blob Storage FAQ — lifecycle policies (24 h on create and on update; no way to know when a policy runs;
  prefix trailing-slash trap; "I don't see capacity changes… check soft delete or versioning"):
  <https://learn.microsoft.com/en-us/azure/storage/blobs/storage-blob-faq>
- `Microsoft.Storage/storageAccounts/managementPolicies` Bicep/ARM reference (`name: 'default'` required;
  `DateAfterModification` on `baseBlob.delete`; api-versions through 2026-04-01; Bicep sample):
  <https://learn.microsoft.com/en-us/azure/templates/microsoft.storage/storageaccounts/managementpolicies>
- Copy Blob REST (destination `Last-Modified` = copy completion time; overwrite semantics; destination needs
  `blobs/write`, source needs `blobs/read`):
  <https://learn.microsoft.com/en-us/rest/api/storageservices/copy-blob>
- Get Blob Properties REST (`x-ms-creation-time`; any modification bumps `Last-Modified`; `x-ms-expiry-time`
  is HNS-only): <https://learn.microsoft.com/en-us/rest/api/storageservices/get-blob-properties>
- Delete Blob REST (`blobs/delete`, least privilege Storage Blob Data Contributor; soft-delete behaviour;
  deletes are not billed): <https://learn.microsoft.com/en-us/rest/api/storageservices/delete-blob>
- List Blobs REST (`blobs/read`, least privilege Storage Blob Data Reader; `prefix` parameter):
  <https://learn.microsoft.com/en-us/rest/api/storageservices/list-blobs>
- Blob versioning (every write creates a version; <1000 versions per blob; full-content billing when the
  whole blob is replaced; `deleteBlobVersion/action` needs Storage Blob Data Owner; separate account
  recommended for frequently overwritten data):
  <https://learn.microsoft.com/en-us/azure/storage/blobs/versioning-overview>
- Soft delete for blobs (1–365 days; soft-deleted snapshot per overwrite; billed as active data; no
  protection against account deletion; `Set Blob Expiry` is HNS-only):
  <https://learn.microsoft.com/en-us/azure/storage/blobs/soft-delete-blob-overview>
- `az storage blob delete-batch` reference (`--pattern` glob where `*` crosses `/`; `--dryrun` ignores
  time-based preconditions; `--auth-mode login`):
  <https://learn.microsoft.com/en-us/cli/azure/storage/blob>
- `azure-cli` source — `storage_blob_delete_batch` swallows `HttpResponseError` and returns success:
  <https://github.com/Azure/azure-cli/blob/dev/src/azure-cli/azure/cli/command_modules/storage/operations/blob.py>
