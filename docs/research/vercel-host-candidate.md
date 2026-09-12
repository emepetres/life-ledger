# Vercel as a deployment host for a Go + SQLite container

Research for [issue #97](https://github.com/emepetres/life-ledger/issues/97), part of map
[#90 (Leave Azure)](https://github.com/emepetres/life-ledger/issues/90). **Advisory** input — it
closes the gap left by [#91](https://github.com/emepetres/life-ledger/issues/91), which surveyed
eight hosts and never evaluated Vercel. Judged on the same axes, against
[ADR-0003](../adr/0003-persistence-and-storage.md)'s hard gate.

Facts checked on **2026-09-12** against Vercel's own current documentation (`vercel.com/docs`,
`vercel.com/pricing`, `vercel.com/changelog`). Every claim below is cited to the page it came from;
where Vercel stamps a page with a "last updated" date it is recorded, because two of #91's findings
turned on silent vendor doc edits. Where the docs do not say, this document says **not documented**
rather than filling the gap from recall.

> **Verdict up front: DISQUALIFIED** — for the same reason ADR-0003 already paid for once. Vercel
> has no always-on request-serving process and no persistent POSIX block device attachable to one.
> **There is nothing on Vercel for [#96](https://github.com/emepetres/life-ledger/issues/96) to
> probe.** #96's scope stands at Fly / Oracle / Railway / mini-PC.

> **⚠️ Two of the ticket's own premises are out of date and are flagged in place below.** Vercel
> shipped **Dockerfile-based Functions** on 2026-06-30 (so "Vercel cannot run our container at all"
> is no longer true), and **Sandbox Drives** — a genuinely persistent, mountable disk — exists in
> beta (so "Vercel Sandbox is not a block device" is now only half right). Neither rescues Vercel;
> both change *why* it fails, and both are recent enough that a pre-2026 mental model gets them
> wrong.

## The gate, restated

From #91 and ADR-0003, unchanged: the hard requirement is **a local block device with an
ext4-class filesystem providing genuine POSIX byte-range advisory locks**, under a process that
stays up. "Has persistent storage" is not the question — Azure Files SMB had persistent storage and
still failed `CREATE TABLE` with `SQLITE_BUSY` under every pragma set tried, because SMB never
exposes the lock primitive. WAL adds a second condition: *"All processes using a database must be on
the same host computer; WAL does not work over a network filesystem"*
([SQLite WAL](https://www.sqlite.org/wal.html), checked 2026-09-12).

Vercel must therefore answer three questions in the affirmative. It answers **no** to all three.

## Answers, in one table

| Axis | Vercel | Verdict |
|---|---|---|
| Always-on process | None. Functions scale to zero; Fluid shares one instance across concurrent invocations but does not keep it alive. Container Functions scale down after **5 min idle** (prod). | ❌ |
| Max continuous execution | Hobby **300 s**; Pro/Enterprise **800 s** GA, **1800 s** (30 min) beta. Sandbox session: **45 min** Hobby / **24 h** Pro. | ❌ |
| Persistent POSIX block device on a Function | **None exists.** No volume or disk product attaches to a Function. | ❌ |
| Persistent disk anywhere on the platform | **Sandbox Drives (beta)** — real, mountable, survives sandbox stop. Attaches to **Sandbox only**, never to a Function. | ⚠️ wrong product |
| SQLite POSIX byte-range locks on a durable file | Not available in any request-serving context. | ❌ |
| Cost of always-on | No always-on primitive to price. Nearest paid analogue (continuous Sandbox) is **~2 orders of magnitude** over €5/mo. | ❌ |
| EU regions | Yes — `fra1`, `cdg1`, `arn1`, `dub1`. Hobby may select **one**. | ✅ |
| Public GHCR image deploy | **Not as a deploy input.** Vercel builds from a `Dockerfile.vercel` into its **own** registry (VCR). | ❌ |

## 1. Is there an always-on process at all?

**No.** Every compute primitive Vercel documents is request-driven, time-bounded, or both.

- **Vercel Functions** ([docs/functions](https://vercel.com/docs/functions), updated 2026-09-03):
  *"Vercel creates a new function invocation for each incoming request… Vercel scales your functions
  down to zero when there are no incoming requests."* Go is a supported runtime, so the *binary*
  could run — it simply cannot stay running.
- **Fluid Compute** ([docs/fluid-compute](https://vercel.com/docs/fluid-compute), updated
  2026-08-24) is the default for new projects since 2025-04-23 and is routinely mistaken for an
  always-on server. It is not. It changes the **concurrency** model — *"multiple invocations can
  share the same physical instance… concurrently"* — plus bytecode caching and pre-warming to cut
  cold starts. Instance reuse is opportunistic: Vercel documents no guaranteed lifetime, and nothing
  promises the process outlives an idle window.
- **`maxDuration`**
  ([configuring-functions/duration](https://vercel.com/docs/functions/configuring-functions/duration)
  and [functions/limitations](https://vercel.com/docs/functions/limitations), both updated
  2026-08-24):

  | Plan | Default | Standard max | Extended max (beta) |
  |---|---|---|---|
  | Hobby | 300 s | 300 s | — |
  | Pro | 300 s | 800 s | 1800 s |
  | Enterprise | 300 s | 800 s | 1800 s |

  These ceilings are **substantially higher than a pre-2026 model of Vercel will reproduce** (the
  old 10 s / 60 s Hobby figures are long superseded). The direction of travel does not matter:
  30 minutes is not always-on.
- **`waitUntil` / background work**
  ([`@vercel/functions` reference](https://vercel.com/docs/functions/functions-api-reference/vercel-functions-package),
  updated 2026-09-03): *"Promises passed to `waitUntil()` will have the same timeout as the function
  itself. If the function times out, the promises will be cancelled."* There is no separate
  long-running lane.
- **Cron Jobs** ([docs/cron-jobs](https://vercel.com/docs/cron-jobs), updated 2026-08-11) fire an
  HTTP GET at an ordinary Function; the invocation is bound by the same `maxDuration`.
- **Container Functions** — see §6. Stateless and scale-to-zero in Vercel's own description.
- **Vercel Sandbox** ([docs/sandbox](https://vercel.com/docs/sandbox), updated 2026-09-03) is the
  only thing that runs for hours: Firecracker microVMs explicitly for *"untrusted or agent-generated
  code"*. Session cap **45 min (Hobby) / 24 h (Pro, Enterprise)**
  ([sandbox/pricing](https://vercel.com/docs/sandbox/pricing), updated 2026-09-02). Persistent
  sandboxes ([persistent-sandboxes](https://vercel.com/docs/sandbox/concepts/persistent-sandboxes),
  updated 2026-08-25) auto-snapshot on stop and auto-resume, so the *sandbox* is effectively
  unbounded — but it is dormant between sessions, not serving.
- **Vercel Workflows** ([docs/workflows](https://vercel.com/docs/workflows), updated 2026-09-04) is
  durable-execution orchestration: steps run as ordinary Functions, state persists to a managed
  database. Pausing *"for minutes or months"* is orchestration state, not a live process holding an
  open file descriptor.
- **Changelog sweep** for any 2025/2026 always-on or long-running container product: nothing beyond
  Sandbox (bounded sessions) and Dockerfile Functions (scale-to-zero).

**Nothing on Vercel keeps a Go binary up indefinitely holding an open SQLite handle.**

## 2. Is there a persistent POSIX block device?

**Not one a Function can use.** Taking the ticket's three survival tests in turn — between
invocations, across a redeploy, across scale-to-zero — no writable path on a Function passes even
the first.

- **The Function filesystem.** The deployment is immutable; `/tmp` is the writable scratch and is
  ephemeral per-instance. **The exact `/tmp` size limit is not documented on any current Vercel docs
  page found on 2026-09-12** — the familiar 512 MB figure is inherited AWS Lambda lore, not a Vercel
  statement, and is recorded here as **unverified**. Its size is moot: a scratch that dies with the
  instance is exactly the position ADR-0003 already occupies on ACA's `EmptyDir`, with none of the
  compensating always-on replica.
- **Vercel Blob** ([docs/vercel-blob](https://vercel.com/docs/vercel-blob), updated 2026-08-26) is
  **object storage** — S3-backed, quoted at *"99.999999999% durability"*, driven by an HTTP/SDK API
  (`put` / `get` / `head` / `list` / `del` / `copy`), objects up to 5 TB. It is **not mountable** and
  has no POSIX semantics. It offers `ifMatch`/ETag conditional writes — optimistic concurrency, not
  `fcntl`. In Life Ledger's terms Blob is a **drop-in replacement for the Azure Blob backup sink**
  behind `internal/blobbackup`, i.e. the #92 slot, and nothing more. It is not a place the DB can
  live.
- **Vercel Sandbox** is a **Firecracker microVM**, not storage: *"isolated Linux microVMs"* with
  64 GB of **ephemeral** NVMe storage per sandbox. The filesystem inside is a real POSIX filesystem —
  but it is ephemeral, and it belongs to a product whose unit of work is a bounded session.
- **Sandbox Drives (beta)**
  ([sandbox/concepts/drives](https://vercel.com/docs/sandbox/concepts/drives), updated 2026-09-04)
  is the one genuine persistent-disk primitive Vercel sells: *"Drives provide persistent storage that
  you mount into a sandbox as a working directory… Files written to a drive persist after the sandbox
  stops."* Region-pinned; 1 GiB default on Hobby, up to 16 TiB; **one read-write mount at a time**,
  every other mount read-only from a snapshot.

  > **⚠️ This is the ticket's stale premise.** "Vercel Sandbox is not a block device" is no longer
  > accurate — Drives is one, and its single-read-write-mount rule even mirrors SQLite's
  > single-writer invariant. It changes nothing about the verdict, for a blunt reason: **a Drive
  > mounts to a Sandbox, never to a Function.** Vercel documents no path by which the thing serving
  > HTTPS traffic is the thing holding the disk. Serving a personal expense tracker out of Sandbox
  > would mean re-architecting the app as agent-style on-demand compute, fronted by a Function, with
  > a 24 h session ceiling and resume logic — and at the cost in §4.
- **Marketplace storage** ([docs/storage](https://vercel.com/docs/storage), updated 2026-09-03):
  Postgres/Neon, Upstash Redis, Supabase — all **network-accessed managed databases**, installed via
  `vercel install`. No local files.
- **Global Config** ([docs/global-config](https://vercel.com/docs/global-config), updated
  2026-08-17) — *"Global Config was previously called Edge Config"*, a rename worth knowing — is a
  small, read-optimised KV store for flags and config, written via API in seconds. Not storage.
- **Vercel KV** no longer exists as a first-party branded product; Redis is Marketplace/Upstash.

## 3. Does anything give SQLite genuine byte-range advisory locks?

**No — not in any context that can serve a request.**

- On a **Function** there is no durable local file to lock, so the question never arises.
- **Turso Cloud** ([marketplace/tursocloud](https://vercel.com/marketplace/tursocloud),
  [changelog](https://vercel.com/changelog/turso-cloud-joins-the-vercel-marketplace)) is the obvious
  "but SQLite on Vercel!" answer, and it is a **network service**: libSQL reached over HTTP/gRPC via
  `@libsql/client`. There is no local file and no `fcntl` anywhere in the Function. It is *a
  different database accessed over the wire*, which fails the map's "keep the single static Go binary
  + SQLite file" preference at the first step — exactly as Cloudflare's SQLite-backed Durable Objects
  did in #91.
- On **Sandbox + a Drive**, a mounted NVMe-backed filesystem inside a Linux microVM plausibly *would*
  honour `fcntl F_SETLK` — but Vercel documents neither the filesystem type nor its locking
  behaviour, so this is **not documented**; and it is unreachable from the serving path regardless.

This is the ADR-0003 disqualifier, and Vercel does not clear it.

## 4. Cost of always-on, against the ~€5/mo ceiling

There is **no always-on primitive to price**, which is the honest headline. Pricing the nearest
analogues:

- **Hobby (free)** ([docs/plans/hobby](https://vercel.com/docs/plans/hobby), updated 2026-08-31):
  4 Active-CPU-hours/month, 360 GB-hours provisioned memory, 1,000,000 invocations, `maxDuration`
  300 s. Sandbox on Hobby: 5 Active-CPU-hours, 420 GB-hours, 5,000 creations, 45-minute sessions, and
  creation **pauses for 30 days** once exceeded. Hobby also *"restricts users to non-commercial,
  personal use only"*
  ([fair-use guidelines](https://vercel.com/docs/limits/fair-use-guidelines#commercial-usage)) — a
  single-user personal expense tracker plausibly *is* permitted use, so this clause is **not** the
  disqualifier and is not leaned on here. The allowances are: a continuously-running month is 730
  wall-clock hours against a **4 Active-CPU-hour** grant, so even if an always-on mode existed the
  free tier could not host it.
- **Pro** is **$20/user/month**, converted into a $20/month usage credit
  ([sandbox/pricing](https://vercel.com/docs/sandbox/pricing)) with metered overage beyond it. That
  floor alone is **4× the €5 ceiling**, before a second of compute is billed.
- **Continuous Sandbox**, as the only shape that runs for hours: at the documented rates (Active CPU
  $0.128/vCPU-hour, memory $0.0212/GB-hour, `iad1`), the docs' 2 vCPU / 4 GB shape over a 730-hour
  month is 2 × 730 × $0.128 ≈ $187 plus 4 × 730 × $0.0212 ≈ $62, i.e. **~$249/month**. That total is
  arithmetic performed here from Vercel's published rates, not a figure Vercel quotes. Smaller shapes
  scale it down but not into range — and the 24 h session cap means it is not continuously runnable
  in any case.

**Against a €5/mo ceiling this is not close in either direction: free cannot do it, and paid is off
by two orders of magnitude.**

## 5. EU region availability

**Yes — the one axis Vercel passes.** ([docs/regions](https://vercel.com/docs/regions), updated
2026-08-11; 20 compute regions.) EU: **`fra1`** Frankfurt, **`cdg1`** Paris, **`arn1`** Stockholm,
**`dub1`** Dublin. (`lhr1` London is not EU.)

Functions default to **`iad1`** (Washington DC); the region is changeable in project settings
([configuring-functions/region](https://vercel.com/docs/functions/configuring-functions/region)).
The limitations table states functions run *"in a single region by default (`iad1`)"* and that
*"Pro and Enterprise teams can set multiple regions"* — so **Hobby can move to `fra1` but is capped
at one region**, which is all this workload wants anyway.

## 6. Can a public GHCR image be deployed at all?

**Not as a deploy input — but the reason is now more interesting than "Vercel does not do
containers."**

> **⚠️ Stale premise.** Vercel shipped
> [**"Bring your Dockerfile to Vercel Functions"**](https://vercel.com/changelog/bring-your-dockerfile-to-vercel-functions)
> on **2026-06-30**: *"Vercel Functions now support deploying HTTP servers from a `Dockerfile` or
> `Containerfile`, using OCI compatible images on Fluid compute."* Any model of Vercel formed before
> mid-2026 will say containers are impossible here. They are not.

What it actually is ([kb/guide/docker](https://vercel.com/kb/guide/docker),
[does-vercel-support-docker-deployments](https://vercel.com/kb/guide/does-vercel-support-docker-deployments)):

- You add a **`Dockerfile.vercel`** (or `Containerfile.vercel`) to the repo; on each commit **Vercel
  builds the image itself**, pushes it to the **Vercel Container Registry (VCR)** (`vcr.vercel.com`,
  project-scoped, Docker Registry HTTP API v2 — `docker push`/`pull`/`tag` work), and serves it as a
  Function.
- **No documented path pulls directly from a third-party registry.** A GHCR image would have to be
  **re-pushed into VCR** — the same "re-push step" edge #91 recorded against Fly.io, and a direct
  negation of #93's build-once-in-CI + public-GHCR-pull conclusion.
- Other deploy inputs: git push, `vercel deploy`, and `vercel deploy --prebuilt` (Build Output API).
- **Sandbox** can boot *"your own custom images stored in Vercel Container Registry"* — again VCR,
  again not ghcr.io.
- And decisively: Vercel's own KB states **container-based Functions are stateless** and scale down
  after **5 minutes idle** in production (30 s in preview). Container support does not buy an
  always-on process or a disk; it buys a different way to package the same ephemeral Function.

## Conclusion — where Vercel ranks in #91's shortlist

**It does not rank. It is disqualified, alongside Google Cloud Run and Cloudflare Containers, and
for the same structural reason.** Vercel is a serverless platform whose persistence story is "use a
managed service over the network" — Blob, Postgres/Neon, Turso, Global Config. There is no always-on
process, and no disk that attaches to the thing serving traffic. Choosing it would be the Azure
Container Apps mistake a third time — and worse than ACA in one specific respect: ACA at least gave
a replica-scoped `EmptyDir` on local ext4 with real POSIX locks under a `min1/max1` always-on
replica. Vercel offers neither the locks nor the always-on.

**The sharp edge: Vercel's 2026 additions make it look far closer to a candidate than it is.**
Dockerfile Functions, Sandbox, Drives — every piece of the requirement now exists *somewhere* on the
platform, and no two of them exist in the same place. The container runs where there is no disk; the
disk mounts where nothing serves traffic; the SQLite lives behind a network API. That near-miss is
exactly the shape that would justify a probe if it were real, which is why it is worth naming why it
is not.

**What Vercel is good for here: nothing this map needs.** Vercel Blob would function as an
`internal/blobbackup` sink, but #92 already ranked that slot across five candidates and chose
Cloudflare R2 on EU jurisdiction; adding a Vercel account to reach a worse-documented S3 alternative
is not an improvement.

### Consequence for #96

**#96 has nothing to probe on Vercel.** The probe it defines — mount the volume, run the
[#20](https://github.com/emepetres/life-ledger/issues/20) `CREATE TABLE`-under-WAL repro from inside
the deployed Linux container, read `/proc/mounts` — has no target here: no volume attaches to a
serving container. The only mountable disk (a Sandbox Drive) is unreachable from the serving path,
so a pass there would prove nothing about a deployment that cannot exist.

**#96's scope is unchanged: Fly.io, Oracle Always Free, Railway, and the mini-PC.**

### Re-verifying this document

```pwsh
# Re-check the load-bearing pages before relying on anything above.
$pages = @(
  'https://vercel.com/docs/functions',
  'https://vercel.com/docs/fluid-compute',
  'https://vercel.com/docs/functions/limitations',
  'https://vercel.com/docs/sandbox/concepts/drives',
  'https://vercel.com/docs/vercel-blob',
  'https://vercel.com/docs/plans/hobby',
  'https://vercel.com/docs/regions'
)
foreach ($page in $pages) {
    $response = Invoke-WebRequest -Uri $page -UseBasicParsing
    "{0} -> {1}" -f $page, $response.StatusCode
}
```

Vercel stamps most docs pages with a visible "Last updated" date; compare it against the dates
recorded per section above. A page that has moved on is the signal to re-read it, not to trust the
summary here — the lesson #91 paid for twice.
