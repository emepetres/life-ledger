# Azure CLI blob copy, batch delete, and SQLite checks on a GitHub runner

Research for [issue #34](https://github.com/emepetres/life-ledger/issues/34), under the wayfinder map
[#31 (scheduled retained backup of the blob database)](https://github.com/emepetres/life-ledger/issues/31).
**Advisory** input to the nightly-backup spec: this document establishes what the runner can actually
execute, so the gate and prune decisions are made against reality. It makes no design choices.

Facts current as of **2026-07-25**; every claim is cited in [Sources](#sources). Shell snippets are
PowerShell Core per [AGENTS.md](../../AGENTS.md); the equivalent inside a workflow `run:` block is the
same command with `\` instead of `` ` `` for line continuation, matching the existing bash style in
`ci-cd.yml` / `infra.yml`.

## Answers in one table

| Ticket question | Answer |
| --- | --- |
| `az storage blob copy start --auth-mode login` under OIDC? | **Yes**, with a twist: the CLI mints a **user delegation SAS** for the source, so the caller also needs `generateUserDelegationKey`. |
| Data-plane role on source / destination? | Source read + destination write + delegation key. **`Storage Blob Data Contributor` at storage-account scope covers all three.** |
| Genuinely server-side? | **Yes.** `Copy Blob` is a PUT with an empty body and an `x-ms-copy-source` header. No bytes cross the runner. |
| Delete / `delete-batch`? | **Yes** under `--auth-mode login`; `Storage Blob Data Contributor`. `delete-batch` has `--pattern`, `--dryrun`, `--if-unmodified-since`. |
| Download for the check? | **Yes**; `Storage Blob Data Reader` suffices (Contributor includes it). Sub-second at this size. |
| `sqlite3` on `ubuntu-latest`? | **Preinstalled**, `3.45.1`. Both pragmas supported. **Do not trust the exit code** — compare the output string. |
| **Does the existing federated credential cover a `schedule` run on main?** | **Yes. No second credential is needed.** Verified against this repo's actual emitted subject. |

**The one thing that will actually break the first run** is not OIDC — it is RBAC. The deploy service
principal holds `Contributor` + `User Access Administrator` on the resource group and **no data-plane
role**, and `Contributor` grants no blob data access whatsoever. See [§3](#3-the-rbac-gap-this-repo-actually-has).

## 1. The federated-credential trap — checked, and it is not a trap here

### What GitHub emits

The subject claim is derived from the **ref**, not from the event name. GitHub's OIDC reference states:

> "The subject claim includes the branch name of the workflow, but only if the job doesn't reference an
> environment, and if the workflow is not triggered by a pull request event."

Only two things displace the branch-ref form: a job-level `environment:`, or a `pull_request` trigger.
The words `schedule` and `workflow_dispatch` do not appear in that reference at all — because neither
affects `sub`. The documented example subjects are:

| Scenario | Syntax |
| --- | --- |
| Specific environment | `repo:ORG/REPO:environment:ENVIRONMENT-NAME` |
| `pull_request` events | `repo:ORG/REPO:pull_request` |
| Specific branch | `repo:ORG/REPO:ref:refs/heads/BRANCH-NAME` |
| Specific tag | `repo:ORG/REPO:ref:refs/tags/TAG-NAME` |

([OpenID Connect reference](https://docs.github.com/en/actions/reference/security/oidc).) A push to a
branch has no row of its own — it *is* the "specific branch" row.

And a scheduled run is pinned to the default branch. The `schedule` event table gives `GITHUB_REF` =
"Default branch", `GITHUB_SHA` = "Last commit on default branch", with the note:

> "Scheduled workflows will only run on the default branch."

([Events that trigger workflows → `schedule`](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#schedule).)

So a cron run on `main` produces a subject ending in `:ref:refs/heads/main` — byte-identical to a push
to `main` and to a `workflow_dispatch` on `main`.

### What *this* repo emits — verified, not inferred

This repo does not use the plain `repo:OWNER/REPO:...` form. `scripts/first-deploy.ps1` builds the
**immutable** subject that embeds the numeric owner and repository IDs. That form is real and
documented: repositories created after **2026-07-15** use it by default, and the `@` separator is used
because `@` cannot appear in a GitHub username or repository name
([changelog](https://github.blog/changelog/2026-04-23-immutable-subject-claims-for-github-actions-oidc-tokens/)).

`emepetres/life-ledger` was created **2026-07-18** — after the cutoff — so it is in the immutable regime
by default. Confirmed against the live repo settings:

```pwsh
gh api repos/emepetres/life-ledger --jq '{created_at, id, owner_id: .owner.id, default_branch}'
# {"created_at":"2026-07-18T07:02:18Z","id":1304681063,"owner_id":1446793,"default_branch":"main"}

gh api repos/emepetres/life-ledger/actions/oidc/customization/sub
# {"use_default":true,"use_immutable_subject":false,
#  "sub_claim_prefix":"repo:emepetres@1446793/life-ledger@1304681063"}
```

`sub_claim_prefix` is GitHub telling us the exact prefix it will sign. The full subject for any
branch-ref run on main is therefore:

```text
repo:emepetres@1446793/life-ledger@1304681063:ref:refs/heads/main
```

which is precisely what `first-deploy.ps1` constructs (`repo:$repoOwner@$ownerId/$repoName@$repoId:ref:refs/heads/main`)
and registers as the `life-ledger-main` federated credential.

> `use_immutable_subject: false` is the **opt-in toggle for pre-cutoff repositories**; it is irrelevant
> here because the immutable form is already the default for this repo, as `sub_claim_prefix` proves.

### Azure matches the subject exactly

Entra does a literal string comparison — and fails *silently* if it does not match:

> "The *subject* setting values must exactly match the configuration on the GitHub workflow
> configuration. Otherwise, Microsoft identity platform will look at the incoming external token and
> reject the exchange for an access token. **You won't get an error, the exchange fails without error.**"

> "**Wildcard characters aren't supported in any federated identity credential property value.**"

([Workload identity federation considerations](https://learn.microsoft.com/en-us/entra/workload-id/workload-identity-federation-considerations).)
Issuer must be `https://token.actions.githubusercontent.com` and audience exactly
`api://AzureADTokenExchange` — both already correct in `first-deploy.ps1`. Wildcard matching would need
*flexible* federated identity credentials, which are still **preview**, are mutually exclusive with a
literal `subject`, and cannot be created or even read by the Azure CLI — so they are not an option here
and are not needed.

### Verdict

> **A `schedule`-triggered run on `main` authenticates with the existing `life-ledger-main` federated
> credential. Do not provision a second one.** `workflow_dispatch` on `main` — also in the map's spec —
> produces the same subject and is likewise covered.

Two ways to break it, both avoidable:

1. **Do not put `environment:` on the nightly job.** That swaps the subject to
   `repo:…:environment:<NAME>` and would need a different credential.
2. **Do not rename or transfer the repository** without re-reading `sub_claim_prefix` — the repo ID is
   stable across renames, but the credential is only as good as the string it was created from.

Also required, unchanged from the existing workflows:

```yaml
permissions:
  id-token: write   # OIDC federated login to Azure
  contents: read
```

> "The job or workflow run requires a `permissions` setting with `id-token: write` to allow GitHub's
> OIDC provider to create a JSON Web Token for every run."
> ([Configuring OpenID Connect in Azure](https://docs.github.com/en/actions/how-tos/secure-your-work/security-harden-deployments/oidc-in-azure).)

Job-level or workflow-level both work; `ci-cd.yml` puts it on the job, `infra.yml` at workflow level.
Either matches the house style. There is no `schedule`-specific restriction on OIDC.

**Map constraint #8 confirmed verbatim**, with a scope narrowing worth recording: *"In a **public**
repository, scheduled workflows are automatically disabled when no repository activity has occurred in
60 days."* This repo is currently **private**, so the documented 60-day rule as written does not apply
to it today — but the risk returns the moment the repo is made public, and the map already names it as
accepted. A second, undocumented-in-that-sentence hazard applies regardless: the `actor` for a scheduled
run is whoever last edited the cron expression, and notifications go to that person.

## 2. Server-side copy under OIDC

### It works, and here is what the CLI actually does

`--auth-mode login` tells the CLI to authorize data operations with the Entra token it already holds
from `azure/login`. If the flag is omitted the CLI falls back to fetching the **account key** instead —
the legacy path this repo should not take:

> "Set the `--auth-mode` parameter to `login` to sign in using a Microsoft Entra security principal
> (recommended). Set the `--auth-mode` parameter to the legacy `key` value to attempt to retrieve the
> account access key to use for authorization. **If you omit the `--auth-mode` parameter, then the Azure
> CLI also attempts to retrieve the access key.**"
> ([Authorize data operations with Azure CLI](https://learn.microsoft.com/en-us/azure/storage/blobs/authorize-data-operations-cli).)

So `--auth-mode login` is not optional decoration — omitting it silently changes the auth model. The
same effect can be pinned workflow-wide with `AZURE_STORAGE_AUTH_MODE=login`.

The non-obvious part is how the CLI resolves the *source* of a copy. From `validate_source_url` in the
Azure CLI storage module ([`_validators.py`](https://github.com/Azure/azure-cli/blob/dev/src/azure-cli/azure/cli/command_modules/storage/_validators.py)),
for `--source-container` / `--source-blob` with no `--source-account-name`:

```python
if not source_account_name:
    ...
    # assume that user intends to copy blob in the same account
    source_account_name = ns.get('account_name', None)

# determine if the copy will happen in the same storage account
same_account = False
if not source_account_key and not source_sas and not is_oauth:
    ...          # <- skipped entirely when auth-mode is login

# if oauth, use user delegation key to generate sas
if is_oauth:
    ...
    source_user_delegation_key = client.get_user_delegation_key(start, expiry)

if not source_sas:
    elif valid_blob_source and (ns.get('share_name', None) or not same_account):
        source_sas = create_short_lived_blob_sas_v2(..., user_delegation_key=source_user_delegation_key)
```

Three consequences, all load-bearing:

1. **No account key is ever fetched.** Good — that is the point of OIDC.
2. **The source account defaults to the destination account**, so a same-account copy needs no
   `--source-account-name`, no `--source-account-key`, and no `--sas-token`.
3. **`same_account` stays `False` under OAuth**, so the CLI *always* mints a short-lived (1-day)
   **user delegation SAS** for the source and passes
   `https://<acct>.blob.core.windows.net/<container>/<blob>?<sas>` as the copy source. That requires
   `Microsoft.Storage/storageAccounts/blobServices/generateUserDelegationKey/action` — an easy thing to
   miss when reasoning only from the `Copy Blob` permission table.

### Roles

`Copy Blob`'s own authorization matrix:

| Object type | Microsoft Entra ID | SAS | Shared Key |
| --- | --- | --- | --- |
| Destination blob | **Yes** | Yes | Yes |
| Source blob in same storage account | **Yes** | Yes | Yes |
| Source blob in another storage account | **No** | Yes | No |

with least-privileged roles — destination: `blobs/write` (or `blobs/add/action` for a new blob) →
**Storage Blob Data Contributor**; source in the same account: `blobs/read` → **Storage Blob Data
Reader** ([Copy Blob](https://learn.microsoft.com/en-us/rest/api/storageservices/copy-blob)).

Adding the delegation-key requirement from the CLI path, the minimum set for the nightly job is:

| Need | RBAC action | Where it lives |
| --- | --- | --- |
| Read the source blob | `…/containers/blobs/read` | Storage Blob Data Contributor **DataActions** |
| Write the destination blob | `…/containers/blobs/write`, `…/blobs/add/action` | Storage Blob Data Contributor **DataActions** |
| Delete on prune | `…/containers/blobs/delete` | Storage Blob Data Contributor **DataActions** |
| List for prune / enumerate | `…/blobServices/containers/read` | Storage Blob Data Contributor **Actions** |
| Mint the source SAS | `…/blobServices/generateUserDelegationKey/action` | Storage Blob Data Contributor **Actions** |

> **One role does all of it: `Storage Blob Data Contributor`
> (`ba92f5b4-2d11-453d-a403-e96b0029c9fe`), scoped to the storage account.** That is the same role and
> the same GUID the Bicep already assigns to the app's user-assigned identity
> (`blobContributorRoleId` in `infra/main.bicep`), so nothing new needs learning.

`generateUserDelegationKey` is account-scoped and cannot be granted at container scope:

> "Because the `Get User Delegation Key` operation acts at the level of the storage account, the
> *Microsoft.Storage/storageAccounts/blobServices/generateUserDelegationKey* action must be scoped at
> the level of the storage account, the resource group, or the subscription."
> ([Get User Delegation Key](https://learn.microsoft.com/en-us/rest/api/storageservices/get-user-delegation-key).)

That kills the tempting least-privilege refinement of "`Storage Blob Data Reader` on the source
container, `Storage Blob Data Contributor` on the destination container": *container*-scoped
assignments cannot carry an account-scoped action, so the CLI would be unable to mint the source SAS no
matter which data role you picked. Recovering from that means adding `Storage Blob Delegator`
(`db58b8e5-c6ad-4a2a-8342-4190687cbf4a`) at account scope on top — three role assignments to replace
one, for a single-user personal project with one container's worth of data. Not worth it.

### The copy is genuinely server-side

`Copy Blob` is a `PUT` against the **destination** URL carrying `x-ms-copy-source: <url>` and
**"Request body: None"**. The storage service reads the source itself. The billing table makes the data
path explicit — the destination account is charged one *write* transaction and the source account one
*read* transaction, and *"Egress between accounts within the same region is free"*. There is no line
item for, and no mechanism by which, bytes would traverse the runner. Same-account, same-region: no
egress at all, and the runner's contribution is one HTTPS request plus one for the delegation key.

**Synchronicity.** Do not assume it is instant. A successful `Copy Blob` returns **202 (Accepted)** with
`x-ms-copy-status: success` *or* `pending`, and the docs are explicit that the async form is
best-effort: *"the Blob service copies blobs when server resources are not being utilized by other
tasks, so a copy is not guaranteed to start immediately or complete in a specified timeframe."*
For a gated nightly job that ambiguity is unwanted. Two clean ways out:

- **`--requires-sync true`** — documented as *"Enforce that the service will not return a response
  until the copy is complete."* This is the `Copy Blob From URL` operation (`x-ms-requires-sync:true`),
  which *"copies a blob to a destination within the storage account **synchronously for source blob
  sizes up to 256 mebibytes (MiB)**"* and returns `x-ms-copy-status: success` with no `pending` state at
  all. Constraints: source and destination must both be **block blobs**, and a source over 256 MiB fails
  409 (Conflict). A few-hundred-KB `expenses.db` is three orders of magnitude inside that limit, and the
  RBAC mapping is identical to `Copy Blob`. *(That the CLI flag maps to this specific REST operation is
  inferred from the API surface rather than read out of the CLI source; the flag's documented behaviour
  is primary-sourced and is what the spec should rely on. If `expenses.db` ever approached 256 MiB the
  spec would need to revisit this — it will not.)*
- **Poll** `az storage blob show --query properties.copy` until `status` is `success`. The CLI group help
  itself says *"Use `az storage blob show` to check the status of the blobs."*

`--requires-sync true` is the simpler of the two and removes a polling loop from the workflow.

```pwsh
az storage blob copy start `
  --auth-mode login `
  --account-name $StorageAccount `
  --source-container ledgerbackup --source-blob expenses.db `
  --destination-container <backups> --destination-blob "daily/2026-07-25.db" `
  --requires-sync true
```

One more `Copy Blob` property worth knowing for a gated copy: *"When the source of a copy operation
provides `ETag` values, any changes to the source while the copy operation is in progress will cause
that operation to fail."* Combined with `--source-if-match <etag>`, that gives an exact
check-then-copy guarantee — capture the ETag at integrity-check time, pass it to the copy, and a write
that lands between check and copy fails the run loudly instead of silently snapshotting unverified
bytes. Worth considering in the spec; the app writes after every user action, so the window is real.

## 3. The RBAC gap this repo actually has

The deploy identity created by `scripts/first-deploy.ps1` is granted exactly two roles, both at
resource-group scope: `Contributor` and `User Access Administrator`. **Neither grants any blob data
access.** `Contributor` (`b24988ac-6180-42a0-ab88-20f7382dd24c`) is:

```json
"actions":     ["*"],
"notActions":  ["Microsoft.Authorization/*/Delete", "Microsoft.Authorization/*/Write", ...],
"dataActions": [],
"notDataActions": []
```

— `dataActions` is **empty**. `User Access Administrator` likewise has `"dataActions": []`. Microsoft
says it directly:

> "These roles do not provide access to data in a storage account via Microsoft Entra ID. However, they
> include the **Microsoft.Storage/storageAccounts/listkeys/action**, which grants access to the account
> access keys."
> ([Prevent authorization with Shared Key](https://learn.microsoft.com/en-us/azure/storage/common/shared-key-authorization-prevent).)

So as things stand today:

- `--auth-mode login` blob commands from the nightly workflow would fail with `AuthorizationPermissionMismatch`.
- Dropping `--auth-mode login` would *appear* to work, because `Contributor` can call `listKeys` and the
  CLI silently falls back to the account key. **That is the wrong fix** — it reintroduces a shared key
  the rest of this design has been careful to avoid, and it would break the moment anyone sets
  `allowSharedKeyAccess: false` on the account.

**The fix is one role assignment**: `Storage Blob Data Contributor` on the storage account for the
deploy service principal. Two places it could live, both already viable:

```pwsh
# (a) imperatively, alongside the existing grants in scripts/first-deploy.ps1
az role assignment create `
  --assignee-object-id $spObjectId --assignee-principal-type ServicePrincipal `
  --role 'Storage Blob Data Contributor' `
  --scope "/subscriptions/$SubscriptionId/resourceGroups/$ResourceGroup/providers/Microsoft.Storage/storageAccounts/$StorageAccount"
```

or (b) declaratively in `infra/main.bicep`, mirroring the existing `blobContributor` resource but with
the deploy principal's object ID passed in as a parameter. (b) matches ADR-0005's "Bicep is the single
source of truth" posture; (a) matches where the *other* grants for this same principal already live. The
identity holds `User Access Administrator`, so it can create the assignment itself either way. **Which
one is a spec decision, not a research finding** — but note that (b) needs a new parameter, because the
deploy SP's object ID is not currently known to the template.

## 4. Delete and `delete-batch`

Both support `--auth-mode {key, login}`. `Delete Blob` maps to
`Microsoft.Storage/storageAccounts/blobServices/containers/blobs/delete`, least-privileged built-in role
**Storage Blob Data Contributor** ([Delete Blob](https://learn.microsoft.com/en-us/rest/api/storageservices/delete-blob)) —
already covered by §2's single assignment. `delete-batch` is not a special case: Microsoft's own
reference example for it uses `--auth-mode login`.

`az storage blob delete-batch` takes `--source/-s` (the container, **required**), plus `--pattern`,
`--dryrun`, `--delete-snapshots`, and the `--if-*` preconditions. The documented example is almost
exactly the prune rule the map needs:

```pwsh
$cutoff = (Get-Date).ToUniversalTime().AddDays(-7).ToString("yyyy-MM-ddTHH:mmZ")
az storage blob delete-batch `
  --auth-mode login --account-name $StorageAccount `
  --source <backups> --pattern "daily/*" `
  --if-unmodified-since $cutoff `
  --dryrun
```

Four things to get right in the spec:

- **`--pattern` is fnmatch, and `*` crosses `/`.** The CLI reference states plainly: *"When you use `*`
  in `--pattern`, it will match any character including the the directory separator `/`."* A pattern
  intended to hit only dailies must be anchored by prefix (`daily/*`) — or, safer given map constraint
  #6 (*never touch month-ends*), the month-end snapshots should live in a **different container or a
  different prefix** so no pattern can reach them. Belt and braces: `[seq]` / `[!seq]` classes are
  supported, so `daily/20??-??-??.db` is expressible.
- **`--dryrun` exists, but it lies about time-based pruning.** The CLI reference is explicit: *"Show the
  summary of the operations to be taken instead of actually deleting the file(s). If this is specified,
  it will ignore all the Precondition Arguments that include `--if-modified-since` and
  `--if-unmodified-since`. So the file(s) will be deleted with the command without `--dryrun` may be
  different from the result list with `--dryrun` flag on."* So a dry run of the prune shows **every blob
  matching `--pattern`**, not the ones the age filter would actually remove. Useful for validating the
  pattern; **useless as a safety check on the retention window**, and actively misleading if read as
  one. Validate the age filter by listing instead:
  `az storage blob list -c <backups> --prefix daily/ --query "[?properties.lastModified<'<cutoff>'].name"`.
- **`--if-unmodified-since` is a precondition on the blob, not a filter on its name.** For dailies that
  are written once and never touched, last-modified *is* the snapshot date, so age-based pruning works.
  If the spec ever rewrites a daily in place, that equivalence breaks and the prune must key off the
  filename instead.
- **Soft delete changes what "delete" means.** `infra/main.bicep` declares the `blobServices/default`
  resource with **no `deleteRetentionPolicy`**, so prune deletes are expected to be permanent. If soft
  delete is enabled on the live account, *"the blob is soft-deleted"* and retained for the policy's
  window instead — which is harmless for correctness but changes the storage-cost arithmetic behind
  constraint #6. Worth one `az storage account blob-service-properties show` at implementation time.

## 5. Download for the integrity check

`PRAGMA integrity_check` needs a real file, so the gate requires a genuine download — this is the one
step that does move bytes through the runner.

`Get Blob` maps to `…/containers/blobs/read`, least-privileged **Storage Blob Data Reader**
([Get Blob](https://learn.microsoft.com/en-us/rest/api/storageservices/get-blob)); `Storage Blob Data
Contributor` includes `blobs/read` in its DataActions, so §2's assignment covers it and no second role
is needed.

```pwsh
az storage blob download `
  --auth-mode login --account-name $StorageAccount `
  --container-name ledgerbackup --name expenses.db `
  --file expenses.db --no-progress
```

**Cost in time.** No primary source publishes a latency figure for a GitHub-hosted runner to Azure
Blob, and I will not invent one. What the docs *do* bound is the service's own patience: *"A `Get Blob`
operation is allowed two minutes per MiB to be completed. If the operation is taking longer than two
minutes per MiB on average, the operation will time out."* That is a timeout budget, not an expectation.
Characterise it honestly for the spec: **a few-hundred-KB blob is a single HTTPS GET whose wall time is
dominated by TLS handshake and token acquisition, not by transfer** — sub-second to low single-digit
seconds. The nightly job's runtime will be set by runner boot, `actions/checkout`, and `az` process
startup, all of which dwarf it. `--no-progress` keeps the log clean.

## 6. SQLite tooling on `ubuntu-latest`

**`ubuntu-latest` currently maps to Ubuntu 24.04** (`ubuntu-latest` and `ubuntu-24.04` are the same
image; `ubuntu-26.04` exists as a public-preview label only). The `-latest` migration from 22.04 is
complete, and the runner-images policy is to announce before moving the label again.

**`sqlite3` is preinstalled — the CLI binary, not just the library.** `Ubuntu2404-Readme.md` (OS
24.04.4 LTS, image `20260714.240.1`) lists it in two places:

| Where | Entry |
| --- | --- |
| Databases | `sqlite3 3.45.1` |
| Installed apt packages | `sqlite3 \| 3.45.1-1ubuntu2.6` |

`libsqlite3-dev 3.45.1-1ubuntu2.6` is listed separately, which is the tell that the CLI is a deliberate
install and not a transitive library pull. The image toolset config confirms it: `toolset-2404.json`
puts `sqlite3` in `.apt.cmd_packages` (packages whose *command* is validated at image-build time) and
`libsqlite3-dev` in `.apt.common_packages`. For reference, `ubuntu-22.04` ships `sqlite3 3.37.2`.

**No install step is needed.** If it ever were, the honest guidance is
`sudo apt-get update && sudo apt-get install -y sqlite3` — the apt index lists are present but baked in
at image-build time and go stale, which is the usual cause of 404s on package fetch. The pinnable
alternative is the official precompiled bundle from <https://sqlite.org/download.html>
(`sqlite-tools-linux-x64-3530400.zip` for 3.53.4; the filename encoding is *"For version 3.X.Y the
filename encoding is 3XXYY00"*, under a year directory that does not auto-advance).

Two other preinstalls the nightly workflow will lean on, from the same readme: **Azure CLI 2.88.0** and
**GitHub CLI 2.96.0** (the latter matters for map constraint #8's "fail loudly and open an issue").
`jq 1.7.1` is there too.

> Pin `runs-on: ubuntu-24.04` rather than `ubuntu-latest` **only if** the spec ever depends on the exact
> sqlite3 version. It currently does not — both pragmas long predate 3.37 — so `ubuntu-latest` (matching
> `ci-cd.yml` and `infra.yml`) is the consistent choice.

### `integrity_check` vs `quick_check`

From the authoritative [pragma reference](https://sqlite.org/pragma.html):

`PRAGMA integrity_check` is a *"low-level formatting and consistency check"* that looks for out-of-order
table/index entries, misformatted records, missing pages, missing or surplus index entries, UNIQUE /
CHECK / NOT NULL constraint violations, freelist integrity, and sections of the database used more than
once or not at all. `PRAGMA quick_check` *"is like integrity_check except that it does not verify UNIQUE
constraints and does not verify that index content matches table content."* Those two omissions are the
entire difference. Cost: `quick_check` is **O(N)**; `integrity_check` *"requires O(NlogN) time where N
is the total number of rows in the database."*

At this database's size — a personal expense tracker, a few writes a day — `O(NlogN)` on a
few-hundred-KB file is nothing. **Use `integrity_check`.** `quick_check` buys a saving that does not
exist here while giving up exactly the index-vs-table cross-check that would catch the corruption class
this whole effort is about.

Both accept an optional max-errors argument, `integrity_check(N)`, *"with N defaulting to 100"*, and
both return **a single row containing the text `ok`** on a healthy database.

### The exit-code trap

`sqlite3 file.db "PRAGMA integrity_check;"` works non-interactively — *"When the sqlite3 program is
launched with two arguments, the second argument is passed to the SQLite library for processing, the
query results are printed on standard output in list mode, and the program exits"*
([CLI docs](https://sqlite.org/cli.html)).

But a corrupt database is **not an error** as far as the shell is concerned: `integrity_check` is a
*successful* query that happens to return rows of text describing the damage. There is no SQL error, so
`-bail` has nothing to trip on and the exit code stays 0.

> **The gate must compare the output string, not the exit status.** `sqlite3.exe`'s exit code on a
> corrupt (as opposed to unopenable) database is not documented on sqlite.org — I could not verify it
> from a primary source, which is itself sufficient reason not to depend on it.

The robust shape, inside the workflow's `run:` block:

```bash
result=$(sqlite3 expenses.db "PRAGMA integrity_check;" 2>&1) || true
if [ "$result" != "ok" ]; then
  echo "::error::integrity_check failed: $result"
  exit 1
fi
```

Merging stderr matters: an unopenable file (truncated download, wrong bytes) writes to stderr and leaves
`$result` empty, which also fails the comparison — so the one test covers both "corrupt" and "could not
open". Add a size/non-empty assertion on the downloaded file if the spec wants to distinguish them in
the failure message.

## What this means for the nightly workflow

Nothing in the map's fixed constraints is contradicted. The concrete deltas the implementation session
must carry:

1. **Grant `Storage Blob Data Contributor` to the deploy service principal**, scoped to the storage
   account. This is the only genuine blocker, and it is one role assignment. Decide Bicep vs
   `first-deploy.ps1` (§3).
2. **Reuse the existing federated credential.** No OIDC work at all. Copy the `azure/login@v2` step
   verbatim from `infra.yml`, keep `permissions: id-token: write`, and **do not add `environment:`** to
   the job.
3. **Always pass `--auth-mode login`** (or set `AZURE_STORAGE_AUTH_MODE=login` on the job). Omitting it
   silently switches to account-key auth.
4. **Use `--requires-sync true`** on the copy so the step's success means the copy finished.
5. **Anchor the prune pattern by prefix, and separate month-ends by container or prefix** so no
   `--pattern` can reach them. `--dryrun` during the rehearsal.
6. **Gate on the string `ok`, not the exit code.**
7. `sqlite3`, `az`, and `gh` are all preinstalled on `ubuntu-latest`. No setup steps.

## Open / unverified

- **The live federated credential's stored subject was not read** — that needs Azure credentials this
  session does not have. The verdict in §1 rests on (a) GitHub's emitted subject, read from the live
  repo, and (b) the subject `first-deploy.ps1` constructs, read from source; they match by
  construction. A 30-second confirmation before the first nightly run:
  `az ad app federated-credential list --id $env:AZURE_CLIENT_ID --query "[].{name:name,subject:subject}"`.
- **`--requires-sync`'s mapping to `Put Blob From URL`** is inferred from the API surface, not from
  reading the CLI's implementation. The flag's *documented behaviour* is primary-sourced and is what the
  spec should rely on.
- **Blob soft-delete state on the live storage account** is not declared in Bicep and was not checked
  (§4). Affects prune cost arithmetic, not correctness.
- **Download latency from a GitHub runner** has no primary source; §5 gives a characterisation, not a
  number. If the spec needs a real figure, the first `workflow_dispatch` run will produce one.

## Sources

**GitHub OIDC and scheduling**

- OpenID Connect reference — example subject claims, token claims, the branch-name-unless-environment-or-pull_request rule: <https://docs.github.com/en/actions/reference/security/oidc>
- OpenID Connect concepts: <https://docs.github.com/en/actions/concepts/security/openid-connect>
- Configuring OpenID Connect in Azure — `id-token: write` requirement: <https://docs.github.com/en/actions/how-tos/secure-your-work/security-harden-deployments/oidc-in-azure>
- Events that trigger workflows → `schedule` (default-branch-only; `GITHUB_REF` = default branch; 60-day disabling in public repos; actor semantics): <https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#schedule>
- Immutable subject claims for GitHub Actions OIDC tokens — the `repo:OWNER@OWNER-ID/REPO@REPO-ID` form, the 2026-07-15 cutoff, opt-in for older repos: <https://github.blog/changelog/2026-04-23-immutable-subject-claims-for-github-actions-oidc-tokens/>
- Live repo verification: `gh api repos/emepetres/life-ledger` and `gh api repos/emepetres/life-ledger/actions/oidc/customization/sub` (run 2026-07-25; output quoted inline in §1)

**Microsoft Entra workload identity federation**

- Workload identity federation considerations — exact subject matching, no wildcards, silent failure, 20-FIC limit: <https://learn.microsoft.com/en-us/entra/workload-id/workload-identity-federation-considerations>
- Configure a federated identity credential on an app: <https://learn.microsoft.com/en-us/entra/workload-id/workload-identity-federation-create-trust>
- Flexible federated identity credentials (preview) — CLI cannot create or read them: <https://learn.microsoft.com/en-us/entra/workload-id/workload-identities-flexible-federated-identity-credentials>

**Azure Storage — authorization and RBAC**

- Authorize access to blob data with Azure CLI — `--auth-mode login` vs `key`, the account-key fallback, `AZURE_STORAGE_AUTH_MODE`: <https://learn.microsoft.com/en-us/azure/storage/blobs/authorize-data-operations-cli>
- Azure built-in roles, Storage — Storage Blob Data Contributor (`ba92f5b4-2d11-453d-a403-e96b0029c9fe`) and Storage Blob Data Reader (`2a2b9908-6ea1-4ae2-8e65-a410df84e7d1`) Actions/DataActions: <https://learn.microsoft.com/en-us/azure/role-based-access-control/built-in-roles/storage>
- Azure built-in roles, Privileged — Contributor (`b24988ac-6180-42a0-ab88-20f7382dd24c`) and User Access Administrator, both with empty `dataActions`: <https://learn.microsoft.com/en-us/azure/role-based-access-control/built-in-roles/privileged>
- Prevent authorization with Shared Key — "These roles do not provide access to data … However, they include `listkeys/action`"; `AllowSharedKeyAccess` semantics; user delegation SAS still permitted when Shared Key is disallowed: <https://learn.microsoft.com/en-us/azure/storage/common/shared-key-authorization-prevent>

**Azure Storage — REST operations**

- Copy Blob — server-side semantics, empty request body, authorization matrix, least-privileged roles, 202 + `x-ms-copy-status`, ETag-changes-fail-the-copy, billing/egress: <https://learn.microsoft.com/en-us/rest/api/storageservices/copy-blob>
- Put Blob From URL — synchronous copy, 5,000 MiB source limit, block-blob destination, `x-ms-copy-source-authorization`: <https://learn.microsoft.com/en-us/rest/api/storageservices/put-blob-from-url>
- Delete Blob — `blobs/delete`, Storage Blob Data Contributor, soft-delete behaviour: <https://learn.microsoft.com/en-us/rest/api/storageservices/delete-blob>
- Get Blob — `blobs/read`, Storage Blob Data Reader, "two minutes per MiB" timeout: <https://learn.microsoft.com/en-us/rest/api/storageservices/get-blob>
- Get User Delegation Key — `generateUserDelegationKey/action`, must be scoped at account level or higher, Storage Blob Delegator: <https://learn.microsoft.com/en-us/rest/api/storageservices/get-user-delegation-key>

**Azure CLI**

- `az storage blob copy` — `copy start` parameters, `--requires-sync`, `copy start-batch`, "Use `az storage blob show` to check the status": <https://learn.microsoft.com/en-us/cli/azure/storage/blob/copy>
- `az storage blob` — `delete`, `delete-batch` (`--source`, `--pattern`, `--dryrun`, `--if-unmodified-since`, the `*`-crosses-`/` note), `download`, `list`: <https://learn.microsoft.com/en-us/cli/azure/storage/blob>
- Azure CLI source, `validate_source_url` in the storage module — the OAuth → user-delegation-SAS source path: <https://github.com/Azure/azure-cli/blob/dev/src/azure-cli/azure/cli/command_modules/storage/_validators.py>

**GitHub runner images**

- actions/runner-images README — `ubuntu-latest` → Ubuntu 24.04; label policy: <https://github.com/actions/runner-images>
- GitHub-hosted runners reference: <https://docs.github.com/en/actions/reference/runners/github-hosted-runners>
- `Ubuntu2404-Readme.md` — image `20260714.240.1`, `sqlite3 3.45.1`, `Azure CLI 2.88.0`, `GitHub CLI 2.96.0`, `jq 1.7.1`: <https://github.com/actions/runner-images/blob/main/images/ubuntu/Ubuntu2404-Readme.md>
- `Ubuntu2204-Readme.md` — `sqlite3 3.37.2`: <https://github.com/actions/runner-images/blob/main/images/ubuntu/Ubuntu2204-Readme.md>
- `toolset-2404.json` — `sqlite3` in `apt.cmd_packages`: <https://github.com/actions/runner-images/blob/main/images/ubuntu/toolsets/toolset-2404.json>

**SQLite**

- PRAGMA reference — `integrity_check`, `quick_check`, the `ok` row, O(N) vs O(NlogN), `integrity_check(N)` defaulting to 100: <https://sqlite.org/pragma.html>
- Command Line Shell for SQLite — two-argument non-interactive invocation, `.bail`: <https://sqlite.org/cli.html>
- SQLite download page — precompiled Linux CLI bundle and the `3XXYY00` filename encoding: <https://sqlite.org/download.html>
- SQLite changelog — 3.45.1 released 2024-01-30: <https://sqlite.org/changes.html>
