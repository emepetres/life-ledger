# Restore runbook — recover the database from a dated snapshot

When a bad write, a corruption, or a broken migration has damaged the live
database, this is how you roll it back to a known-good point-in-time snapshot.
It is the operator counterpart to the nightly retained-backup workflow
([ADR-0007](../adr/0007-scheduled-retained-backup.md)): the workflow *makes* the
dated snapshots; this runbook *promotes* one back into production.

All commands are **PowerShell Core (`pwsh`)** ([AGENTS.md](../../AGENTS.md)).

> **Scope.** This recovers from *data* damage — a fat-finger delete, app-logic
> corruption, or development-induced damage while running on real data. It does
> not address the revision-swap window or off-Azure catastrophe (both out of
> scope in ADR-0007).

## The single lever

The app has exactly one restore mechanism, and every path below uses it:

> **Overwrite `ledgerbackup/expenses.db` with a chosen dated snapshot, then cut a
> new revision.**

A new revision gives a fresh, empty `EmptyDir` volume, so the app's boot-time
`restorePlan` loads the database *from the blob* rather than starting empty
([ADR-0003](../adr/0003-persistence-and-storage.md)). This was proven in
production ([#36](https://github.com/emepetres/life-ledger/issues/36), revision
`lifeledger--0000005` booted `restored from backup`). There is **no** direct
replica-file poke and **no** escape hatch — the blob is the only source of truth
the app will read.

## Prerequisites — a principal that can write the live blob

Least-privilege RBAC (ADR-0007) deliberately leaves **no standing principal with
write access to `ledgerbackup`** except the app's own identity:

- the app UAMI can write `ledgerbackup` but is not something you drive by hand;
- the CI service principal has only **Reader** on `ledgerbackup` (it copies
  *out* of it nightly), so it cannot write the restore back;
- a human operator has **no** blob-data role by default.

So a restore needs a **just-in-time grant**. Grant your own account (or a chosen
principal) `Storage Blob Data Contributor` on the `ledgerbackup` container for
the duration of the restore, then remove it. You need control-plane rights
(Owner / User Access Administrator on the resource group) to self-assign.

```pwsh
$RG        = "<resource-group>"                     # $env:AZURE_RESOURCE_GROUP
$APP       = "lifeledger"
$STORAGE   = az storage account list -g $RG --query "[0].name" -o tsv
$ME        = az ad signed-in-user show --query id -o tsv
$SCOPE     = az storage account show -g $RG -n $STORAGE --query id -o tsv

# Grant, scoped to the ledgerbackup container (not the whole account).
$SCOPE_BACKUP = "$SCOPE/blobServices/default/containers/ledgerbackup"
az role assignment create `
  --assignee $ME `
  --role "Storage Blob Data Contributor" `
  --scope $SCOPE_BACKUP

# ...perform the restore below, then REVOKE when done:
# az role assignment delete --assignee $ME --role "Storage Blob Data Contributor" --scope $SCOPE_BACKUP
```

RBAC propagation can lag a minute or two; if the first blob command returns
`AuthorizationPermissionMismatch`, wait and retry.

## The one gate: is a migration implicated?

Before you restore, answer one question — it is the only branch in this runbook:

> **Did a database migration cause or contribute to the damage?**

- **No** — ordinary corruption, a fat-finger delete, or an app-logic bug that
  wrote bad data. Take the [ordinary restore](#ordinary-restore) path.
- **Yes** — a migration itself mangled the data, or shipped alongside the bug.
  Take the [migration-is-the-bug](#migration-is-the-bug-a-deliberate-code-downgrade)
  path, because restoring old data under the *current* image would just re-run
  the bad migration on boot and re-damage it.

## Ordinary restore

Migration **not** implicated. Restore under the current image.

**1. Pick the snapshot.** List what retention holds and choose the newest one
that predates the damage:

```pwsh
az storage blob list `
  --account-name $STORAGE --container-name ledgersnapshots `
  --auth-mode login --prefix daily/ `
  --query "[].{name:name, modified:properties.lastModified}" -o table
# month-ends: --prefix monthly/
$SNAPSHOT = "daily/2026-06-14.db"   # the chosen good point-in-time
```

**2. (Recommended) Safety-snapshot the current live blob** — the undo-the-undo.
Copy the *current* `expenses.db` into a third, disjoint prefix the nightly prune
never touches, so a wrong restore is itself reversible:

```pwsh
$STAMP = (Get-Date -AsUTC -Format "yyyy-MM-ddTHHmmssZ")
az storage blob copy start `
  --account-name $STORAGE --auth-mode login --requires-sync true `
  --source-container ledgerbackup     --source-blob expenses.db `
  --destination-container ledgersnapshots --destination-blob "pre-restore/$STAMP.db"
```

**3. Deactivate the current revision — first.** This is deterministic: with no
live replica running, nothing can `VACUUM INTO` + `Save` over the blob while you
copy the snapshot in. Do **not** rely on ACA swap timing to protect the copy.

```pwsh
$CURRENT = az containerapp revision list -n $APP -g $RG `
  --query "[?properties.active].name | [0]" -o tsv
az containerapp revision deactivate -n $APP -g $RG --revision $CURRENT
```

**4. Copy the snapshot over the live blob.**

```pwsh
az storage blob copy start `
  --account-name $STORAGE --auth-mode login --requires-sync true `
  --source-container ledgersnapshots  --source-blob $SNAPSHOT `
  --destination-container ledgerbackup --destination-blob expenses.db
```

**5. Cut a new revision on the current image.** The new replica boots with an
empty `EmptyDir`, loads the blob, and goose migrates forward (frequently a
no-op, always fine here because the migration is not the problem):

```pwsh
az containerapp update -n $APP -g $RG `
  --revision-suffix ("restore" + (Get-Date -AsUTC -Format "yyyyMMddHHmm"))
```

**6. [Verify](#verify).** Then **revoke** the just-in-time grant from the
prerequisites.

## Migration-is-the-bug: a deliberate code downgrade

Migration implicated. You must boot **code that never had the bad migration**,
carrying **old data** — never the current image, which would re-apply it.

**1. Identify the bad migration and the commit that shipped it.** Life Ledger
tags every image by `github.sha`, so a commit maps to a deployed image.

**2. Pick a snapshot *and* an image SHA both from *before* the bad migration.**
There is no recorded link between a dated snapshot and the image SHA that was
live when it was taken — this is a conscious KISS omission (the thing that would
record it is stamping `deployed_sha` as blob metadata at nightly-copy time; left
out). So **correlate by hand**: match the snapshot's date against the git commit
history to find a commit — and therefore an image SHA — from before the bad
migration merged.

```pwsh
$SNAPSHOT = "daily/2026-06-14.db"   # data from before the bad migration
$OLD_SHA  = "<git-sha from before the bad migration merged>"
$ACR      = az acr list -g $RG --query "[0].loginServer" -o tsv
$OLD_IMAGE = "$ACR/life-ledger:$OLD_SHA"
```

**3. Safety-snapshot, deactivate, and copy** exactly as in the ordinary path
(steps 2–4 above) — the same live-blob protection applies.

**4. Cut *one* revision carrying the old image *and* the old data together.**
This is the load-bearing rule: **never split the image roll from the data
restore** — either order leaves a window where the wrong code meets the wrong
data and re-damages it. One revision, both at once:

```pwsh
az containerapp update -n $APP -g $RG `
  --image $OLD_IMAGE `
  --revision-suffix ("downgrade" + (Get-Date -AsUTC -Format "yyyyMMddHHmm"))
```

The old code boots, sees the snapshot already at *its* schema version → goose is
a no-op → serves clean. **Do not run `goose down`** on the live schema; you are
booting code that never had the migration, not reversing it in place.

> **Accepted cost.** You lose whatever good code shipped in the same commit as
> the bad migration, until a *fixed* image (bad migration removed or corrected)
> is rebuilt and deployed later. That later deploy is an ordinary CD roll.

**5. [Verify](#verify).** Then **revoke** the just-in-time grant.

## Verify

1. **Mechanical.** The new revision reaches `Running` / healthy, and its boot log
   shows the restore actually happened:

   ```pwsh
   az containerapp logs show -n $APP -g $RG --revision <new-revision> --tail 50
   ```

   Look for `database ready at … (restored from backup)` — **not**
   `started fresh`, which would mean the blob copy silently failed or the
   container is misconfigured.

2. **Functional.** Spot-check the app: the expected records are back and the
   damage is gone. Only you know what "correct" looks like.

3. **All-clear.** If a `🚨 Nightly backup gate failed` alarm issue drove this
   restore, **closing that issue is the terminal signal**. If no alarm issue
   existed, none is created.

4. **Revoke** the just-in-time `Storage Blob Data Contributor` grant on
   `ledgerbackup` (prerequisites), so no standing operator write access remains.

## Why it is shaped this way

The full reasoning — why a single lever, why deactivate-first over ACA timing,
why the migration branch, why no `goose down`, why the snapshot→SHA link is not
recorded — is in
[issue #37](https://github.com/emepetres/life-ledger/issues/37) and
[ADR-0007](../adr/0007-scheduled-retained-backup.md).
