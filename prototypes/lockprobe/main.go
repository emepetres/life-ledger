// Command lockprobe is a THROWAWAY PROTOTYPE — wayfinder ticket #96.
//
// It answers one question, on one host at a time: does the volume mounted at
// the probe directory give SQLite genuine POSIX byte-range advisory locks, or
// is it a network filesystem in disguise?
//
// ADR-0003 records that Azure Files SMB failed exactly here, and that the same
// repro PASSES from a Windows SMB client — so a pass proves nothing unless it
// was run from inside the deployed Linux container on the real mounted volume.
// Running this on the Windows dev box reproduces that false pass. Don't.
//
// It deliberately uses modernc.org/sqlite with internal/store's exact dsn()
// pragmas: the #20 failure was modernc's pure-Go locking implementation, and
// the C `sqlite3` CLI takes a different code path.
//
// Usage (inside the container/VM, on the mounted volume):
//
//	./lockprobe -dir /data            # run the suite, print the report, exit
//	./lockprobe -dir /data -hold      # ... then stay alive, so the platform
//	                                  # doesn't treat the container as crashed
//
// Run it at least twice, with a restart / redeploy / machine-move in between:
// the persistence section reports what survived from earlier runs.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// dsn mirrors internal/store.dsn() exactly (ADR-0003). If that changes, this
// must change with it — the whole point is to probe the app's real pragmas.
func dsn(path string) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(ON)")
	q.Add("_pragma", "busy_timeout(5000)")
	return "file:" + filepath.ToSlash(path) + "?" + q.Encode()
}

// Exit codes for the self-exec child modes, read by the parent.
const (
	exitChildLockAcquired = 0 // child got the lock — no conflict detected
	exitChildLockDenied   = 3 // child was refused — locks are honoured
	exitChildLockError    = 4 // fcntl itself failed
)

func main() {
	var (
		dir      = flag.String("dir", envOr("LOCKPROBE_DIR", "/data"), "mounted volume directory to probe")
		hold     = flag.Bool("hold", false, "stay alive after reporting (keeps the container running)")
		tryLock  = flag.String("try-lock", "", "internal: child mode, attempt to take the lock and exit with a code")
		writerID = flag.String("writer", "", "internal: child mode, hammer inserts into this db as a second process")
	)
	flag.Parse()

	// Child modes: these are re-execs of this same binary, used to prove that
	// locks conflict ACROSS processes. Same-process POSIX locks never conflict,
	// so a single-process test would pass on a filesystem with no locking at all.
	if *tryLock != "" {
		os.Exit(childTryLock(*tryLock))
	}
	if *writerID != "" {
		os.Exit(childWriter(*writerID))
	}

	r := &report{dir: *dir}
	r.run()
	r.print()

	if *hold {
		fmt.Println("\n-hold set: staying alive. Re-run the probe after a restart to test persistence.")
		select {}
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type check struct {
	name   string
	ok     bool
	fatal  bool // a failure here disqualifies the host
	detail string
}

type report struct {
	dir     string
	facts   []string
	checks  []check
	fsClass string
	aborted string
}

func (r *report) fact(f string, args ...any) {
	r.facts = append(r.facts, fmt.Sprintf(f, args...))
}

func (r *report) pass(name, detail string, args ...any) {
	r.checks = append(r.checks, check{name: name, ok: true, detail: fmt.Sprintf(detail, args...)})
}

func (r *report) fail(name string, fatal bool, detail string, args ...any) {
	r.checks = append(r.checks, check{name: name, fatal: fatal, detail: fmt.Sprintf(detail, args...)})
}

func (r *report) run() {
	// The directory must exist before gatherFacts statfs's it.
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		r.gatherFacts()
		r.aborted = fmt.Sprintf("cannot create probe dir %s: %v", r.dir, err)
		return
	}
	r.gatherFacts()

	r.checkFSClass()
	r.checkRawLocks()
	r.checkSQLite()
	r.checkDurability()
	r.checkPersistence()
}

// checkFSClass judges the volume on what it IS, before any write. This catches
// the failure the lock checks structurally cannot: tmpfs and overlayfs honour
// POSIX locks perfectly and then discard the database on restart, so every
// other check in this probe passes on them. Only the persistence check would
// notice, and only on a second run — by which time a host may already look
// chosen.
func (r *report) checkFSClass() {
	const name = "the volume is a persistent local filesystem"
	switch r.fsClass {
	case classLocal:
		r.pass(name, "local block filesystem — the case ADR-0003 records as known-good")
	case classNetwork:
		r.fail(name, true, "network filesystem — this is the ADR-0003 failure mode; it may accept lock syscalls without enforcing them")
	case classEphemeral:
		r.fail(name, true, "EPHEMERAL filesystem — locks will work and the database will still be lost on restart. Every lock check below passes here; they do not redeem it")
	case classForeign:
		r.fail(name, true, "locks are served by a non-POSIX backend — a PASS below is ADR-0003's dev-parity false pass, not evidence")
	default:
		r.fail(name, false, "could not classify the filesystem — resolve the f_type and mount line by hand before trusting any PASS below")
	}
}

// gatherFacts records what the filesystem claims to be. On a managed host this
// is often the decisive evidence on its own: ext4/xfs on a block device is the
// known-good case, while cifs/nfs/fuse/overlay is the known-bad one.
func (r *report) gatherFacts() {
	host, _ := os.Hostname()
	r.fact("probe run at   %s", time.Now().UTC().Format(time.RFC3339))
	r.fact("hostname       %s", host)
	r.fact("platform       %s/%s (go %s)", runtime.GOOS, runtime.GOARCH, runtime.Version())
	r.fact("probe dir      %s", r.dir)

	if runtime.GOOS != "linux" {
		r.fsClass = classForeign
		r.fact("!! NOT LINUX — ADR-0003's dev-parity trap. This result is worthless; run inside the deployed container.")
		return
	}
	label, class, err := classifyFS(r.dir)
	r.fsClass = class
	if err != nil {
		r.fact("statfs         failed: %v", err)
	} else {
		r.fact("statfs f_type  %s  [%s]", label, class)
	}
	if m, err := mountFor(r.dir); err != nil {
		r.fact("/proc/mounts   lookup failed: %v", err)
	} else {
		r.fact("/proc/mounts   %s", m)
	}
}

// checkRawLocks tests fcntl(F_SETLK) below SQLite entirely: one process holds a
// write lock on a byte range, a second process must be REFUSED. A filesystem
// that grants both is silently lock-free, and SQLite on it will corrupt rather
// than block.
func (r *report) checkRawLocks() {
	const name = "POSIX byte-range locks conflict across processes"
	if runtime.GOOS != "linux" {
		r.fail(name, false, "skipped: not linux")
		return
	}
	path := filepath.Join(r.dir, "lockprobe.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		r.fail(name, true, "cannot open %s: %v", path, err)
		return
	}
	defer f.Close()
	defer os.Remove(path)

	if err := takeWriteLock(f); err != nil {
		r.fail(name, true, "parent could not take F_WRLCK at all: %v (the filesystem refuses byte-range locks)", err)
		return
	}

	self, err := os.Executable()
	if err != nil {
		r.fail(name, false, "cannot locate self to spawn child: %v", err)
		return
	}
	cmd := exec.Command(self, "-try-lock", path)
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
	switch code := cmd.ProcessState.ExitCode(); code {
	case exitChildLockDenied:
		r.pass(name, "second process correctly refused (EAGAIN/EACCES) while the first held the lock")
	case exitChildLockAcquired:
		r.fail(name, true, "second process ACQUIRED a lock the first process already held — locks are not honoured on this filesystem")
	case exitChildLockError:
		r.fail(name, true, "child's fcntl failed outright — the lock syscall does not work here")
	default:
		r.fail(name, false, "child exited %d, inconclusive", code)
	}
}

func childTryLock(path string) int {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return exitChildLockError
	}
	defer f.Close()
	switch err := takeWriteLock(f); {
	case err == nil:
		return exitChildLockAcquired
	case isLockConflict(err):
		return exitChildLockDenied
	default:
		return exitChildLockError
	}
}

const (
	dbName       = "lockprobe.db"
	snapshotName = "lockprobe-snapshot.db"
	runsName     = "lockprobe-runs.jsonl"
	childWrites  = 25
)

// checkSQLite is the #20 repro, generalised: a bare CREATE TABLE under the
// app's real pragmas, then the things internal/store actually depends on —
// WAL surviving, concurrent writers serialising, and VACUUM INTO producing a
// consistent snapshot.
func (r *report) checkSQLite() {
	path := filepath.Join(r.dir, dbName)
	// Start from a clean DB so CREATE TABLE is a real first-write, exactly as
	// goose's version table was in #20.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}

	ctx := context.Background()
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		r.fail("sql.Open with app pragmas", true, "%v", err)
		return
	}
	defer db.Close()

	// #20's exact failing path: a bare CREATE TABLE on a fresh file. On Azure
	// Files SMB this returned SQLITE_BUSY after the full busy_timeout elapsed.
	start := time.Now()
	if _, err := db.ExecContext(ctx, `CREATE TABLE probe (id INTEGER PRIMARY KEY, writer TEXT NOT NULL, n INTEGER NOT NULL)`); err != nil {
		r.fail("CREATE TABLE on the mounted volume (the #20 repro)", true,
			"%v (after %s — a full busy_timeout means the lock never became grantable)", err, time.Since(start).Round(time.Millisecond))
		return
	}
	r.pass("CREATE TABLE on the mounted volume (the #20 repro)", "succeeded in %s", time.Since(start).Round(time.Millisecond))

	// WAL can silently fall back to another journal mode on a filesystem that
	// cannot support shared memory — a pass that hides a downgrade.
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		r.fail("journal_mode is actually WAL", true, "querying journal_mode: %v", err)
	} else if !strings.EqualFold(mode, "wal") {
		r.fail("journal_mode is actually WAL", true, "journal_mode = %q — WAL was requested and silently refused", mode)
	} else {
		r.pass("journal_mode is actually WAL", "wal, and the -wal/-shm sidecars live beside the db")
	}

	// A second OS process writing the same DB. This is what a lock-free
	// filesystem loses: without real locks the writers interleave and the file
	// is left inconsistent rather than one of them waiting on busy_timeout.
	r.checkConcurrentWriters(ctx, db, path)

	// internal/store's backup path is VACUUM INTO, not a raw file copy.
	snapPath := filepath.Join(r.dir, snapshotName)
	_ = os.Remove(snapPath)
	start = time.Now()
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, snapPath); err != nil {
		r.fail("VACUUM INTO produces a snapshot", true, "%v", err)
	} else {
		r.pass("VACUUM INTO produces a snapshot", "wrote %s in %s", snapshotName, time.Since(start).Round(time.Millisecond))
		r.checkSnapshotConsistent(ctx, db, snapPath)
	}

	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		r.fail("PRAGMA integrity_check on the live db", true, "%v", err)
	} else if integrity != "ok" {
		r.fail("PRAGMA integrity_check on the live db", true, "%s", integrity)
	} else {
		r.pass("PRAGMA integrity_check on the live db", "ok")
	}
}

func (r *report) checkConcurrentWriters(ctx context.Context, db *sql.DB, path string) {
	const name = "two OS processes can write concurrently without corruption"
	self, err := os.Executable()
	if err != nil {
		r.fail(name, false, "cannot locate self to spawn child: %v", err)
		return
	}
	child := exec.Command(self, "-writer", "child", "-dir", r.dir)
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		r.fail(name, false, "cannot start child writer: %v", err)
		return
	}

	var parentErr error
	for i := range childWrites {
		if _, err := db.ExecContext(ctx, `INSERT INTO probe (writer, n) VALUES (?, ?)`, "parent", i); err != nil {
			parentErr = err
			break
		}
	}
	childErr := child.Wait()

	switch {
	case parentErr != nil:
		r.fail(name, true, "parent insert failed: %v", parentErr)
		return
	case childErr != nil:
		r.fail(name, true, "child writer failed: %v (busy_timeout did not rescue it)", childErr)
		return
	}

	var parentRows, childRows int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM probe WHERE writer = 'parent'`).Scan(&parentRows)
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM probe WHERE writer = 'child'`).Scan(&childRows)
	if parentRows != childWrites || childRows != childWrites {
		r.fail(name, true, "expected %d rows from each writer, got parent=%d child=%d — writes were lost", childWrites, parentRows, childRows)
		return
	}
	r.pass(name, "%d rows from each of two processes, all committed", childWrites)
}

// checkSnapshotConsistent reopens the VACUUM INTO output as a database of its
// own: a snapshot that cannot be opened, or that disagrees with the source on
// row count, is not a backup.
func (r *report) checkSnapshotConsistent(ctx context.Context, src *sql.DB, snapPath string) {
	const name = "the VACUUM INTO snapshot is readable and matches the source"
	snap, err := sql.Open("sqlite", dsn(snapPath))
	if err != nil {
		r.fail(name, true, "reopening snapshot: %v", err)
		return
	}
	defer snap.Close()

	var integrity string
	if err := snap.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		r.fail(name, true, "integrity_check on snapshot: %v", err)
		return
	}
	if integrity != "ok" {
		r.fail(name, true, "snapshot integrity_check = %s", integrity)
		return
	}

	var srcRows, snapRows int
	if err := src.QueryRowContext(ctx, `SELECT COUNT(*) FROM probe`).Scan(&srcRows); err != nil {
		r.fail(name, true, "counting source rows: %v", err)
		return
	}
	if err := snap.QueryRowContext(ctx, `SELECT COUNT(*) FROM probe`).Scan(&snapRows); err != nil {
		r.fail(name, true, "counting snapshot rows: %v", err)
		return
	}
	if srcRows != snapRows {
		r.fail(name, true, "snapshot has %d rows, source has %d", snapRows, srcRows)
		return
	}
	r.pass(name, "integrity ok, %d rows in both", snapRows)
}

func childWriter(id string) int {
	dir := flag.Lookup("dir").Value.String()
	db, err := sql.Open("sqlite", dsn(filepath.Join(dir, dbName)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "child writer open: %v\n", err)
		return 1
	}
	defer db.Close()
	for i := range childWrites {
		if _, err := db.Exec(`INSERT INTO probe (writer, n) VALUES (?, ?)`, id, i); err != nil {
			fmt.Fprintf(os.Stderr, "child writer insert %d: %v\n", i, err)
			return 1
		}
	}
	return 0
}
