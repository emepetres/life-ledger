# Comparing candidate storage backends for SQLite on Azure Container Apps

Research for [issue #21](https://github.com/emepetres/life-ledger/issues/21). **Advisory** input to the
later persistence decision ticket — this document ranks the candidates with evidence but does **not**
make the final choice — that is recorded in
[ADR-0003 (persistence & storage)](../adr/0003-persistence-and-storage.md), which this document is the
evidence behind, alongside [ADR-0005 (deploy to Azure)](../adr/0005-deploy-azure-cicd.md).

Facts current as of **2026-07-24**; every claim is cited to a primary source below. Azure moves fast and
the public pricing pages render region placeholders — **all $ figures are order-of-magnitude and must be
re-checked in the Azure Pricing Calculator for West Europe before the decision ticket** (flagged inline).

## The workload

Go + SQLite via the pure-Go `modernc.org/sqlite` driver, one DB file, `journal_mode=WAL` and
`busy_timeout=5000` set on every connection (`internal/store/store.go`), goose migrations embedded in the
binary. Running on Azure Container Apps (ACA), West Europe, **min 1 / max 1 replica** (single-writer
invariant), DB on a volume mounted at `/data`. Single user, a handful of writes per day. Stated priorities,
in order: **one deployable unit**, **cheapness** (~$5/mo target), **local-dev parity** (zero-config
`./data/expenses.db`). Keep SQLite unless forced off it.

## Two facts that reshape the comparison

**1. ACA has exactly three storage types — none of them a managed disk.** The official docs enumerate
container-scoped ephemeral, replica-scoped ephemeral (`EmptyDir`), and Azure Files (SMB *or* NFS), and
state plainly: *"Azure Container Apps doesn't support mounting file shares from Azure NetApp Files or Azure
Blob Storage."* There is **no attached-managed-disk / block-persistent-volume primitive for Container
Apps** — that is an AKS / Azure Container Instances capability, not an ACA one.
([ACA storage-mounts](https://learn.microsoft.com/en-us/azure/container-apps/storage-mounts), updated
2026-07-21.)

> **This directly answers R1.c.** "Azure managed disk / persistent volume attached to the Container App"
> is **not a real ACA option** in West Europe or anywhere else — there is no such SKU to be GA or preview.
> The closest *realizable* form of the handoff's favourite is **NFS Azure Files** (a subtype of Azure
> Files, GA on ACA since June 2025), so R1.c is assessed below as NFS Azure Files. The single-replica
> attach questions (survives restart? survives reschedule? min1/max1 constraint?) are therefore moot for a
> block disk and re-framed for NFS: because the data lives on the *share*, not the replica, it survives
> both restart and reschedule regardless of replica count.

**2. Why SMB breaks SQLite, and which candidates actually fix it.** SQLite's correctness depends on the
OS lock syscalls behaving as advertised: *"POSIX advisory locking is known to be buggy or even
unimplemented on many NFS implementations"* and *"Your best defense is to not use SQLite for files on a
network filesystem."* ([SQLite locking](https://www.sqlite.org/lockingv3.html).) Two distinct failure
modes matter here:

- **Advisory locking.** Azure Files **SMB does not expose POSIX byte-range advisory locks**, so a lock
  that should briefly block instead fails immediately — the `SQLITE_BUSY` at migration. `busy_timeout`
  only helps if the lock syscall actually *waits*; on SMB it never gets the chance. **NFS Azure Files is
  "fully POSIX-compliant"** and implements the NFSv4.1 lock operations
  ([NFS in Azure Files](https://learn.microsoft.com/en-us/azure/storage/files/files-nfs-protocol), updated
  2026-07-17), so it is the one network-share option that gives SQLite the locking primitive it needs.
- **WAL shared memory.** `journal_mode=WAL` keeps a `-shm` wal-index in shared memory; the docs are
  explicit: *"All processes using a database must be on the same host computer; WAL does not work over a
  network filesystem."* ([SQLite WAL](https://www.sqlite.org/wal.html).) At **min1/max1 this is moot** in
  steady state (one container, one host) — **except** during the ADR-0005 revision-swap two-writer window,
  when old and new replicas briefly co-mount the share. On *any* network share that window violates WAL's
  same-host guarantee; on local disk the window cannot occur (each replica has its own disk).

## Candidate assessment

### R1.c — Azure managed disk / persistent volume → realized as NFS Azure Files
- **Literal form does not exist on ACA** (fact 1). No managed-disk attach primitive, so the GA/SKU/attach
  questions have no answer to give.
- **Realizable form — NFS Azure Files — is viable.** POSIX advisory byte-range locks supported; survives
  container restart **and** replica reschedule (data on the share). Keeps one SQLite file, one binary, and
  the `LIFELEDGER_DB_PATH` local-parity story.
- **Cost:** NFS is **SSD/premium-only** and needs a provisioned share; premium file shares bill on
  provisioned GiB (~$0.16/GiB-mo) with a practical floor around **100 GiB ⇒ ~$16/mo**, well over the ~$5
  target. Plus a **mandatory custom VNet + private or service endpoint** (NFS has no user auth; access is
  network-gated). *(Pricing needs live verification — see note at top.)*
- **Main risk:** cost floor and infra step-up (VNet/endpoint) vs today; SQLite's blanket network-FS
  caution still applies; the revision-swap WAL window is unchanged.

### R1.a — Azure Blob via BlobFuse — disqualified (two independent reasons)
- **ACA cannot mount Blob at all** (fact 1). Dead on arrival for this platform.
- **Even elsewhere, the locking answer is "no."** BlobFuse translates FS calls to Blob REST and *"doesn't
  guarantee full POSIX compliance"* — rename is non-atomic, and it offers no real byte-range advisory
  locking. ([BlobFuse overview](https://learn.microsoft.com/en-us/azure/storage/blobs/blobfuse2-what-is),
  updated 2026-02-02.) Strictly **worse than the SMB setup being replaced**. Rule it out. (Blob remains
  useful as a *backup target* — see R1.b.)

### R1.b — ephemeral / `EmptyDir` (local container disk)
- **Best locking story of all.** Local ext4 gives *genuine* POSIX locks, so the `SQLITE_BUSY` root cause
  disappears entirely; WAL's same-host requirement is satisfied by construction.
- **Storage is bundled free** in the replica's ephemeral quota — 4 GiB at ≤1 vCPU, 8 GiB above 1 vCPU
  ([ACA storage-mounts](https://learn.microsoft.com/en-us/azure/container-apps/storage-mounts)); a tiny DB
  fits easily. ~$0 over today, plus cents for Blob if backing up.
- **Preserves the rationale best:** one static binary, single writer, and byte-for-byte local parity with
  `go run`.
- **Main risk:** replica-scoped ephemeral storage is **wiped when the replica shuts down** (reschedule,
  revision deploy, scale event) — files survive a container *restart* but not a replica *reschedule*.
  Mitigation: **backup-on-write** — after each committed write (a handful/day) copy `expenses.db` to a Blob
  container; restore on boot if the local file is absent. That shrinks the worst-case loss to ~one write
  and restores durability without any mounted volume. Data-loss tolerance for a personal tracker is high,
  and the mitigation is cheap to build.

### R1.d — Azure Database for PostgreSQL Flexible Server (smallest Burstable, B1ms)
- **Viable and robust** — purpose-built, removes every SQLite-on-network-FS concern; the server owns
  concurrency so the single-writer invariant is no longer load-bearing.
- **Cost:** Burstable **B1ms** (1 vCore / 2 GiB) compute ≈ $12–13/mo + ~32 GiB storage ≈ $3–4 + backup ⇒
  **~$15–17/mo**. West Europe runs above the commonly-quoted East US figures.
  ([PostgreSQL Flexible Server pricing](https://azure.microsoft.com/en-us/pricing/details/postgresql/flexible-server/);
  per-machine-type reference [bytebase](https://www.bytebase.com/dbcost/azure-flexible-server-pricing/).)
  *(Verify West Europe live — burstable SKUs are also occasionally gated in new subscriptions.)*
- **Effort:** rewrite `internal/store` (~318 non-test lines across `store.go` + `crud.go`): `?`→`$N`
  placeholders, `res.LastInsertId()` → `INSERT … RETURNING id` (`crud.go:41`), drop the WAL/`busy_timeout`
  pragmas, switch goose dialect from `DialectSQLite3` to Postgres, add a driver. Bounded but real.
- **Main risk / cost to rationale:** loses the **single deployable unit** (now app **+** managed DB
  service) and the pure-Go single-static-binary story (ADR-0003), and breaks **local parity** (local dev
  now needs a Postgres instead of a file). Disproportionate at a few writes/day — this is the "graduate off
  SQLite" option, justified only once write volume/concurrency outgrows single-writer SQLite.

**Baseline for reference:** the **Azure Files SMB share** in use today is nearly free at this size
(transaction-optimized ~$0.06/GiB-mo ⇒ well under $1/mo). Its problem is *correctness, not cost*.
([Azure Files pricing](https://azure.microsoft.com/en-us/pricing/details/storage/files/).)

## Comparison table

| Candidate | Viable for this write pattern? | ~Monthly cost (West Europe)* | Single-unit / single-writer / local-parity | Main risk |
| --- | --- | --- | --- | --- |
| **R1.c → NFS Azure Files** (managed-disk attach doesn't exist on ACA) | **Yes** — POSIX advisory locks; survives restart **and** reschedule | **~$16+/mo** (SSD/premium, ~100 GiB floor) + VNet/endpoint | ✓ / ✓ / ✓ (still one SQLite file) | Cost floor + mandatory custom VNet & private/service endpoint; network-FS caution + revision-swap WAL window remain |
| **R1.a — Blob via BlobFuse** | **No** — disqualified twice | n/a | n/a | ACA can't mount Blob; and BlobFuse isn't POSIX-compliant / no real locking — worse than SMB |
| **R1.b — ephemeral `EmptyDir` + backup-on-write** | **Yes — best locking story** (real local POSIX locks) | **~$0** extra + cents of Blob for backups | ✓✓ / ✓✓ / ✓✓ (identical to `go run`) | Data wiped on replica reschedule/deploy/scale; mitigated by backup-on-write (loss ≈ one write) |
| **R1.d — PostgreSQL Flexible Server (B1ms)** | **Yes** — purpose-built | **~$15–17/mo** | ✗ / n/a / ✗✗ | `internal/store` rewrite; loses single-binary + local-parity story; overkill at a few writes/day |

*\*All costs are order-of-magnitude from published rate cards; the Azure pricing pages render region
placeholders. Re-verify West Europe in the Pricing Calculator before deciding.*

## Ranked recommendation

1. **R1.b — ephemeral local disk + backup-on-write to Blob.** Best fit for *this* app's stated priorities:
   **cheapest** (~$0 over today), **simplest**, preserves the single static binary / single writer /
   perfect local parity, and — because the DB sits on local ext4 — gives SQLite **genuine POSIX locking**,
   eliminating the `SQLITE_BUSY` root cause outright. The only weakness is durability on reschedule, which
   backup-on-write reduces to ~one write. Recommend unless "never lose a single committed write, with zero
   app-level backup logic" is a hard requirement.

2. **R1.c realized as NFS Azure Files.** The correct "platform-guaranteed durability" answer: gives SQLite
   the POSIX advisory locking SMB lacks and survives restart **and** reschedule, while keeping SQLite and
   local parity. Price is a **~$16/mo floor** plus a **mandatory custom VNet + private/service endpoint** —
   a real cost and infra step-up over ADR-0005, both above the ~$5 target. SQLite's standing caution
   against *any* network FS and the revision-swap WAL window still apply.

3. **R1.d — PostgreSQL Flexible Server (Burstable B1ms).** The robust "graduate off SQLite" option. Choose
   only when write volume/concurrency is expected to outgrow the single-writer assumption. At ~$15–17/mo
   with an `internal/store` rewrite and the loss of the single-binary/local-parity story, it is
   disproportionate to a personal few-writes-a-day tracker today.

4. **R1.a — Blob via BlobFuse. Disqualified.** ACA can't mount Blob, and BlobFuse isn't POSIX-compliant and
   offers no real locking — strictly worse than the SMB setup being replaced.

**Bottom line:** the favourite as literally written (attached managed disk) **doesn't exist on Container
Apps**. The real decision is between *staying cheap and simple with ephemeral + backup-on-write* (#1,
recommended) and *paying ~$16/mo for platform-guaranteed durability via NFS Azure Files* (#2). Postgres is
the escape hatch for when the app outgrows single-writer SQLite. Final choice belongs to the decision
ticket, weighed against the SMB-reproduction verdict.

## Sources

- Azure Container Apps — Use storage mounts (three storage types; no managed-disk/Blob mount; ephemeral
  size table; NFS requires custom VNet): <https://learn.microsoft.com/en-us/azure/container-apps/storage-mounts> (updated 2026-07-21)
- Azure Container Apps — Azure Files volume tutorial (SMB & NFS): <https://learn.microsoft.com/en-us/azure/container-apps/storage-mounts-azure-files>
- NFS Azure Files volume mount on ACA — GA announcement (June 2025): <https://azurefeeds.com/2025/06/05/launched-generally-available-nfs-azure-files-volume-mount-support-in-azure-container-apps/>
- NFS file shares in Azure Files (fully POSIX-compliant; NFSv4.1; SSD/premium-only; no user auth ⇒ private/service endpoint required): <https://learn.microsoft.com/en-us/azure/storage/files/files-nfs-protocol> (updated 2026-07-17)
- SQLite — File Locking And Concurrency (network-FS caution; POSIX advisory locks buggy/unimplemented on NFS): <https://www.sqlite.org/lockingv3.html>
- SQLite — Write-Ahead Logging (WAL shared memory requires same host; "WAL does not work over a network filesystem"): <https://www.sqlite.org/wal.html>
- BlobFuse — What is BlobFuse? (doesn't guarantee full POSIX compliance; non-atomic rename): <https://learn.microsoft.com/en-us/azure/storage/blobs/blobfuse2-what-is> (updated 2026-02-02)
- Azure Files pricing (baseline SMB share): <https://azure.microsoft.com/en-us/pricing/details/storage/files/>
- Azure Database for PostgreSQL Flexible Server pricing: <https://azure.microsoft.com/en-us/pricing/details/postgresql/flexible-server/>
- Azure Flexible Server per-machine-type pricing reference (B1ms ≈ $12–13/mo compute, region-adjusted): <https://www.bytebase.com/dbcost/azure-flexible-server-pricing/>
