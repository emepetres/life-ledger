# lockprobe — THROWAWAY prototype for [#96](https://github.com/emepetres/life-ledger/issues/96)

Answers one question per host: **does the attached volume give SQLite genuine
POSIX byte-range advisory locks, or is it a network filesystem in disguise?**

This is the one fact that could disqualify a front-runner *after* cutover
instead of before. [ADR-0003](../../docs/adr/0003-persistence-and-storage.md)
records that Azure Files SMB failed exactly here.

> ⚠️ **The dev-parity trap.** The same repro **passes** from a Windows SMB
> client and fails only from a Linux CIFS mount. **Run this inside the deployed
> Linux container, on the real mounted volume.** A run on the Windows dev box —
> or against a local Docker volume on your own machine — reproduces exactly the
> false pass that made SMB look survivable last time.

## What it checks

| Check | Why it is here |
| --- | --- |
| **Filesystem class** (statfs `f_type` + `/proc/mounts`) | The only check that catches a volume which locks perfectly and then *throws the data away*. |
| **Cross-process `fcntl(F_SETLK)`** | SQLite's actual primitive, tested below SQLite. Two OS processes — same-process POSIX locks never conflict, so a single-process test passes on a filesystem with no locking at all. |
| **`CREATE TABLE` on a fresh db** | The literal [#20](https://github.com/emepetres/life-ledger/issues/20) repro, under `internal/store`'s exact `dsn()` pragmas. |
| **`journal_mode` is really `wal`** | WAL can be silently refused and fall back — a pass that hides a downgrade. |
| **Two OS processes writing concurrently** | Proves locks *serialise* rather than interleave and lose writes. |
| **`VACUUM INTO` + reopen + row-count match** | `internal/store`'s backup path is `VACUUM INTO`, not a file copy. |
| **Persistence across runs** | Appends a boot-stamped record. Needs a **second run after a restart** — the only check one run cannot settle. |

It deliberately uses **`modernc.org/sqlite`**, not the `sqlite3` CLI: the #20
failure was modernc's pure-Go locking implementation, and the C library takes a
different code path.

## Validated against known cases

Run in WSL before shipping it to any host, to prove it detects bad volumes and
not merely good ones:

| Volume | statfs | Lock checks | Verdict |
| --- | --- | --- | --- |
| ext4 | `0xef53` local | all pass | **PASS** (incl. cross-boot persistence) |
| `/dev/shm` tmpfs | `0x1021994` ephemeral | **all pass** | **FAIL** — ephemeral |
| `/mnt/c` 9p drvfs | `0x1021997` foreign | **all pass** | **FAIL** — non-POSIX lock backend |

The last two matter most: every lock-level check passes and the verdict is still
FAIL. `/mnt/c` is the dev-parity trap reproduced — a Windows-backed filesystem
sailing through every SQLite check.

## Build

```pwsh
foreach ($arch in @("amd64", "arm64")) {
  $Env:GOOS = "linux"; $Env:GOARCH = $arch; $Env:CGO_ENABLED = "0"
  go build -trimpath -ldflags="-s -w" -o "prototypes/lockprobe/dist/lockprobe-linux-$arch" ./prototypes/lockprobe/
}
```

`arm64` is for Oracle Always Free (Ampere A1); everything else on the shortlist
is `amd64`, including the mini-PC's N95.

## Reading the output

- **`VERDICT: PASS`** — locks, WAL, `VACUUM INTO` and cross-restart persistence
  all confirmed. Only reachable on a second run.
- **`VERDICT: PASS so far`** — everything works, persistence not yet proven.
  Restart the machine and run again.
- **`VERDICT: FAIL`** — at least one disqualifying failure. **Strike the host.**

Capture the *whole* report per host, including the facts block: the
`/proc/mounts` line and `f_type` are the raw evidence #96 asks for.

## Per-host runbook

Each host needs **two runs with a restart in between**. The first run reports
`FIRST RUN` on persistence; the second is what proves the volume survived.

### Fly.io

```pwsh
fly launch --no-deploy --name lockprobe-ll --region ams
fly volumes create probedata --region ams --size 1 --yes
```

Add to `fly.toml`, and disable autostop so the machine does not vanish:

```toml
[[mounts]]
  source      = "probedata"
  destination = "/data"

[[services]]
  auto_stop_machines  = false
  auto_start_machines = false
```

```pwsh
fly deploy --dockerfile prototypes/lockprobe/Dockerfile
fly logs                      # first report
fly machine restart <id>
fly logs                      # second report — persistence
fly apps destroy lockprobe-ll ; fly volumes destroy <vol-id>
```

Fly is the only shortlisted managed host documenting its volume as a local NVMe
slice on the machine's own physical host, so it is expected to pass — the run
confirms it rather than discovering it.

### Railway

Storage medium is **undocumented**, making Railway the candidate most likely to
fail. Deploy the same Dockerfile, attach a volume mounted at `/data` in the
service settings, then read the deploy logs; redeploy to get the second run.

### Oracle Cloud Always Free

A real VM, so no container needed — but it is **ARM**, so ship the `arm64`
binary. Attach a Block Volume, format it `ext4`, mount at `/data`:

```pwsh
scp prototypes/lockprobe/dist/lockprobe-linux-arm64 "ubuntu@<ip>:~/lockprobe"
ssh ubuntu@<ip> "chmod +x ~/lockprobe && sudo ~/lockprobe -dir /data"
ssh ubuntu@<ip> "sudo reboot"
ssh ubuntu@<ip> "sudo ~/lockprobe -dir /data"   # second run
```

Also probe the **boot volume** (`-dir /home/ubuntu/probe`) — if that is already
a real block device, the separate Block Volume may be unnecessary.

### Home mini-PC (Proxmox)

`amd64`. Same two-run shape, with the reboot being the real value here: per
[#94](https://github.com/emepetres/life-ledger/issues/94) this box takes an
**unclean power loss 1–2×/yr**, so run the second pass after a hard power-cut of
the VM (Proxmox *Stop*, not *Shutdown*) rather than a clean reboot. That is the
failure mode the host actually has.

### Vercel — nothing to probe

[#97](https://github.com/emepetres/life-ledger/issues/97) disqualified it. Vercel
runs Dockerfile Functions now, but they are **stateless** with a 5-minute idle
scale-down, and the one real persistent disk it offers — **Sandbox Drives** —
mounts to a Sandbox, never to a Function. There is no volume here to mount.

## When done

Fold the verdicts into #96 and delete the branch. Nothing here belongs on
`main`; the app does not ship a probe.
