# Deployment host candidates for a Go + SQLite container

Research for [issue #91](https://github.com/emepetres/life-ledger/issues/91), part of map
[#90 (Leave Azure)](https://github.com/emepetres/life-ledger/issues/90). **Advisory** input — this
document gathers evidence and ranks candidates, it does **not** make the choice. That belongs to a
later decision ticket and its ADR (superseding [ADR-0003](../adr/0003-persistence-and-storage.md) and
[ADR-0005](../adr/0005-deploy-azure-cicd.md)).

Facts checked on **2026-09-12**; every claim is cited to a primary source (official docs, pricing
pages, first-party changelogs). Several vendor pricing pages render their tables in JavaScript and
return no figures to a fetcher — where that happened it is flagged inline as **unverified** rather
than filled in from memory.

> **Two findings invalidated the ticket's own premises and are flagged in place below:** Oracle
> **halved** the Always Free Arm allowance effective 2026-08-18, and Hetzner **raised cloud server
> prices** and **renamed the CX line** on 2026-06-15 — the "CX22" the ticket names no longer exists.

## The workload, restated as host requirements

One static Go binary (`modernc.org/sqlite`, distroless image, port 8080, `/health`), one SQLite file
with `journal_mode=WAL` + `busy_timeout=5000`, one user, a handful of writes a day. From that:

1. **A filesystem that provides genuine POSIX byte-range advisory locks.** This is the hard gate.
   ADR-0003 records the proof: Azure Files SMB from a Linux CIFS mount fails `CREATE TABLE` with
   `SQLITE_BUSY` under *every* pragma set tried, because SMB never exposes the lock primitive, so
   `busy_timeout` never gets a lock to wait on. SQLite's own manual is blunt: *"Your best defense is
   to not use SQLite for files on a network filesystem."*
   ([SQLite locking](https://www.sqlite.org/lockingv3.html), checked 2026-09-12.) **"Has a persistent
   volume" is therefore not the question — "is it a local block device with an ext4-class filesystem"
   is.**
2. **WAL additionally requires same-host access.** *"All processes using a database must be on the
   same host computer; WAL does not work over a network filesystem."*
   ([SQLite WAL](https://www.sqlite.org/wal.html), checked 2026-09-12.) At one replica this is only
   at risk during a deploy overlap — which is exactly the ADR-0003 revision-swap window.
3. **Always-on**, or a wake so fast it is unnoticeable from a phone on mobile data.
4. **Public HTTPS** on a provided hostname, custom domain a bonus.
5. **Outbound network** for pushing backups/snapshots to an object store.

Note the asymmetry this creates: a host with **no** persistent disk is not automatically out — the
current architecture already survives that (ephemeral local ext4 + backup-on-write). What is fatally
out is a host whose *only* persistence is a **network filesystem without POSIX locks**. That is a
worse position than having no disk at all.

## Comparison table

All monthly figures are for a single always-on instance at this workload. €/$ are quoted in the
currency the vendor publishes; no conversion is applied.

| Host | Storage for the DB file | POSIX locks? | Always-on? | Cost/mo | Card? | EU | GHCR pull | HTTPS |
|---|---|---|---|---|---|---|---|---|
| **Oracle Cloud Always Free** | Block Volume, 200 GB combined, own ext4 | **Yes** (real block device) | Yes, but idle-reclaim policy | **€0** | **Yes** | Yes, many | Yes (it's a VM) | DIY (Caddy/LB) |
| **Hetzner CX23** | Local NVMe, 40 GB | **Yes** | Yes, unconditional | **€5.99** (€5.49 + €0.50 IPv4, ex-VAT) | Yes | Yes (DE/FI) | Yes (it's a VM) | DIY (Caddy) |
| **Fly.io (Machines + Volumes)** | Fly Volume = slice of NVMe **on the same physical host** | **Yes** | Yes, if autostop disabled | ~$2.02 machine + $0.15/GB vol | **Yes** | Yes (AMS/FRA/CDG) | Not documented | Built-in |
| **Railway** | Volume (type undocumented) | Undocumented | Yes (sleeping is opt-in) | **$5** Hobby, incl. $5 usage | **Yes** | Yes (AMS) | Yes | Built-in |
| **Render** | Persistent disk, paid plans only | Undocumented | Free tier **sleeps**; paid always-on | ~$7 + $0.25/GB (price unverified) | Yes | Yes (Frankfurt) | Yes | Built-in |
| **Koyeb** | Volumes: **not** on Free/eco instances; public preview | Undocumented | Free instance **sleeps at 1 h** | $0 (no disk) or **$29** Pro (for volumes) | Yes | Yes (Frankfurt) | Yes | Built-in |
| **Google Cloud Run** | **No block device.** GCS FUSE or NFS only | **No** (FUSE); Filestore NFS unaffordable | Only via min-instances (billed) | Free tier insufficient for always-on | Yes | Yes | Yes (Artifact Registry preferred) | Built-in |
| **Cloudflare Containers** | *"All disk is ephemeral"* | n/a | Sleeps after 10 min by default | $5 Workers Paid, allowances too small | Yes | Yes | Yes | Built-in |

## Per-candidate notes and the sharp edge

### Oracle Cloud Always Free

The only candidate that is simultaneously **€0 hard**, **real persistent block storage**,
**always-on**, and **EU-regioned**. Current allowances
([OCI Always Free Resources](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm),
checked 2026-09-12):

- **Compute:** 2 × `VM.Standard.E2.1.Micro` (AMD, 1/8 OCPU, 1 GB RAM each), **plus** OCI Ampere A1
  Arm at *"1,500 OCPU hours and 9,000 GB hours per month"* — which the docs restate as *"Maximum 2
  OCPUs and 12 GB of memory across all OCI Ampere A1 Compute instances"*
  ([OCI Free Tier overview](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier.htm),
  checked 2026-09-12).
- **Storage:** *"200 GB of combined boot volume and block volume storage"*, 47 GB minimum boot volume
  per instance, 5 volume backups. Must be created in the tenancy's **home region** to qualify.
- **Network:** 10 TB/month outbound; 1 Always Free Flexible Load Balancer at 10 Mbps.
- **Regions:** `eu-frankfurt-1`, `eu-amsterdam-1`, `eu-madrid-1`/`-3`, `eu-paris-1`, `eu-marseille-1`,
  `eu-milan-1`, `eu-turin-1`, `eu-stockholm-1`, `eu-zurich-1`
  ([OCI regions](https://docs.oracle.com/en-us/iaas/Content/General/Concepts/regions.htm), checked
  2026-09-12).
- **Card:** required. *"Most users need a mobile phone number and a credit card to create an
  account"*, and *"Your credit card will NOT be charged unless you upgrade your account"*
  (freetier.htm).
- **Free-forever:** yes. Always Free *"never expires"*; after the 30-day / $300 trial ends the
  account stays active and Always Free resources *"continue uninterrupted"* (freetier.htm).

> **⚠️ STALE-KNOWLEDGE FLAG — the Arm allowance was halved in August 2026.** The well-known
> "4 OCPU / 24 GB Ampere A1 for free" figure — which is what a pre-2026 model of this platform will
> reproduce — is **no longer current**. Oracle's own docs now state the cap as **2 OCPUs and 12 GB**,
> and describe the enforcement: *"If you have more than 2 OCPUs/12 GB of Ampere A1 instances
> provisioned, all instances are disabled"* and *"deleted after 30 days unless you upgrade to a paid
> account"* (freetier.htm, checked 2026-09-12). The change took effect **2026-08-18** and was made
> with no blog post or changelog entry — only a documentation edit and a late "Action Required: OCI
> Always Free Update" email. (Secondary corroboration of the date and the silence:
> [InfoQ](https://www.infoq.com/news/2026/07/oracle-cloud-free-tier-limits/); the *limit itself* is
> cited to Oracle's docs above, not to InfoQ.)
>
> **For Life Ledger this changes nothing operationally** — 2 OCPU / 12 GB is still absurdly generous
> for one Go binary — but it is a direct, recent demonstration of the platform risk below.

**Sharp edge: Oracle can, and demonstrably does, take it away.** Two mechanisms, both first-party:

1. **Idle reclamation, and our workload is textbook idle.** Verbatim from the docs: *"Idle Always Free
   compute instances may be reclaimed by Oracle. Oracle will deem virtual machine and bare metal
   compute instances as idle if, during a 7-day period, the following are true: CPU utilization for
   the 95th percentile is less than 20%; Network utilization is less than 20%; Memory utilization is
   less than 20% (applies to A1 shapes only)."* A personal expense tracker serving a handful of
   requests a day sits at ~0% on all three. Note the **AND** — an A1 instance escapes if it holds
   *any one* metric above 20%, and memory is the cheap one to hold (a large, honest page cache or a
   sized runtime). On the AMD `E2.1.Micro` shapes the memory clause does not apply, so only CPU or
   network can save you. This is a real design constraint, not a footnote: **choosing Oracle means
   deliberately engineering the instance to not look idle.**
2. **Unilateral, unannounced terms changes.** The August 2026 halving is the proof. The €0 is real,
   but it is €0 at Oracle's discretion, revised without notice.

Plus the well-documented-by-absence friction: signup requires a card and phone verification, and Arm
A1 capacity in popular EU regions is frequently unavailable ("Out of host capacity") — the latter is
widely reported but **not** stated in any Oracle primary source, so it is recorded here as an
unverified operational risk to test empirically, not as a fact.

TLS is DIY: the Always Free load balancer exists, but the simplest path is Caddy on the instance with
automatic Let's Encrypt. Outbound is unrestricted (10 TB/mo).

### Hetzner (CX23 — *not* CX22)

> **⚠️ THE TICKET'S "CX22" NO LONGER EXISTS, AND PRICES WENT UP.** Hetzner applied a price adjustment
> *"starting on the 15 June 2026; 8 AM CEST"*
> ([Hetzner price adjustment](https://docs.hetzner.com/general/infrastructure-and-availability/price-adjustment/),
> checked 2026-09-12). The CX line is now **CX23/CX33/CX43/CX53**, and the shared-vCPU lines rose
> sharply — the CPX line roughly **2.4×** (CPX22 €7.99 → €19.49). The CX line rose more gently:

| Model | Old €/mo | New €/mo |
|---|---|---|
| CX23 | 3.99 | **5.49** |
| CX33 | 6.49 | 8.49 |
| CX43 | 11.99 | 15.99 |
| CX53 | 22.49 | 29.49 |

> Figures are **excl. IPv4** and excl. VAT. The primary IPv4 adds **€0.50/mo**
> ([Hetzner IP pricing](https://docs.hetzner.com/general/infrastructure-and-availability/ipv4-pricing/),
> checked 2026-09-12; *"The prices do not include VAT"*). **CX23 all-in ≈ €5.99/mo ex-VAT**, which is
> **over** the map's €5 ceiling before VAT is even applied. Any prior mental figure of "€3.79 for a
> CX22" is stale by three months.

CX23 is 2 vCPU / 4 GB / 40 GB NVMe, 20 TB traffic, locations `FSN1`/`NBG1` (Germany) and `HEL1`
(Finland) ([Hetzner cost-optimized](https://www.hetzner.com/cloud/cost-optimized/), checked
2026-09-12). It is a plain VM: local NVMe, your own ext4, genuine POSIX locks, no idle policy, no
sleep, no reclamation — **the storage question is simply not a question here.** Docker pulls from a
private GHCR are ordinary `docker login` operations.

**Sharp edge: it is the only candidate where *you* are the platform.** There is no managed TLS, no
managed deploy, no managed anything — you own kernel updates, unattended-upgrades, the firewall, the
reverse proxy, fail2ban, and the restore drill. That is a real, recurring maintenance tax on a
personal project, and it is the exact cost the ADR-0003 "single deployable unit" preference was trying
to avoid. It is also, notably, **the same tax as the mini-PC candidate but paid in cash instead of
electricity** — which makes Hetzner hard to justify *against* the mini-PC on merits, since the mini-PC
is already owned. Secondary edge: at €5.99 ex-VAT it breaches the €5 preference, and the June 2026
adjustment shows the price is not fixed. Also observed on 2026-09-12: **every CX model displayed
"not available"** on Hetzner's own cost-optimized page across EU locations — possibly transient stock
exhaustion post-price-change, but it must be re-checked before committing.

### Fly.io (Machines + Volumes)

Technically the best fit on the storage axis of any managed platform. Fly Volumes are *"a slice of an
NVMe drive on the same physical server as the Machine on which it's mounted"*
([Fly Volumes overview](https://fly.io/docs/volumes/overview/), checked 2026-09-12) — a genuine local
block device, not a network share, so POSIX locks and WAL both behave. A machine can mount exactly one
volume and a volume attaches to exactly one machine, which matches the single-writer invariant
perfectly.

Costs ([Fly pricing](https://fly.io/docs/about/pricing/), checked 2026-09-12): `shared-cpu-1x` with
256 MB ≈ **$2.02/mo** in Amsterdam, volumes **$0.15/GB/mo**, EU egress $0.02/GB, first 10 GB of
snapshots free each month. A 1 GB volume brings the total to roughly **$2.20/mo** — comfortably inside
the ceiling. No organization plan fee; support plans ($29+) are optional add-ons
([Fly plans](https://fly.io/plans), checked 2026-09-12). Always-on is a config choice: the `fly launch`
defaults are `auto_stop_machines = "stop"`, `min_machines_running = 0`, and you turn it off by setting
*"auto_stop_machines to "off" and auto_start_machines to false"*
([autostop/autostart](https://fly.io/docs/launch/autostop-autostart/), checked 2026-09-12). HTTPS on
`*.fly.dev` is automatic.

**Sharp edge: Fly's own docs tell you not to run what we want to run.** Verbatim: *"Always provision
at least two volumes per app"* — because volumes have no replication (*"Volumes don't have built-in
replication between them, so your app or database needs to take care of replicating data between
volumes"*) and a single machine+volume pair means a host hardware failure is simultaneous **downtime
and data loss**. Daily snapshots exist (5-day default retention) but the docs warn *"snapshots
shouldn't be your primary backup method"*. For Life Ledger this is survivable — ADR-0003's
backup-on-write already assumes the local disk is disposable — but it means **the persistent volume
does not retire the backup mechanism**, which was one of the hoped-for simplifications. Secondary
edge: **no documented support for pulling from a private external registry.** `fly deploy --image`
exists, but the flag list has no `--registry-username`/`--registry-password` equivalent
([fly deploy reference](https://fly.io/docs/flyctl/deploy/), checked 2026-09-12), and the docs cover
only `registry.fly.io` — so a private-GHCR pipeline would need a re-push step. Card required for most
accounts; the alternative is a **$25 minimum** credit purchase
([Fly billing](https://fly.io/docs/about/billing/), checked 2026-09-12).

### Railway

$5/mo Hobby plan *includes* $5 of resource usage, so a 512 MB always-on service plausibly lands at
exactly the ceiling; there is also a $0 Free plan with $1 monthly credit and a one-time $5 trial grant
([Railway plans](https://docs.railway.com/reference/pricing/plans), checked 2026-09-12). Volumes are
$0.15/GB/mo, capped at **5 GB on Hobby**, one per service, and *"Replicas cannot be used with
volumes"* — which suits us
([Railway volumes](https://docs.railway.com/reference/volumes), checked 2026-09-12). EU West Metal
(Amsterdam, `europe-west4-drams3a`) is available
([Railway regions](https://docs.railway.com/reference/regions), checked 2026-09-12). Sleeping is
opt-in, not default. Managed TLS, GHCR image deploys supported.

**Sharp edge: the storage medium is undocumented — the one fact we most need.** Railway's volume docs
describe size limits, billing and scaling constraints but never say whether the backing store is a
local block device or a network filesystem, and there is no POSIX-locking statement anywhere. Given
ADR-0003, **"it has volumes" is precisely the claim that burned this project on Azure Files**; Railway
cannot be selected without an empirical `CREATE TABLE`-under-WAL probe on a real volume, the same
shape of test as [#20](https://github.com/emepetres/life-ledger/issues/20). Secondary edges: the docs
note *"there will be a small amount of downtime when re-deploying a service that has a volume
attached"* (the revision-swap window, made explicit and arguably *safer* than ACA's overlap), *"as of
March 30th, Railway requires the use of a post-paid card"*, and the $5 includes usage — so the bill is
$5 *floor*, not $5 *cap*, if anything grows.

### Render

The free tier is disqualified on two independent counts
([Deploy for Free](https://render.com/docs/free), checked 2026-09-12): Render *"spins down a Free web
service that goes 15 minutes without receiving any inbound traffic"* with a restart that *"takes about
one minute"* — a one-minute cold start on a phone on mobile data is not "unnoticeable" — and free web
services ***cannot*** attach persistent disks, with an ephemeral filesystem where changes are *"lost
every time the service redeploys, restarts, or spins down"*. There is also a 750 free instance-hour
monthly cap, below a full month's 730×… (it covers exactly one service, with no headroom).

Paid: disks attach to *"a paid Render web service, private service, or background worker"*, are
accessible by *"only a single service instance"*, block horizontal scaling, and disable zero-downtime
deploys (*"Render stops the existing instance before bringing up the new instance"*)
([Persistent Disks](https://render.com/docs/disks), checked 2026-09-12). Disk storage is **$0.25/GB/mo**
(Render docs, via search snippet — the pricing page itself is JS-rendered and returned no table, so
this figure is **lower-confidence**). Frankfurt is an available region. The cheapest paid plan is
**Starter** (0.5 CPU / 512 MB) ([Compute plans](https://render.com/docs/compute-plans), checked
2026-09-12); its published monthly price is **unverified** here for the same JS-rendering reason —
treat any "$7" figure as needing confirmation.

**Sharp edge: the free tier and the persistent disk are mutually exclusive by design**, so Render can
only compete as a paid host, at which point it is the most expensive candidate that still lacks a
documented POSIX-locking guarantee. Its single genuine virtue over Railway is that the single-instance
and stop-before-start semantics are spelled out, which would *close* ADR-0003's revision-swap clobber
window rather than merely shrink it.

### Koyeb

Free instance still exists: 0.1 vCPU / 512 MB / 2 GB SSD, one per organization, Frankfurt or
Washington DC ([Koyeb instances](https://www.koyeb.com/docs/reference/instances), checked 2026-09-12).
Two lines from that page end its candidacy for our shape:

> *"They scale down to zero when they don't receive any traffic for 1 hour"*
> *"They can't be used with Volumes to add persistent storage"*

And from the volumes reference ([Koyeb volumes](https://www.koyeb.com/docs/reference/volumes), checked
2026-09-12): *"Volumes can only be attached to standard and GPU Services. You cannot attach a volume to
`eco-*` or `free` Instance"*, sizes *"must be between 1 and 10 Gigabytes"*, Frankfurt/DC only,
*"Volumes currently only work with Services with a scale of one"*, and — decisively — volumes are in
**public preview** and *"are currently only suitable for testing"*.

**Sharp edge: the free path has no disk, and the disk path is not free — it is $29.** The cheapest
paid instances (`eco-nano` $1.61/mo, `nano` $2.68/mo) are inside budget but **`eco-*` is explicitly
excluded from volumes**, and NVMe volumes are listed as a Pro-plan feature at **$29/mo**
([Koyeb pricing](https://www.koyeb.com/pricing), checked 2026-09-12). So Koyeb is either €0 with
ephemeral storage and a 1-hour sleep, or €29 — nothing in between. Even the €29 tier ships storage the
vendor labels test-only.

### Google Cloud Run

**Disqualified on storage, independently of price.** Cloud Run offers in-memory, Cloud Storage (GCS
FUSE), NFS/Filestore and secret volume mounts — **no attached block device**. And the FUSE option
fails ADR-0003's gate in the vendor's own words
([Cloud Storage volume mounts](https://docs.cloud.google.com/run/docs/configuring/services/cloud-storage-volume-mounts),
checked 2026-09-12): *"Cloud Storage FUSE does not provide concurrency control for multiple writes
(file locking) to the same file"* and *"Cloud Storage FUSE is not a fully POSIX-compliant file
system."* This is the **exact failure mode** ADR-0003 already paid for on Azure Files SMB, and the
same reason ADR-0003 disqualified BlobFuse. Filestore NFS would provide locks but starts around a
terabyte and hundreds of €/mo — categorically out.

Price makes it worse rather than better. Always-on requires `min-instances ≥ 1`, and under
instance-based billing *"you are billed the default rate for the entire instance lifecycle"*
([min instances](https://docs.cloud.google.com/run/docs/configuring/min-instances), checked
2026-09-12), with a 512 MiB memory floor
([billing settings](https://docs.cloud.google.com/run/docs/configuring/billing-settings), checked
2026-09-12). The Always Free allowance is *"2 million requests per month, 360,000 GB-seconds of memory,
180,000 vCPU-seconds of compute time"*
([GCP free tier](https://docs.cloud.google.com/free/docs/free-cloud-features), checked 2026-09-12).
A month is ~2.6 million seconds, so a single always-allocated vCPU consumes ~14× the free vCPU-second
grant. Always-on Cloud Run is not a free-tier workload.

**Sharp edge: Cloud Run is the Azure Container Apps mistake with a different logo.** Same serverless
shape, same absence of block storage, same network-filesystem-without-locks as the only "persistent"
option. Choosing it would re-adopt the exact constraint this map exists to escape. (Worth noting for
completeness: GCP's *other* always-free item, one `e2-micro` Compute Engine VM with 30 GB standard
persistent disk, **is** a real VM with real locks — but it is restricted to `us-west1`/`us-central1`/
`us-east1`, i.e. **no EU**, and 1 GB/mo North-America egress.)

### Cloudflare Containers

**Disqualified on storage, in one sentence from the docs**
([Containers platform details](https://developers.cloudflare.com/containers/platform-details/), checked
2026-09-12): *"All disk is ephemeral. When a Container instance goes to sleep, the next time it is
started, it will have a fresh disk as defined by its container image."* Containers also sleep — the
platform *"sets sleepAfter to 10 minutes by default"*.

Cost rules it out a second time. Containers require the **$5/mo Workers Paid plan** (no free option),
which includes *"25 GiB-hours/month"* of memory
([Containers pricing](https://developers.cloudflare.com/containers/pricing/), checked 2026-09-12). The
smallest instance type, `lite`, is 256 MiB — always-on for a 730-hour month is 0.25 × 730 ≈ **182
GiB-hours**, over **7×** the included allowance, before CPU (375 vCPU-minutes/month included, against
43,800 wall-clock minutes) is counted. Always-on is simply not the billing model.

**Sharp edge: the platform's persistence story is "don't use the filesystem" — use Durable Objects.**
Cloudflare does offer SQLite-backed Durable Objects, but that is a *rewrite into a different runtime
and storage API*, not a host for our binary. It fails the map's "keep the single static Go binary +
SQLite design" preference at the first step.

## Ranked shortlist

Judged against the map's standing preferences: **€0 preferred / ≤€5 acceptable**, **always-on
required**, **EU preferred not a veto**, **GHCR registry preferred not a gate**, **keep the single Go
binary + SQLite shape**.

1. **Fly.io (Machines + a small Volume) — ~$2.20/mo.** The only *managed* platform where the storage
   medium is documented as a local NVMe slice on the machine's own host, which is the single hardest
   requirement and the one every other managed candidate leaves unstated. Always-on is one config
   line, EU regions exist, TLS is free and automatic, the cost is under half the ceiling. Pay for it
   with: a card (or $25 of credit), an undocumented private-GHCR path, and Fly's own warning that a
   single volume is a single point of failure — meaning **keep the backup-on-write mechanism**, which
   is ADR-0003's design anyway, so nothing is actually lost.
2. **Oracle Cloud Always Free — €0.** Ranks second only because of platform risk, not capability: on
   paper it wins outright (€0, real block storage, always-on, EU, 200 GB, 10 TB egress). But it is
   the only candidate that requires **engineering the instance to not look idle** to keep it, and the
   August 2026 halving proves the terms change unilaterally and unannounced. Rank it first instead of
   second if €0 is being treated as closer to a hard constraint than a preference — the gap is
   narrow, and the honest tiebreaker between #1 and #2 is "$2/mo" versus "a standing obligation to
   care about Oracle's reclamation heuristics."
3. **Railway (Hobby, $5/mo) — conditional.** Suits the shape (EU, always-on, managed TLS, GHCR,
   one-volume-one-replica), sits exactly at the ceiling, but is **blocked on an unanswered question**:
   nothing in Railway's docs establishes the volume's filesystem or its locking behaviour. Promote it
   above Render only if a probe confirms POSIX locks; otherwise it carries the Azure Files risk
   unexamined.
4. **Hetzner CX23 — €5.99/mo ex-VAT.** Zero technical risk and total control, but it now **breaches
   the €5 preference**, and everything it offers over the mini-PC candidate is offered *by* the mini-PC
   for no recurring cash. Keep it as the "guaranteed to work, costs money and time" fallback.
5. **Render (paid) — ~$7+/mo.** Free tier structurally excluded (sleeps, and cannot have a disk); paid
   tier is above budget with the same undocumented-locking gap as Railway. Its one edge — explicit
   stop-before-start deploys, which closes ADR-0003's clobber window — isn't worth the premium.
6. **Koyeb.** Free means no disk and a 1-hour sleep; disks mean $29/mo *and* a vendor label of
   "suitable for testing" only. No viable middle.
7. **Google Cloud Run.** Disqualified: no block device, and the only network option is documented as
   lacking file locking and full POSIX compliance — the Azure Files failure re-run. Always-on also
   busts the free tier by ~14×.
8. **Cloudflare Containers.** Disqualified: *"All disk is ephemeral"*, sleeps by default, and
   always-on exceeds the paid plan's included memory allowance by over 7×.

**Not assessed here:** the **mini-PC (Proxmox, at home)**, which map #90 names an equal candidate. It
is out of this ticket's scope but it dominates #4 on every axis this document measures, and any
decision ticket should weigh it directly against #1 and #2 rather than against Hetzner.

### The one probe worth running before deciding

Every managed candidate except Fly.io fails on the *same* unanswered question, and ADR-0003 already
contains the test that answers it: create the DB, open it with `journal_mode=WAL` and
`busy_timeout=5000`, and run goose's `CREATE TABLE` against the mounted volume — the precise repro
from [#20](https://github.com/emepetres/life-ledger/issues/20). It takes minutes and it is the
difference between "the docs don't say" and a fact. Note ADR-0003's **dev-parity trap** when doing so:
the SMB repro *passed* from a Windows client and failed only on Linux. **Probe from the deployed
Linux container, never from the dev box.**
