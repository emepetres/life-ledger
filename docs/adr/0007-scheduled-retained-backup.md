# Scheduled retained backup of the database, and a restore runbook

**Status:** accepted — extends the persistence design in
[ADR-0003](0003-persistence-and-storage.md) and the deploy design in
[ADR-0005](0005-deploy-azure-cicd.md). Neither is superseded; this adds a new
protection they left open.

Life Ledger keeps a nightly series of **retained, point-in-time snapshots** of
the SQLite database in a second Blob container, guarded by an integrity check,
pruned to a rolling 7-day daily window with **month-end snapshots kept forever**.
A separate **restore runbook** ([docs/deployment/restore-runbook.md](../deployment/restore-runbook.md))
turns any snapshot back into production. The scheduler is a GitHub Actions cron;
no new runtime service, no app change, ~\$0.

## Context: the existing backup protects against the wrong thing

ADR-0003's backup-on-write copies a consistent `VACUUM INTO` snapshot to a single
Blob, `ledgerbackup/expenses.db`, after every write and restores it on a cold
boot. That protects against **replica loss** — a reschedule wipes the ephemeral
`EmptyDir`, and the blob repopulates it.

It does **nothing** against a *bad write*. An accidental delete, a corruption, or
— the primary driver here — **development-induced data damage while running on
real data** is copied over the only good copy within a second. There is exactly
one blob, and it always holds the latest state, good or bad. To roll back you
need copies from *before* the damage, and the write-through backup keeps none.

This ADR adds that history.

## Decisions

| Aspect | Decision |
| --- | --- |
| Threats in scope | Fat-finger deletes; silent corruption; **development-induced data damage** on real data. Rollback granularity of one day; a 7-day window suffices. |
| Threats out of scope | The ADR-0003 revision-swap window; cross-account / off-Azure catastrophe. (See [rejected alternatives](#rejected-alternatives).) |
| Where snapshots live | A **new `ledgersnapshots` container** beside `ledgerbackup` in the same storage account. Layout: `daily/YYYY-MM-DD.db` and `monthly/YYYY-MM.db`, disjoint prefixes. The live `expenses.db` **does not move** — purely additive, no app change. |
| Scheduler | **GitHub Actions cron `0 1 * * *` UTC**, plus `workflow_dispatch`. No new runtime service. |
| Mechanism | **Blob-to-blob server-side copy** of the live `expenses.db`. No app endpoint, no app change — the live blob is already a consistent `VACUUM INTO` snapshot. |
| Integrity gate | The nightly copy happens **only after a corruption check passes** (opens, `PRAGMA integrity_check` = `ok`, `goose_db_version` ≥ 1). A blind copy would replicate corruption into all 7 dailies. A trip skips the copy **and** freezes the prune, and raises an alarm. |
| Retention | Delete dailies older than 7 days; **never touch month-ends**. Month-ends are kept forever — storage is ~cents/year at this size, so retention is a look-back decision, not a cost one. |
| Month-end snapshot | The month-end **is the 1st-of-next-month run**, written to both `daily/` and `monthly/`, and **labelled by the month it represents** (`monthly/2026-06.db`), not the day it ran. A passing run backfills a missed month-end within the first 7 days of the month. |
| Prune mechanism | **Pruned inside the workflow**, not by a Blob lifecycle-management policy. |
| Access model | **Least-privilege built-in roles in Bicep.** The app UAMI's Blob grant **shrinks from account-wide to `ledgerbackup` alone** (zero reach into the history it would roll back to). The CI service principal gets **Reader on `ledgerbackup`** (copy source) + **Contributor on `ledgersnapshots`** (copy dest + prune). |
| Observability | **Fail loudly:** a red workflow run **and** a single sticky `backup-alarm` issue (`🚨 Nightly backup gate failed`), create-or-comment, cleared manually. |
| Restore | Operator-driven, via [the restore runbook](../deployment/restore-runbook.md): overwrite `ledgerbackup/expenses.db` with a chosen snapshot, then cut a new revision. One gate — *is a migration implicated?* — branches to a code-downgrade path. |

The full reasoning behind each row lives in the wayfinding tickets it came from:
snapshot home & RBAC ([#32](https://github.com/emepetres/life-ledger/issues/32)),
lifecycle vs workflow prune ([#33](https://github.com/emepetres/life-ledger/issues/33)),
runner blob ops ([#34](https://github.com/emepetres/life-ledger/issues/34)),
the integrity gate ([#35](https://github.com/emepetres/life-ledger/issues/35)),
the restore primitive ([#36](https://github.com/emepetres/life-ledger/issues/36)),
and the restore runbook ([#37](https://github.com/emepetres/life-ledger/issues/37)),
under map [#31](https://github.com/emepetres/life-ledger/issues/31).

## Rejected alternatives

- **Blob lifecycle-management policy for the daily prune** — the platform-native
  way to expire `daily/` blobs after 7 days. Rejected in favour of pruning inside
  the workflow ([#33](https://github.com/emepetres/life-ledger/issues/33)). A
  lifecycle policy runs on the platform clock **unconditionally**, so if the cron
  stops (the accepted 60-day risk below), the policy keeps deleting until *zero*
  dailies remain — it degrades to no backups. A workflow prune only runs when the
  workflow runs, so a stopped cron degrades to *stale-but-present* snapshots,
  which is the strictly safer failure. The `daily/` vs `monthly/` prefix split is
  kept anyway, so the lifecycle fallback stays open if ever wanted.
- **An app endpoint or app change to produce the snapshot** — unnecessary. The
  live `ledgerbackup/expenses.db` is already a consistent, sidecar-free
  `VACUUM INTO` snapshot (ADR-0003), so a blob-to-blob copy captures a clean point
  in time with zero coupling to the running app.
- **A second storage account for isolation** — rejected
  ([#32](https://github.com/emepetres/life-ledger/issues/32)). The threat is the
  app writing bad data through its data-plane identity; a *container* is Azure
  RBAC's native scope, so confining the app to `ledgerbackup` already blocks it
  from the history completely. A second account adds moving parts for no extra
  blast-radius reduction at this scale.
- **A custom "read/write, no delete" role for the app** — rejected. Built-in
  Contributor includes delete, which the app never uses, but `ledgerbackup` holds
  only the disposable working copy; delete permission there threatens nothing in
  the retained history. A custom role is standing maintenance for zero benefit.
- **A dedicated dead-man's-switch service** to detect the cron stopping —
  rejected. A genuine independent timer is exactly the new always-on runtime
  service this effort rules out. The stopped-cron risk is named and accepted
  instead (below).
- **A restore product feature** (an admin route/job that promotes a snapshot) —
  rejected. A new attack surface guarding a once-a-year action for a single user;
  the operator runbook covers it with no standing surface.

## Accepted risks

- **DST slop and GitHub cron drift.** GitHub cron is UTC-only, so `0 1 * * *`
  UTC is 3 a.m. Madrid in summer and 2 a.m. in winter, and GitHub's scheduler
  itself drifts (and can delay under load). The 01:00–02:00 UTC slot is chosen so
  the Madrid date and the UTC date agree at that hour — no off-by-one-day
  filenames in either season — but the **exact minute is not load-bearing**. Do
  not "fix" the drift.
- **The 60-day scheduled-workflow disable.** GitHub disables a repo's `schedule:`
  triggers after 60 days of no repo activity. This is a genuine dead-man's-switch
  gap, consciously accepted (a real fix needs the independent timer this effort
  rules out). It bites only public repos today; the repo is currently private.
  The workflow-prune choice above ensures that if the cron *does* silently stop,
  the failure mode is stale-but-present snapshots, not deletion to zero.
- **"7 days" is really 7–9, best-effort.** The prune runs only on nights the
  workflow runs and the gate passes, so the daily window is a best-effort 7–9
  days, not a hard 7. That is by design.
- **The revision-swap window** (restated from ADR-0003 and ADR-0005 so the
  documents do not appear to disagree). During a deploy, ACA briefly runs the old
  and new replica together; each has its own `EmptyDir`, but both back up to the
  same `ledgerbackup/expenses.db`, so last-writer-wins can clobber a write. This
  is accepted in ADR-0003 (mitigation is operational: don't enter data
  mid-deploy). A 3 a.m. snapshot **does not** address it and is not meant to —
  the window is a live-write race, not a point-in-time-history gap.

## Consequences

- **Bicep changes, no new runtime service, ~\$0.** A new `ledgersnapshots`
  container, the app UAMI's Blob grant re-scoped to `ledgerbackup`, and two new
  CI role assignments (Reader on `ledgerbackup`, Contributor on
  `ledgersnapshots`). No new networking, no new always-on compute.
- **The CI principal gains data-plane access it did not have.** Previously CI held
  only control-plane roles (Contributor + User Access Administrator on the
  resource group). It now holds two blob-data roles, both declared in Bicep and
  least-privilege.
- **Restore requires a just-in-time grant.** No standing principal can write
  `ledgerbackup` except the app itself, so an operator restoring a snapshot grants
  themselves `Storage Blob Data Contributor` on `ledgerbackup` for the duration
  and revokes it after — documented in [the restore runbook](../deployment/restore-runbook.md).
- **The snapshot→image-SHA link is not recorded.** For the migration-is-the-bug
  restore path the operator correlates snapshot dates against git history by hand
  (KISS); if this ever becomes painful, stamp `deployed_sha` as blob metadata at
  nightly-copy time.

## Later upgrades left open

- Stamp `deployed_sha` (and other provenance) as blob metadata on each snapshot,
  so the migration-is-the-bug path needs no manual date↔commit correlation.
- Revisit "month-ends forever" once real snapshot size and growth numbers exist —
  today the storage cost is negligible, so the decision is a look-back one.
- An independent dead-man's-switch (external timer) if the 60-day disable ever
  bites in practice — accepting the new runtime service it requires.

Depends on persistence ([ADR-0003](0003-persistence-and-storage.md)) and deploy
([ADR-0005](0005-deploy-azure-cicd.md)); charted in map
[#31](https://github.com/emepetres/life-ledger/issues/31).
