# Where the offsite backup should live once Azure is gone

Research for [issue #92](https://github.com/emepetres/life-ledger/issues/92), under
[map #90](https://github.com/emepetres/life-ledger/issues/90). **Advisory** input to the durability
decision — this document ranks the candidates with evidence but does **not** make the choice; that will be
recorded in the ADR that supersedes [ADR-0003](../adr/0003-persistence-and-storage.md) and
[ADR-0007](../adr/0007-scheduled-retained-backup.md).

Facts current as of **2026-09-12**; every claim is cited to a primary source (vendor docs, vendor pricing
pages, or the SDK's own module graph resolved locally). **Staleness flags** are marked ⚠ inline: free tiers
and pricing pages are the fastest-moving facts here, AWS's free tier was restructured mid-2025 and is still
settling, and two Hetzner prices render client-side and could not be read from the page itself.

Dependency-tree numbers were **measured**, not recalled: each candidate SDK was resolved into a throwaway
module at today's latest version and the build-relevant module set counted with
`go list -deps ./... | xargs go list -f '{{.Module.Path}}' | sort -u`. Those counts are reproducible facts
about 2026-09-12 module versions, not estimates.

## The workload, stated precisely

One SQLite file of roughly **400 KB**. A handful of writes a day, each currently triggering a synchronous
`VACUUM INTO` snapshot + full-file upload (`store.backupAfterWrite`, ADR-0003). One nightly snapshot copy
from a GitHub Actions cron (`.github/workflows/backup.yml`, ADR-0007). Restore reads are rare — a cold boot
or a runbook restore.

Annualised, that is: **≈ 5–10 PUTs/day (~3k/yr)**, **~1 GET/day at most**, and a stored footprint of
7 dailies + 12 month-ends/year × 400 KB ≈ **8 MB after a year, ~13 MB after five**. Every free tier below
is sized in gigabytes and millions of operations. **The load is not the discriminator — code and auth
complexity are.** Rank accordingly.

Two constraints carry over from the ADRs and must survive:

1. **The `store.Backup` seam is `Save(ctx, snapshotPath) / Load(ctx, destPath)`** — two methods moving one
   file, with `Load` returning `(false, nil)` for "no backup yet" and an error for anything else
   (`internal/store/backup.go`). Any replacement adapter implements exactly that. `internal/blobbackup` is
   ~100 lines; that is the bar.
2. **Retention is daily-7 + monthly-forever** (ADR-0007), with month-ends written to a prefix-disjoint
   `monthly/` path precisely so a prefix-scoped prune can never reach them.

## The dependency-weight measurement

The binary is CGO-free (`modernc.org/sqlite`) and the whole current Azure adapter costs **9 Azure-specific
modules** on top of the existing tree (`azcore`, `azidentity`, `sdk/internal`, `azblob`,
`microsoft-authentication-library-for-go`, `golang-jwt/jwt/v5`, `google/uuid`, `kylelemons/godebug`,
`pkg/browser` — measured from `go list -deps ./internal/blobbackup` in this repo, cross-checked against
`go.mod`). That is the number a replacement should beat or match.

| Client | Build-relevant modules | Notes |
| --- | --- | --- |
| `github.com/pkg/sftp` + `golang.org/x/crypto/ssh` | **4** (`pkg/sftp`, `kr/fs`, `x/crypto`, `x/sys`) | `x/crypto` and `x/sys` are **already in the tree** → **2 genuinely new modules.** The lightest option by a wide margin. |
| `github.com/aws/aws-sdk-go-v2/service/s3` + `/credentials` | **12** | All AWS-owned, no third-party transitive deps at all; `smithy-go` is the only non-`aws-sdk-go-v2` module. Serves **R2, B2, Tigris, S3, and Hetzner Object Storage** from one adapter. |
| `github.com/minio/minio-go/v7` | **19** | Pulls `klauspost/compress`, `klauspost/cpuid`, `tinylib/msgp`, `zeebo/xxh3`, `gopkg.in/ini.v1`, `x/net`, `x/text`… A *lighter-feeling* S3 client that is measurably **heavier** than the AWS SDK for this use. |
| `google.golang.org/api/drive/v3` | **25** | Drags in `google.golang.org/grpc`, `protobuf`, four `go.opentelemetry.io/otel` modules, `s2a-go`, `gax-go`, `cloud.google.com/go/auth`. **The heaviest candidate**, and the only one that adds gRPC and OpenTelemetry to a binary that has neither. |
| `filippo.io/age` (for the encryption question below) | **4** (`filippo.io/age`, `filippo.io/hpke`, `x/crypto`, `x/sys`) | → **2 genuinely new modules.** |

> ⚠ These counts are for the module versions resolvable on 2026-09-12 (`aws-sdk-go-v2/service/s3`
> v1.113.1, `minio-go` v7.3.0, `smithy-go` v1.28.1). Transitive trees shift; re-measure with the command
> above before the decision lands.

**The single most consequential finding for effort:** four of the seven candidates (R2, B2, Tigris, S3 —
and a fifth, Hetzner Object Storage, not on the original list) speak the **same S3 API**. One
`internal/s3backup` adapter, parameterised by endpoint + region + key pair, serves all of them, and the
choice of vendor collapses to a config change rather than a code change. That decouples "which bucket" from
"how much Go do we write", and it means the bucket vendor can be changed later for the cost of two secrets.

## Candidate assessment

### Cloudflare R2

- **Free tier — permanent, and 25× our five-year footprint.** 10 GB-month storage, 1 million Class A
  operations/month, 10 million Class B operations/month, and **egress is free**, with no minimum charge on
  Standard storage. Paid rates beyond that are $0.015/GB-month storage, $4.50/million Class A.
  ([R2 pricing](https://developers.cloudflare.com/r2/pricing/), page states last-updated 2026-08-07;
  checked 2026-09-12.) Our ~3k PUTs/yr is **0.3% of one month's** Class A allowance. Stays free
  indefinitely at this load.
- **Auth from a headless Go process — a static key pair, no expiry.** R2 issues S3-compatible credentials
  as an **Access Key ID** (the token's id) and a **Secret Access Key** (the SHA-256 of the token value).
  Tokens can be scoped to a set of buckets with "Object Read & Write". The docs describe account tokens as
  remaining "valid until manually revoked" and document **no TTL option**.
  ([R2 API tokens](https://developers.cloudflare.com/r2/api/tokens/), checked 2026-09-12.) Two secrets,
  neither of which expires and neither of which needs a refresh flow. This is the simplest auth story of
  any candidate.
- **Go SDK — `aws-sdk-go-v2/service/s3`, 12 modules.** R2 supports `PutObject`, `GetObject`, `HeadObject`,
  `ListObjectsV2`, `DeleteObject` and `CopyObject`. Unsupported: object tagging, ACLs, object lock, AWS-KMS
  SSE (SSE-C works).
  ([R2 S3 API compatibility](https://developers.cloudflare.com/r2/api/s3/api/), checked 2026-09-12.) Every
  operation `store.Backup` and the nightly cron need is in the supported set; nothing in the unsupported
  list is used today.
- **EU pinning — yes, and it is a hard guarantee, not a hint.** R2 distinguishes *location hints* ("a best
  effort and not a guarantee") from **jurisdictional restrictions**, of which `eu` is one, which "ensure
  objects stay within specific legal jurisdictions". An EU bucket is addressed through
  `https://<ACCOUNT_ID>.eu.r2.cloudflarestorage.com`. ⚠ **One-way door:** "Once an R2 bucket is created,
  the jurisdiction cannot be changed" — and recreating a same-named bucket re-applies the original
  jurisdiction.
  ([R2 data location](https://developers.cloudflare.com/r2/reference/data-location/), checked 2026-09-12.)
  Create the bucket in the `eu` jurisdiction from the start or live with the wrong one forever.
- **Retention — expressible natively, and the existing prefix scheme already fits.** R2 lifecycle rules
  support expiration by object age filtered on a `Prefix`, with the documented example being "delete logs
  older than 90 days" via a `Prefix` filter plus `Expiration: Days: 90`. 1000-rule maximum.
  ([R2 object lifecycles](https://developers.cloudflare.com/r2/buckets/object-lifecycles/), checked
  2026-09-12.) So **one rule — `Prefix: daily/`, `Expiration: Days: 7`** — is the whole of ADR-0007's
  prune, and `monthly/` is untouched because no rule names it. The prune step disappears from
  `backup.yml`. Note this trades the current *observable* prune (a step that runs and logs) for an
  invisible platform behaviour — the same coupling/observability objection recorded in
  [blob-lifecycle-retention.md](blob-lifecycle-retention.md) applies, and keeping the explicit prune step
  is a legitimate choice even where the platform could do it.
- **Effort:** small. ~100-line `internal/s3backup` mirroring `blobbackup` almost line-for-line
  (`PutObject`/`GetObject` in place of `UploadFile`/`DownloadFile`, `smithy`'s `NoSuchKey` in place of
  `bloberror.BlobNotFound`), two secrets, an endpoint and `region: "auto"`.

### Backblaze B2

- **Free tier — 10 GB always free, and free API calls.** "First 10GB storage is always free"; egress is
  free up to 3× average monthly storage (then $0.01/GB); "Class A, B, and C API calls are free for
  pay-as-you-go customers", with Class D at $0.004/10,000 after the first 2,500/day. No minimum file size
  or storage-duration fee. ([B2 pricing](https://www.backblaze.com/cloud-storage/pricing), checked
  2026-09-12.) Free indefinitely at this load; the 3× egress rule is irrelevant when egress is a few
  hundred KB.
- **Auth — static application keys, optionally expiring.** The master key has "all capabilities, access to
  all buckets, and has no file prefix restrictions or expiration"; **standard application keys** can be
  scoped to a bucket, to a **file-name prefix**, and to capabilities (`listBuckets`, `readFiles`,
  `writeFiles`, `deleteFiles`). Keys *may* be given an expiry — "a positive integer less than 1000 days" —
  but that is opt-in, so a non-expiring scoped key is available.
  ([B2 application keys](https://www.backblaze.com/docs/cloud-storage-application-keys), checked
  2026-09-12.) Two secrets, no refresh flow — equal to R2, with **finer-grained scoping** (prefix-level) as
  a genuine advantage: a key restricted to `writeFiles` on `daily/` cannot delete a month-end.
- **Go SDK — the same `aws-sdk-go-v2` adapter via the S3-compatible API.** Endpoints take the form
  `https://s3.<region>.backblazeb2.com`. ([B2 data regions](https://www.backblaze.com/docs/cloud-storage-data-regions)
  and the S3-compatible API docs, checked 2026-09-12.) ⚠ The exact region identifier (e.g.
  `eu-central-003`) is account-specific and shown in the bucket's console page — read it there, don't
  hardcode from a doc.
- **EU pinning — yes, via an account-level choice that is irreversible.** Regions are US West, US East,
  **EU Central (Amsterdam, Netherlands)**, and CA East. ⚠ "The choice that you make during account creation
  dictates where all of that account's data is stored. After you create your Backblaze B2 account, you
  cannot change your selected region."
  ([B2 data regions](https://www.backblaze.com/docs/cloud-storage-data-regions), checked 2026-09-12.) The
  one-way door is at **account** level here, not bucket level — a stronger commitment than R2's.
- **Retention — expressible, but with a hide-then-delete twist worth understanding.** Lifecycle rules are
  scoped by `fileNamePrefix`, and `daysFromHidingToDeleting` "causes hidden files that you specify to be
  automatically deleted after a number of days"; `daysFromUploadingToHiding` hides files after a set
  period. Up to 100 rules per bucket, applied once per day.
  ([B2 lifecycle rules](https://www.backblaze.com/docs/cloud-storage-lifecycle-rules), checked
  2026-09-12.) The daily-7 prune is therefore `fileNamePrefix: daily/` with
  `daysFromUploadingToHiding: 7` + a small `daysFromHidingToDeleting`, i.e. a **two-phase** expiry rather
  than R2's single `Days: 7`. Expressible, marginally more to reason about.
- **Effort:** small — identical adapter to R2, different endpoint and key pair.

### Google Drive (the user's own suggestion)

This is the candidate the issue explicitly wanted pinned down, and the answer is clearly negative on three
independent axes.

- **Free tier — 15 GB, but shared with everything else the account does.** "Your account […] is a Personal
  account with up to 15 GB of storage which is shared among Drive, Gmail, and Photos."
  ([Google storage quota](https://support.google.com/drive/answer/6374270), checked 2026-09-12.) The
  quota is ample for 13 MB, but it is **coupled to an unrelated mailbox** — a full Gmail is a backup
  outage. No other candidate has that failure mode.
- **Auth — the worst story of any candidate, and the reason to stop here.** Three separate hazards, all
  from Google's own docs (checked 2026-09-12):
  - **The 7-day refresh token.** "A Google Cloud Platform project with an OAuth consent screen configured
    for an external user type and a publishing status of 'Testing' is issued a refresh token expiring in
    7 days, unless the only OAuth scopes requested are a subset of name, email address, and user profile."
    ([Using OAuth 2.0](https://developers.google.com/identity/protocols/oauth2)) A Drive scope is not in
    that subset. So a Testing-status app's backup **silently dies every week** — which is precisely the
    failure ADR-0007's alarm exists to catch, converted from an exception into a weekly certainty.
  - **Six months of disuse also kills it.** The same page lists "token unused for six months" among the
    reasons a refresh token stops working. A backup that runs nightly is safe from this one, but a
    *restore-only* credential is not.
  - **Escaping the 7-day clock means publishing the app.** Apps using only non-sensitive scopes need no
    full verification — "If your app utilizes only non-sensitive scopes, it is not mandatory for your app
    to complete the app verification process" — but brand-verification still applies for the consent
    screen. ([OAuth app verification](https://support.google.com/cloud/answer/13463073)) `drive.file`
    ("Create new Drive files, or modify existing files, that you open with an app…") *is* non-sensitive;
    `drive` and `drive.readonly` are **restricted** scopes requiring a security assessment.
    ([Drive API scopes](https://developers.google.com/workspace/drive/api/guides/api-specific-auth)) So
    there is a survivable path — `drive.file` only, app published, non-sensitive — but it means running an
    OAuth consent-screen lifecycle, storing a refresh token as a secret, and implementing a refresh flow,
    to back up one 400 KB file.
- **Go SDK — 25 modules, the heaviest by 2×.** `google.golang.org/api/drive/v3` pulls gRPC, protobuf, four
  OpenTelemetry modules, `s2a-go` and the `cloud.google.com/go/auth` stack (measured 2026-09-12). Uploads
  over 5 MB need the resumable protocol; under 5 MB, simple/multipart suffices, so a 400 KB DB is a
  one-request upload at least. ([Drive uploads](https://developers.google.com/workspace/drive/api/guides/manage-uploads),
  checked 2026-09-12.)
- **EU pinning — no.** Consumer Drive offers no data-location control; the issue's own note is correct and
  nothing in the Drive API docs contradicts it.
- **Retention — no native lifecycle rules at all.** Drive has no object-expiry policy engine; the daily-7
  prune stays a manual list-and-delete in the cron, exactly as today, with the added chore of resolving
  file IDs (Drive addresses files by opaque ID, and permits duplicate names in a folder — so the cron must
  also defend against having created two `2026-09-12.db` files).
- **Effort:** the largest of any candidate, for the weakest result. **Recommend against.**

### Hetzner Storage Box

- **Not free — but very cheap, and the SFTP protocol is the real attraction.** BX11 is 1 TB with
  "Unlimited" traffic, 10 snapshots, 100 sub-accounts, no minimum contract term.
  ([Storage Box BX11](https://www.hetzner.com/storage/storage-box/bx11/), checked 2026-09-12.) ⚠ **The
  price renders client-side and could not be read from the page**; a search result put BX11 in the
  **€3.40–4.20/month** range, which is inside map #90's ~€5/month ceiling but must be confirmed in the
  Hetzner console before anyone relies on it.
- **Auth — SSH keys, which is a *better* headless story than it first looks.** Protocols are "SSH port 22
  (only SCP and SFTP, no interactive access), SSH port 23 (interactive access), SFTP, SCP, FTP, FTPS, SMB,
  WebDAV", with both password and SSH-key login (RSA, ECDSA, ED25519 on port 22). Sub-accounts see only
  their own directory while the main user sees everything.
  ([Storage Box docs](https://docs.hetzner.com/storage/storage-box/general/), checked 2026-09-12.) ⚠ One
  real caveat from the same page: **"Password authorization cannot be disabled"** — so the account password
  remains an attack surface regardless of key hygiene. A dedicated sub-account scoped to a backup
  directory contains the blast radius.
- **Go SDK — the lightest by far: 2 genuinely new modules.** `github.com/pkg/sftp` over
  `golang.org/x/crypto/ssh`, and `x/crypto`/`x/sys` are already in `go.mod`. The adapter is an SFTP open +
  `io.Copy` in each direction — arguably *simpler* than the Azure code it replaces, since a missing file is
  a plain `os.IsNotExist`-shaped error rather than a vendor error code.
- **EU pinning — yes, trivially.** Storage Boxes are in "optional Germany or Finland"
  ([Storage Box](https://www.hetzner.com/storage/storage-box/), checked 2026-09-12). Both EU.
- **Retention — no native lifecycle engine.** Snapshots exist (10/20/30/40 manual plus equal automatic
  slots per BX11/BX21/BX31/BX41) and automatic snapshots "can be defined for specific times", but the docs
  do **not** specify retention semantics for them
  ([Storage Box docs](https://docs.hetzner.com/storage/storage-box/general/), checked 2026-09-12) — and
  whole-box snapshots are a different shape from ADR-0007's per-file dated history anyway. The daily-7
  prune stays manual: an SFTP `ReadDir` + date-parse + `Remove` loop, which is honestly about ten lines and
  more *observable* than a lifecycle rule. ⚠ Note the box-level cap of **10 concurrent connections**
  ([Storage Box](https://www.hetzner.com/storage/storage-box/)) — irrelevant at one writer, worth knowing.
- **Effort:** small-to-medium. Smallest dependency cost of any candidate; slightly more code than an S3
  adapter because the prune is hand-rolled; costs real money.
- **Adjacent option worth noting: Hetzner Object Storage.** S3-compatible, in Falkenstein / Nuremberg /
  Helsinki, GDPR-framed, with object lock and versioning
  ([Hetzner Object Storage](https://www.hetzner.com/storage/object-storage/), checked 2026-09-12). ⚠ Its
  price also renders client-side; the page states only that "This base price includes 1 TB of storage (up
  to 744 TB-hours) and 1 TB of egress traffic", and a search result gives **€4.99/month base**. At
  ~€5/month for 13 MB of data it is strictly dominated by R2's free tier — mentioned for completeness, not
  recommended.

### AWS S3 free tier

- ⚠ **This is the candidate whose facts changed most recently, and the free tier as commonly remembered no
  longer exists for new accounts.** The classic offer — "5 GB of Amazon S3 Standard storage, 20,000 Get
  Requests, 2,000 Put Requests, and 100 GB of data transfer out […] each month for one year" — was a
  **12-month introductory** offer, never always-free. As of **July 15, 2025** new customers instead get
  "up to $200 in AWS Free Tier credits", the free plan "is available for 6 months after account creation",
  and "All Free Tier credits must be used within 12 months of your account creation date".
  ([S3 pricing](https://aws.amazon.com/s3/pricing/) and [AWS Free Tier](https://aws.amazon.com/free/), both
  checked 2026-09-12.)
- **And the ending is hostile to a backup sink.** From the Free Tier FAQs: "When your free plan expires,
  AWS closes your account, and you'll lose access to your resources and data", with 90 days to upgrade
  before permanent deletion. ([AWS Free Tier FAQs](https://aws.amazon.com/free/free-tier-faqs/), checked
  2026-09-12.) **An account-closure-on-expiry policy is disqualifying for the one copy of your financial
  history.** On the Paid Plan, S3 simply bills — pennies at 13 MB, but a billing relationship and a card on
  file, which is more moving parts than R2's free tier, not fewer.
- Auth (IAM user access key, or OIDC from Actions), SDK (12 modules, the reference implementation), EU
  pinning (`eu-west-1`/`eu-central-1`), and lifecycle rules (prefix + expiration, the model every other
  vendor copies) are all **best-in-class**. The problem is exclusively the free-tier shape and the
  account-closure clause.
- **Effort:** small — same adapter. **Not recommended** given R2/B2 deliver the same API on a genuinely
  permanent free tier.

### Tigris

- **Free tier — the smallest of the S3 group, still ~380× our five-year footprint.** 5 GB Standard storage,
  10,000 Class A and 100,000 Class B requests, **egress free always**. Paid: $0.02/GB-month Standard,
  $0.005 per 1,000 Class A. ([Tigris pricing](https://www.tigrisdata.com/pricing/), checked 2026-09-12.)
  Our ~3,000 PUTs/yr against 10,000 Class A/month is comfortable, though it is the one free tier where
  ops — not storage — is the binding constraint, so a future chattier backup regime would hit it first.
- **Auth — S3-compatible static access keys.** Same two-secret shape as R2/B2.
- **Go SDK — the same `aws-sdk-go-v2` adapter.** S3-compatible by design.
- **EU pinning — unclear from the pricing page, which is itself a finding.** Tigris is "globally
  distributed" by default with "single-region and multi-region buckets" available, and states "Tigris
  pricing is the same across all geographies", but the pricing page **does not document an EU-pinning
  mechanism**. ⚠ Unresolved: confirm single-region EU placement in the bucket-configuration docs before
  treating EU as satisfied.
- **Retention:** ⚠ **not established.** Tigris advertises storage tiers and a backup/archive use case, but
  lifecycle-rule support was not confirmed against a primary doc in this pass. Assume manual pruning until
  verified.
- **Effort:** small (same adapter), but it is the **least-verified** candidate here, and it is a smaller,
  younger company than Cloudflare or Backblaze — a relevant consideration for a sink meant to hold
  month-ends *forever*.

### Dark horse: GitHub private repo / release assets

Worth taking seriously, because the auth problem vanishes: in Actions, `GITHUB_TOKEN` already exists,
`contents: write` is a standard permission that "allows the action to create a release"
([workflow syntax — permissions](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#permissions),
checked 2026-09-12), and `backup.yml` already uses `github.token` for its alarm issue. **Zero new secrets.**

- **Two distinct shapes, with very different verdicts:**
  - **Committing the DB into a private repo — bad idea.** GitHub warns above 50 MiB and blocks files above
    100 MiB, and "We recommend repositories remain small, ideally less than 1 GB".
    ([About large files](https://docs.github.com/en/repositories/working-with-files/managing-large-files/about-large-files-on-github),
    checked 2026-09-12.) A 400 KB binary blob committed nightly is ~146 MB/year of history that **git
    cannot delta-compress meaningfully** (encrypted or compressed SQLite pages are effectively random), it
    grows without bound because git history is append-only, and "delete the daily older than 7 days" is
    *unexpressible*: removing a file from the working tree does not remove the blob from history, so the
    daily-7 prune becomes a history rewrite. **ADR-0007's retention policy is structurally incompatible
    with git.** Rule this out.
  - **Release assets — actually viable.** "Each file included in a release must be under 2 GiB", "There is
    no limit on the total size of a release, nor bandwidth usage", and release assets **do not count
    against repository storage or bandwidth quotas**.
    ([About releases](https://docs.github.com/en/repositories/releasing-projects-on-github/about-releases),
    checked 2026-09-12.) Upload is `POST /repos/{owner}/{repo}/releases/{release_id}/assets` against
    `uploads.github.com`
    ([Releases REST API](https://docs.github.com/en/rest/releases/assets), checked 2026-09-12). Assets are
    mutable and individually deletable, so a `backups` release holding `daily-<date>.db` assets **can**
    express daily-7 + monthly-forever — via an explicit `gh release delete-asset` prune, no differently
    from today's `az storage blob delete-batch`.
- **But the fatal objection is correlation, not capability.** This puts the backup in **the same account,
  under the same credential, in the same blast radius as the thing being backed up.** A compromised or
  suspended GitHub account loses the code *and* the financial history simultaneously; an Actions
  misconfiguration can delete both. The whole point of an *offsite* backup is that its failure modes are
  uncorrelated with the primary's. Note the current design already fails this test partially (the cron that
  writes the snapshots is itself in GitHub Actions) — but the *storage* being elsewhere is what keeps the
  correlation partial. Collapsing storage into GitHub too removes the last independent axis.
- **EU pinning: no.** No region control for release assets.
- **Verdict:** a legitimate **secondary/redundant** sink — cheap, zero-auth, uncorrelated with the *bucket*
  vendor — and a **poor primary**. If it appeals, run it *alongside* an object store, not instead of one.
- **Also ruled out for completeness:** GitHub Actions **artifacts** cannot serve as the sink at all —
  retention is capped at 90 days, which cannot express monthly-forever.
- **Reachability: Actions only.** The Go binary would need its own PAT or GitHub App credential, forfeiting
  the entire "no new secrets" advantage that motivates this option. This is an Actions-side sink or
  nothing.

## Comparison table

| | **Free at this load?** | **Auth from Go binary** | **New Go modules** | **EU pinning** | **Retention native?** | **Reachable from** |
| --- | --- | --- | --- | --- | --- | --- |
| **Cloudflare R2** | ✅ permanent (10 GB, 1M Class A/mo) | static key pair, **no expiry** | 12 (AWS SDK) | ✅ `eu` jurisdiction (⚠ irreversible per bucket) | ✅ one prefix+age rule | binary **and** Actions |
| **Backblaze B2** | ✅ permanent (10 GB, free A/B/C calls) | static app key, prefix-scopable, optional expiry | 12 (AWS SDK) | ✅ EU Central/Amsterdam (⚠ irreversible per **account**) | ✅ prefix + hide-then-delete | binary **and** Actions |
| **Tigris** | ✅ (5 GB, 10k Class A/mo) | static key pair | 12 (AWS SDK) | ⚠ unconfirmed | ⚠ unconfirmed | binary **and** Actions |
| **AWS S3** | ❌ 6-month plan; **account closed on expiry** | IAM key / OIDC | 12 (AWS SDK) | ✅ | ✅ | binary **and** Actions |
| **Hetzner Storage Box** | ❌ ~€3.40–4.20/mo ⚠ | SSH key (⚠ password auth can't be disabled) | **2** (sftp) | ✅ DE/FI | ❌ manual prune | binary **and** Actions |
| **Hetzner Object Storage** | ❌ ~€4.99/mo ⚠ | static key pair | 12 (AWS SDK) | ✅ DE/FI | ✅ | binary **and** Actions |
| **Google Drive** | ✅ 15 GB — but shared with Gmail/Photos | ⚠ OAuth refresh token; **7-day expiry in Testing**, 6-month disuse expiry | **25** (+gRPC, +OTel) | ❌ none | ❌ none | binary **and** Actions (both painful) |
| **GitHub release assets** | ✅ (assets exempt from quotas) | ⚠ needs a PAT from the binary; free in Actions | 0 (shell out to `gh`) | ❌ | ❌ manual prune | **Actions only**, realistically |
| **GitHub repo commits** | — | — | — | ❌ | ❌ **inexpressible** (git history is append-only) | ruled out |

## Should we encrypt before uploading?

**Yes — and it is nearly free, so the cost/benefit is not close.**

The argument *against* is that every serious candidate encrypts at rest already, and that adding a key
creates a new way to lose the data: a backup you cannot decrypt is not a backup. That second point is real
and is the only thing that needs designing around.

The argument *for* is stronger here for three specific reasons:

1. **The threat model changed the moment the host did.** Under Azure the bucket sat inside a subscription
   the user controlled, reached by managed identity with **no long-lived key in existence**. Every
   replacement above is authenticated by a **static key pair stored as a GitHub secret** (or a refresh
   token, for Drive). That key is now the single thing standing between a third-party bucket and a complete
   personal financial history — transaction-level, dated, categorised. Encryption-before-upload makes a
   leaked bucket credential a *confidentiality non-event*.
2. **The cost is two modules and about fifteen lines.** `filippo.io/age` measures **4 build modules, of
   which `x/crypto` and `x/sys` are already in the tree** — so 2 genuinely new, against the 9 Azure modules
   being removed. It is "a simple, modern and secure file encryption tool, format, and Go library" with
   "small explicit keys […] no config options", supporting X25519 recipients, scrypt passphrases, and SSH
   keys, against a versioned format spec at `age-encryption.org/v1`
   ([FiloSottile/age](https://github.com/FiloSottile/age), checked 2026-09-12). Wrapping the existing
   `VACUUM INTO` snapshot in `age.Encrypt` before `Save` and `age.Decrypt` after `Load` is a change
   confined to the adapter — the `store.Backup` seam does not move.
3. **It makes the dark-horse and the cheap options safe.** An encrypted blob is *fine* as a GitHub release
   asset, and it neutralises the Hetzner "password auth cannot be disabled" caveat and Drive's
   consumer-account exposure. Encryption widens the field of acceptable sinks rather than narrowing it.

**The design constraints that make it safe rather than dangerous:**

- **Use an X25519 recipient, not a passphrase.** Keep the *public* key in the app/CI config (it can encrypt
  but not decrypt) and the *private* key in a password manager plus one offline copy. A leaked CI secret
  then cannot read the backups, which is most of the point.
- ⚠ **The integrity gate in `backup.yml` currently runs `sqlite3 PRAGMA integrity_check` on the downloaded
  file.** On an encrypted blob that check is impossible without decrypting first. This is a **real
  interaction to design for**, not a footnote: either the gate decrypts in the workflow (which puts the
  private key in CI and undoes constraint 1), or the gate moves to the *producer* side — the binary
  verifies the snapshot before encrypting and uploading — with the cron verifying only that the ciphertext
  is well-formed and non-empty. **Recommend the producer-side gate**; it is also where the check belongs
  logically, since that is where a consistent snapshot is created.
- **Test the restore path, not just the backup path.** An encrypted sink makes the restore runbook's
  "prove you can get the data back" step load-bearing rather than ceremonial.

If the encryption-key-management cost is judged too high *right now*, the fallback is not "no encryption"
but "pick a sink whose credentials you can scope tightly and rotate" — which points at B2's prefix-scoped
application keys. But the recommendation stands: **encrypt.**

## Recommendation, ranked for least moving parts

**1. Cloudflare R2 (EU jurisdiction), behind a new `internal/s3backup`, with age encryption.** Fewest
moving parts of anything that is genuinely free forever. Two non-expiring secrets, no refresh flow, no
billing relationship, an SDK whose entire transitive tree is vendor-owned, a hard EU jurisdiction
guarantee, and a retention policy that collapses to one lifecycle rule (or keeps the explicit prune, your
choice). Reachable from **both** the Go binary and Actions, so it does not pre-judge whether
backup-on-write survives the move. The only trap is that the EU jurisdiction is fixed at bucket creation —
get it right the first time.

**2. Backblaze B2 (EU Central), same adapter.** Effectively tied with R2 on effort — the *same code*, a
different endpoint. It wins on credential scoping (prefix-level application keys mean the write key
literally cannot touch `monthly/`) and loses on the account-level region lock and the slightly clumsier
two-phase lifecycle rule. Pick this over R2 if credential blast-radius matters more than bucket-level
region flexibility. **Because the adapter is identical, this is a decision that can be deferred and
reversed cheaply** — which is the strongest reason to build the S3 adapter regardless of which vendor wins.

**3. Hetzner Storage Box, via SFTP.** The one option that *reduces* the dependency tree — 2 new modules
against the 9 being deleted — and the simplest code of all: open, `io.Copy`, close. Loses on cost
(~€3.40–4.20/mo of a ~€5 budget spent on 400 KB) and on having no lifecycle engine, so the prune stays
hand-written. Rises sharply in value **if the chosen host is the home mini-PC**, where a Hetzner box is a
natural neighbour and the "everything in one cloud vendor" objection bites hardest. Reachable from both.

**4. Tigris.** Same adapter, free tier ample, but EU pinning and lifecycle support are **unverified** and
it is the youngest vendor for a "forever" archive. Promote only after closing those two gaps.

**5. GitHub release assets — as a *second* sink, never the only one.** Zero new secrets, quota-exempt,
2 GiB per asset, and a workable explicit prune. But it is correlated with the primary's blast radius and
has no EU story. Its real use is redundancy: a five-line extra step in `backup.yml` that uploads the same
encrypted month-end to a `backups` release gives a genuinely independent second copy for almost nothing.
**Actions-only** in practice.

**6. AWS S3.** Technically excellent, disqualified by the post-2025 free tier: a 6-month plan that **closes
the account** on expiry, or a paid billing relationship. Only sensible if AWS is being adopted for the host
anyway.

**7. Google Drive. Recommend against.** Heaviest SDK (25 modules including gRPC and OpenTelemetry), the
only OAuth-refresh auth model, a 7-day token expiry unless the OAuth app is published, quota shared with
the user's mailbox, no EU control, and no lifecycle rules — so the prune stays manual *and* has to deal
with opaque file IDs. It is the most work for the least capability of any candidate assessed.

### Reachability summary (the flag the map asked for)

- **Work from the Go binary *and* from Actions:** R2, B2, Tigris, S3, Hetzner Storage Box, Hetzner Object
  Storage. All of these keep `store.WithBackup` viable if backup-on-write survives the move, and all of
  them are equally callable from a cron.
- **Actions-only in practice:** GitHub release assets (the binary would need its own PAT, which forfeits
  the only reason to choose it).
- **Technically both, practically neither:** Google Drive — the OAuth refresh-token lifecycle is painful in
  a long-lived binary *and* in a cron.

### What this does not decide

Whether **backup-on-write survives at all**. If the new host has a real persistent disk, the synchronous
per-write upload may become unnecessary and the sink reduces to the nightly Actions cron — in which case
the Go-binary reachability column stops mattering and the field widens (GitHub release assets rise). That
is map #90's open "Durability design, once the host is known" item, and it should be settled **before** the
adapter is written. The S3-shaped recommendation above is deliberately robust to either answer.

## Sources

- R2 pricing (10 GB-month storage, 1M Class A, 10M Class B, free egress, $0.015/GB-month; page dated
  2026-08-07): <https://developers.cloudflare.com/r2/pricing/>
- R2 API tokens (Access Key ID = token id, Secret = SHA-256 of token value; bucket scoping; "valid until
  manually revoked"; no documented TTL): <https://developers.cloudflare.com/r2/api/tokens/>
- R2 data location (location hints are "best effort and not a guarantee"; `eu` jurisdiction; the
  `<ACCOUNT_ID>.eu.r2.cloudflarestorage.com` endpoint; "the jurisdiction cannot be changed"):
  <https://developers.cloudflare.com/r2/reference/data-location/>
- R2 object lifecycles (prefix filter + `Expiration: Days`; "delete logs older than 90 days" example; 1000
  rule maximum): <https://developers.cloudflare.com/r2/buckets/object-lifecycles/>
- R2 S3 API compatibility (PutObject/GetObject/HeadObject/ListObjectsV2/DeleteObject/CopyObject supported;
  tagging, ACLs, object lock, KMS SSE unsupported; SSE-C supported):
  <https://developers.cloudflare.com/r2/api/s3/api/>
- Backblaze B2 pricing ("First 10GB storage is always free"; free Class A/B/C calls; 3× egress rule; no
  minimum file size or duration fee): <https://www.backblaze.com/cloud-storage/pricing>
- B2 application keys (master key has no expiration; standard keys scopable by bucket, file prefix and
  capabilities; optional expiry "less than 1000 days"):
  <https://www.backblaze.com/docs/cloud-storage-application-keys>
- B2 lifecycle rules (`fileNamePrefix`; `daysFromUploadingToHiding`; `daysFromHidingToDeleting`; 100 rules
  per bucket; applied once per day): <https://www.backblaze.com/docs/cloud-storage-lifecycle-rules>
- B2 data regions (US West, US East, EU Central/Amsterdam, CA East; "After you create your Backblaze B2
  account, you cannot change your selected region"): <https://www.backblaze.com/docs/cloud-storage-data-regions>
- Google OAuth 2.0 (Testing-status external apps get "a refresh token expiring in 7 days"; the seven
  reasons a refresh token stops working, incl. six months unused):
  <https://developers.google.com/identity/protocols/oauth2>
- Google OAuth app verification ("If your app utilizes only non-sensitive scopes, it is not mandatory for
  your app to complete the app verification process"; brand-verification for the consent screen):
  <https://support.google.com/cloud/answer/13463073>
- Drive API scopes (`drive.file` is non-sensitive per-file access; `drive`, `drive.readonly`,
  `drive.metadata` are restricted and need a security assessment):
  <https://developers.google.com/workspace/drive/api/guides/api-specific-auth>
- Drive API uploads (simple/multipart ≤5 MB, resumable >5 MB):
  <https://developers.google.com/workspace/drive/api/guides/manage-uploads>
- Google account storage ("up to 15 GB of storage which is shared among Drive, Gmail, and Photos"):
  <https://support.google.com/drive/answer/6374270>
- Hetzner Storage Box (1/5/10/20 TB tiers; unlimited traffic; 10 concurrent connections; SFTP/SCP/rsync/
  WebDAV/Borg/Restic/rclone; "optional Germany or Finland"; no minimum contract):
  <https://www.hetzner.com/storage/storage-box/> and <https://www.hetzner.com/storage/storage-box/bx11/>
- Hetzner Storage Box docs (protocol/port list; SSH key types; "Password authorization cannot be disabled";
  sub-account isolation; 10/20/30/40 manual + automatic snapshot slots, retention unspecified):
  <https://docs.hetzner.com/storage/storage-box/general/>
- Hetzner Object Storage (S3-compatible; FSN1/NBG1/HEL1; base price includes 1 TB storage + 1 TB egress;
  object lock, versioning, SSE): <https://www.hetzner.com/storage/object-storage/>
- AWS S3 pricing (the $200 credits / 6-month free plan / 12-month credit expiry replacing the old offer):
  <https://aws.amazon.com/s3/pricing/>
- AWS Free Tier (Free Plan vs Paid Plan; "30+ AWS services are always free"; account self-closes at
  6 months or credit exhaustion): <https://aws.amazon.com/free/>
- AWS Free Tier FAQs ("When your free plan expires, AWS closes your account, and you'll lose access to your
  resources and data"; 90 days to upgrade): <https://aws.amazon.com/free/free-tier-faqs/>
- Tigris pricing (5 GB storage, 10k Class A, 100k Class B free; $0.02/GB-month Standard; free egress;
  "pricing is the same across all geographies"): <https://www.tigrisdata.com/pricing/>
- GitHub large files (50 MiB warning, 100 MiB block, 25 MiB browser upload, "<1 GB" repo guidance):
  <https://docs.github.com/en/repositories/working-with-files/managing-large-files/about-large-files-on-github>
- GitHub releases ("Each file included in a release must be under 2 GiB"; "There is no limit on the total
  size of a release, nor bandwidth usage"; assets exempt from repo storage/bandwidth quotas):
  <https://docs.github.com/en/repositories/releasing-projects-on-github/about-releases>
- GitHub release assets REST API (`POST /repos/{owner}/{repo}/releases/{release_id}/assets` on
  `uploads.github.com`): <https://docs.github.com/en/rest/releases/assets>
- GitHub Actions workflow syntax — `permissions` (the scope list; `contents: write` "allows the action to
  create a release"):
  <https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#permissions>
- age (Go library `filippo.io/age`; X25519 / scrypt / SSH recipients; "small explicit keys […] no config
  options"; format spec `age-encryption.org/v1`): <https://github.com/FiloSottile/age>
- Dependency-tree counts: measured locally on 2026-09-12 with `go mod tidy` + `go list -deps ./... | xargs
  go list -f '{{.Module.Path}}' | sort -u` against `aws-sdk-go-v2/service/s3` v1.113.1, `minio-go` v7.3.0,
  `google.golang.org/api/drive/v3`, `pkg/sftp`, `filippo.io/age`, and this repo's
  `go list -deps ./internal/blobbackup`.
