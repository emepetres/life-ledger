package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The durability probe answers a question the lock checks structurally cannot:
// when the machine loses power mid-flight, does a WAL-mode SQLite database come
// back CONSISTENT, with every transaction that already returned still present?
//
// checkPersistence only proves the filesystem kept an append-only text file.
// That is a weaker claim: a disk that lies about flushes will preserve a lazily
// written text file and still lose committed SQLite transactions.
//
// The method: keep a database across runs, never wiping it. Each run verifies
// what is there, appends rows, then records the post-commit row count to a
// separate ledger file — written AFTER the commit returned. On the next run,
// fewer rows in the database than the ledger claims means the commit lied.
//
//	actual < expected  -> a transaction that RETURNED was lost. Fatal.
//	actual >= expected -> sound (a cut between commit and ledger write is benign).
//
// Two databases run in parallel under different durability pragmas, so the
// result says not just whether the host is safe but which pragma it needs —
// the open question in map #90's durability fog.
const (
	durableRowsPerRun = 10
	durableLedger     = "lockprobe-durable.jsonl"
)

type durableVariant struct {
	file      string
	label     string
	pragmas   []string
	rationale string
}

var durableVariants = []durableVariant{
	{
		file:      "lockprobe-durable-normal.db",
		label:     "synchronous=NORMAL (the app's current pragmas)",
		pragmas:   nil,
		rationale: "WAL's default. Fast, and documented to risk losing recent commits on power loss",
	},
	{
		file:      "lockprobe-durable-full.db",
		label:     "synchronous=FULL",
		pragmas:   []string{"synchronous(FULL)"},
		rationale: "fsyncs the WAL on every commit — the candidate fix if NORMAL loses rows here",
	},
}

type durableLedgerEntry struct {
	Variant  string `json:"variant"`
	Expected int    `json:"expected"`
	Boot     string `json:"boot_id"`
	At       string `json:"at"`
}

// durableDSN extends the app's real dsn() rather than replacing it, so the only
// difference between variants is the durability pragma under test.
func durableDSN(path string, extra []string) string {
	base := dsn(path)
	if len(extra) == 0 {
		return base
	}
	q := url.Values{}
	for _, p := range extra {
		q.Add("_pragma", p)
	}
	return base + "&" + q.Encode()
}

func (r *report) checkDurability() {
	expected, corrupt := r.readDurableLedger()
	r.checkLedgerIntact(corrupt)
	for _, v := range durableVariants {
		r.checkDurableVariant(v, expected[v.file])
	}
}

// checkLedgerIntact reports damage to the probe's own append-only ledgers. It
// earns its place because this is not hypothetical: on an LXC guest backed by an
// LVM thin volume, an unclean power cut zeroed a ~10-minute-old append while
// every committed SQLite transaction in the same directory survived.
//
// ext4 with delayed allocation journals the new file length but not the data
// blocks, so recovery reads the tail back as NULs. A parser that skips
// unreadable lines therefore loses data SILENTLY — which is how this probe once
// reported a destroyed ledger as a cheerful "baseline run".
//
// The finding matters beyond the probe: on such a host, anything the app writes
// without fsync is not durable. SQLite is safe because it fsyncs; a hand-rolled
// snapshot or marker file is not.
func (r *report) checkLedgerIntact(corrupt int) {
	const name = "the probe's own append-only ledgers survived intact"
	if corrupt == 0 {
		r.pass(name, "no NUL-padded or unparseable records")
		return
	}
	r.fail(name, false, "%d unreadable record(s) — NUL-padded or truncated by an unclean shutdown. Committed SQLite data may still be intact (it fsyncs; these appends do not), but on this host ANY un-fsynced write is lossy. Inspect with `cat -A`", corrupt)
}

// readDurableLedger returns the last recorded post-commit row count per variant,
// and how many records were unreadable. That second value is the whole point: an
// unreadable ledger must be reported, never silently treated as an absent one.
func (r *report) readDurableLedger() (map[string]int, int) {
	out := map[string]int{}
	raw, err := os.ReadFile(filepath.Join(r.dir, durableLedger))
	if err != nil {
		return out, 0
	}
	corrupt := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var e durableLedgerEntry
		if json.Unmarshal([]byte(line), &e) != nil {
			corrupt++
			continue
		}
		out[e.Variant] = e.Expected
	}
	return out, corrupt
}

func (r *report) appendDurableLedger(e durableLedgerEntry) {
	f, err := os.OpenFile(filepath.Join(r.dir, durableLedger), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	line, _ := json.Marshal(e)
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

func (r *report) checkDurableVariant(v durableVariant, expected int) {
	name := "committed transactions survive power loss — " + v.label
	path := filepath.Join(r.dir, v.file)
	_, statErr := os.Stat(path)
	preexisting := statErr == nil

	ctx := context.Background()
	db, err := sql.Open("sqlite", durableDSN(path, v.pragmas))
	if err != nil {
		r.fail(name, true, "opening %s: %v", v.file, err)
		return
	}
	defer db.Close()

	// Opening a WAL database left behind by a power cut is itself part of the
	// test: SQLite replays the WAL here, and a torn WAL surfaces as an error.
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS durable (id INTEGER PRIMARY KEY, boot TEXT NOT NULL)`); err != nil {
		r.fail(name, true, "%s did not survive reopen: %v (WAL replay failed — the database is damaged)", v.file, err)
		return
	}

	if preexisting {
		var integrity string
		if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
			r.fail(name, true, "integrity_check on the carried-over %s: %v", v.file, err)
			return
		}
		if integrity != "ok" {
			r.fail(name, true, "carried-over %s is CORRUPT: %s", v.file, integrity)
			return
		}
	}

	var actual int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM durable`).Scan(&actual); err != nil {
		r.fail(name, true, "counting rows in %s: %v", v.file, err)
		return
	}

	switch {
	// A vanished database is the worst case, not a fresh start: the ledger is
	// proof that commits were acknowledged into a file that is no longer here.
	case !preexisting && expected > 0:
		r.fail(name, true, "%s VANISHED — the ledger records %d acknowledged commits, and the file is gone. The volume did not keep the database", v.file, expected)
		return
	// A surviving database with no ledger record is NOT a fresh start: the
	// ledger was destroyed while the database lived. Saying "baseline" here
	// would hide a real loss behind a benign message.
	case preexisting && expected == 0:
		r.fail(name, false, "CANNOT VERIFY: %s exists with %d rows, but the ledger holds no record for it — the ledger was lost or NUL-padded by an unclean shutdown. Re-baselining; the database itself may be perfectly intact", v.file, actual)
	case !preexisting:
		r.fail(name, false, "baseline run — %s created, no prior commit to verify. Cut the power and run again; %s", v.file, v.rationale)
	case actual < expected:
		r.fail(name, true, "LOST COMMITTED DATA: %d rows present, %d were committed and acknowledged before the cut (%d lost). %s",
			actual, expected, expected-actual, v.rationale)
	case actual > expected:
		r.pass(name, "%d rows, ledger expected %d — the cut landed between commit and ledger write, which is benign. Nothing acknowledged was lost", actual, expected)
	default:
		r.pass(name, "all %d acknowledged commits survived the restart intact, integrity ok", actual)
	}

	for i := 0; i < durableRowsPerRun; i++ {
		if _, err := db.ExecContext(ctx, `INSERT INTO durable (boot) VALUES (?)`, bootID()); err != nil {
			r.fail(name, true, "appending row %d to %s: %v", i, v.file, err)
			return
		}
	}

	var total int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM durable`).Scan(&total); err != nil {
		r.fail(name, false, "recounting %s after append: %v", v.file, err)
		return
	}
	// Written only after the commits returned, so the ledger is a record of what
	// SQLite promised — the claim the next run holds it to.
	r.appendDurableLedger(durableLedgerEntry{
		Variant:  v.file,
		Expected: total,
		Boot:     bootID(),
		At:       time.Now().UTC().Format(time.RFC3339),
	})
}
