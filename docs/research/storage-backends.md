# Storage-backend research — SQLite on Azure Container Apps

Research for [issue #21](https://github.com/emepetres/life-ledger/issues/21). Advisory input to the
persistence decision ticket; the actual choice is made there, weighing this against the SMB-repro
verdict. Companion to [ADR-0003 (persistence & storage)](../adr/0003-persistence-and-storage.md) and
[ADR-0005 (deploy to Azure)](../adr/0005-deploy-azure-cicd.md).

Facts current as of **2026-07-24**. Azure changes fast; each claim is cited and dated where the source
carries a date.

## Question

The current deployment — SQLite (`modernc.org/sqlite`, WAL, `busy_timeout=5000`), one DB file on an
**Azure Files SMB share** mounted at `/data`, on Azure Container Apps (ACA) West Europe at min1/max1 —
crashes with `SQLITE_BUSY` during migration. If SMB must go, **which storage backend is the best fit**
for a single-user, few-writes-a-day tracker that prizes a single deployable unit, cheapness (~$5/mo),
and local-dev parity? Keep SQLite unless forced off it.

## The two findings that reshape the survey

Before the candidate-by-candidate table, two facts from the official ACA docs change the shape of the
answer:

1. **ACA supports exactly three storage types: container-scoped ephemeral, replica-scoped ephemeral
   (`EmptyDir`), and Azure Files (SMB *or* NFS).** The docs state plainly: *"Azure Container Apps
   doesn't support mounting file shares from Azure NetApp Files or Azure Blob Storage."* There is **no
   managed-disk / block-volume attach** for Container Apps at all — that is an AKS / Azure Container
   Instances capability, not an ACA one. ([ACA storage-mounts docs](https://learn.microsoft.com/en-us/azure/container-apps/storage-mounts), updated 2026-07-21.)

   → **R1.c "Azure managed disk attached to the Container App" is not a real ACA option.** The closest
   realizable form of the handoff's favourite is **NFS Azure Files** (a *subtype of Azure Files*, GA
   since June 2025), which is what R1.c is treated as below.

2. **Why SMB breaks SQLite, and why NFS is different.** SQLite's correctness rests on the OS's file-lock
   syscalls behaving as advertised; its docs say *"Your best defense is to not use SQLite for files on a
   network filesystem"* and that *"POSIX advisory locking is known to be buggy or even unimplemented on
   many NFS implementations."* ([SQLite locking docs](https://www.sqlite.org/lockingv3.html).) Two
   distinct issues:
   - **Locking.** Azure Files **SMB does not provide POSIX byte-range advisory locks**; SQLite needs
     them, so a lock that should briefly block instead fails immediately — the `SQLITE_BUSY` seen at
     migration. `busy_timeout` only helps if the lock syscall actually *waits*; on SMB the syscall
     doesn't behave, so the timeout never gets a chance. Azure Files **NFS 4.1**, by contrast, is
     *"fully POSIX-compliant"* and supports *"advisory byte range locking."* ([NFS in Azure Files](https://learn.microsoft.com/en-us/azure/storage/files/files-nfs-protocol); [Azure Files blog](https://azure.microsoft.com/en-us/blog/nfs-41-support-for-azure-files-is-now-in-preview/).)
   - **WAL shared memory.** `journal_mode=WAL` keeps a `-shm` wal-index in shared memory and *"does not
     work over a network filesystem … all readers must exist on the same machine."*
     ([SQLite WAL docs](https://www.sqlite.org/wal.html).) At **min1/max1 this multi-host concern is
     moot** — every connection is in one container on one host — *except* during the ADR-0005
     revision-swap two-writer window, when old and new replicas briefly co-mount. On any network share,
     WAL's cross-host guarantee doesn't hold in that window; on local disk the window can't happen at
     all (each replica has its own disk).

## Candidate comparison

| Candidate | Viable for this write pattern? | ~ Monthly cost (West Europe) | Preserves single-unit / single-writer / local-parity? | Main risk |
| --- | --- | --- | --- | --- |
| **R1.c → NFS Azure Files** (the realizable form; managed-disk attach does **not** exist on ACA) | **Yes** — POSIX advisory byte-range locks supported; survives restart **and** reschedule (data is on the share, not the replica) | **~$16+/mo floor** — NFS is SSD/premium-only, provisioned model, **100 GiB minimum** at ~$0.16/GiB-mo; plus VNet/private-endpoint. Well over the ~$5 target | Single unit ✓, single-writer ✓, local parity ✓ (still one SQLite file, still `LIFELEDGER_DB_PATH`) | Cost floor + mandatory **custom VNet & private/service endpoint** (NFS has no user auth); SQLite-on-network-FS still carries SQLite's blanket caution; WAL cross-host caveat in the revision-swap window remains |
| **R1.a — Azure Blob via BlobFuse** | **No.** Two independent disqualifiers | n/a | n/a | (1) **ACA cannot mount Blob at all** (docs above). (2) Even elsewhere, **BlobFuse is not POSIX-compliant** — it translates FS calls to Blob REST, *"doesn't guarantee full POSIX compliance,"* rename is non-atomic, and it offers no real byte-range advisory locking. Strictly worse than SMB for SQLite. Rule it out. |
| **R1.b — ephemeral / `EmptyDir`** (container-local disk) | **Yes, technically the best locking story** — local ext4 gives *real* POSIX locks; SMB problem disappears entirely | **~$0 extra** (bundled in the replica's 4–8 GiB ephemeral quota); + a few cents for Blob if backing up | Single unit ✓✓, single-writer ✓✓, local parity ✓✓ (identical to `go run`) | **Data loss on replica reschedule / revision deploy / scale event** — ephemeral storage is wiped when the replica shuts down. Mitigation: **backup-on-write** — after each committed write (a handful/day), copy `expenses.db` to a Blob container; restore on boot if local file absent. Shrinks the loss window to ~one write and restores durability without a mounted volume. |
| **R1.d — Azure Database for PostgreSQL Flexible Server** (smallest Burstable) | **Yes** — purpose-built; removes every SQLite-on-network-FS concern | **~$15–17/mo** — Burstable **B1ms** (1 vCore/2 GiB) compute ≈ $12–13 + 32 GiB storage ≈ $3–4 + backup. (Azure pricing page returns region placeholders; verify in the Pricing Calculator for West Europe.) | Single unit ✗ (now app **+** a managed DB service), single-writer n/a (server handles concurrency), local parity ✗✗ (local dev needs a Postgres, breaking the zero-config `./data/expenses.db` story) | Rewrite of `internal/store` (~120 lines, `database/sql` + goose): `?`→`$N` placeholders, `LastInsertId()`→`INSERT … RETURNING id`, drop the WAL/`busy_timeout` pragmas, goose dialect → `postgres`. Loses the pure-Go single-static-binary rationale (ADR-0003). Real but bounded effort; **overkill** at a few writes/day. |

Current baseline for reference: the **Azure Files SMB share** in use today is nearly free at this size
(transaction-optimized ~$0.06/GiB-mo, a tiny DB ⇒ well under $1/mo storage +
negligible transactions). Its problem is correctness, not cost. ([Azure Files pricing](https://azure.microsoft.com/en-us/pricing/details/storage/files/).)

## Ranked recommendation

1. **R1.b — ephemeral local disk + backup-on-write to Blob.** Best fit for *this* app's stated
   priorities. It is the **cheapest** (~$0 over today, minus the SMB share), the **simplest**, keeps the
   single static binary / single-writer / perfect local parity, and — because the DB lives on local
   ext4 — gives SQLite **genuine POSIX locking**, eliminating the `SQLITE_BUSY` root cause outright. The
   only cost is durability on reschedule, and backup-on-write (trivial at a handful of writes/day)
   reduces the loss window to about one write. Recommend this unless "must never lose a single committed
   write, with zero app-level backup logic" is a hard requirement.

2. **R1.c realized as NFS Azure Files.** The "proper" managed-persistence answer and the correct fix if
   durability must be guaranteed by the platform, not by app code: it gives SQLite the POSIX advisory
   locking SMB lacks and survives both restart and reschedule, while keeping SQLite and local parity.
   The price is a **~$16/mo floor** (premium SSD, 100 GiB minimum) and a **mandatory custom VNet +
   private/service endpoint** — both a real step up in cost and infra complexity from ADR-0005, and both
   above the ~$5 target. Note also SQLite's standing caution against *any* network filesystem still
   applies, and the revision-swap WAL window is unchanged.

3. **R1.d — PostgreSQL Flexible Server (Burstable B1ms).** The robust "graduate off SQLite" option.
   Choose only if write volume/concurrency is expected to grow past the single-writer assumption (the
   condition ADR-0005 already names for graduating). At ~$15–17/mo, with an `internal/store` rewrite and
   the loss of the single-binary/local-parity story, it is disproportionate to a personal few-writes-a-
   day tracker today.

4. **R1.a — Blob via BlobFuse. Disqualified.** ACA cannot mount Blob, and BlobFuse is not
   POSIX-compliant and offers no real locking — strictly worse than the SMB setup being replaced.
   (And the literal R1.c "managed disk attached to the Container App" is likewise **not available on
   ACA** — hence its reinterpretation as NFS at #2.)

**Bottom line:** the leading candidate as written (attached managed disk) doesn't exist on Container
Apps. The decision is really between *staying cheap and simple with ephemeral + backup-on-write* (#1,
recommended) and *paying ~$16/mo for platform-guaranteed durability via NFS Azure Files* (#2). Postgres
is the escape hatch for when the app outgrows single-writer SQLite.

## Sources

- Azure Container Apps — Use storage mounts (storage types; no managed-disk/Blob mount): https://learn.microsoft.com/en-us/azure/container-apps/storage-mounts (updated 2026-07-21)
- Azure Container Apps — Azure Files volume tutorial (SMB & NFS): https://learn.microsoft.com/en-us/azure/container-apps/storage-mounts-azure-files
- NFS Azure Files GA announcement (June 2025): https://azurefeeds.com/2025/06/05/launched-generally-available-nfs-azure-files-volume-mount-support-in-azure-container-apps/
- NFS file shares in Azure Files (POSIX-compliant, byte-range locking, SSD/premium-only, provisioned model): https://learn.microsoft.com/en-us/azure/storage/files/files-nfs-protocol (updated 2026-07-17)
- NFS 4.1 for Azure Files — advisory byte-range locking (Azure blog): https://azure.microsoft.com/en-us/blog/nfs-41-support-for-azure-files-is-now-in-preview/
- SQLite — File Locking And Concurrency (network-FS caution, POSIX advisory locks): https://www.sqlite.org/lockingv3.html
- SQLite — Write-Ahead Logging (WAL shared memory requires same host): https://www.sqlite.org/wal.html
- BlobFuse — What is BlobFuse? (not fully POSIX-compliant, non-atomic rename): https://learn.microsoft.com/en-us/azure/storage/blobs/blobfuse2-what-is (updated 2026-02-02)
- Azure Files pricing: https://azure.microsoft.com/en-us/pricing/details/storage/files/
- Azure Database for PostgreSQL Flexible Server pricing: https://azure.microsoft.com/en-us/pricing/details/postgresql/flexible-server/
- Azure Flexible Server per-machine-type pricing (B1ms ≈ $12/mo compute, region-adjusted): https://www.bytebase.com/dbcost/azure-flexible-server-pricing/
