package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type runRecord struct {
	At       string `json:"at"`
	Hostname string `json:"hostname"`
	BootID   string `json:"boot_id"`
}

// checkPersistence is the only check that cannot pass on a single run: it
// appends a record and reports the ones already there. Run the probe, restart /
// redeploy / move the machine, run it again — if the earlier records are gone,
// the volume is not persistent no matter how well it locks. A prior record with
// a DIFFERENT boot_id is the proof; a same-boot_id record only shows the file
// survived the last few seconds.
func (r *report) checkPersistence() {
	const name = "prior runs survived on this volume"
	path := filepath.Join(r.dir, runsName)

	var prior []runRecord
	if raw, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if line == "" {
				continue
			}
			var rec runRecord
			if json.Unmarshal([]byte(line), &rec) == nil {
				prior = append(prior, rec)
			}
		}
	} else if !os.IsNotExist(err) {
		r.fail(name, false, "reading %s: %v", runsName, err)
		return
	}

	host, _ := os.Hostname()
	me := runRecord{At: time.Now().UTC().Format(time.RFC3339), Hostname: host, BootID: bootID()}
	line, _ := json.Marshal(me)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		r.fail(name, false, "cannot append to %s: %v", runsName, err)
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()

	if len(prior) == 0 {
		r.fail(name, false, "FIRST RUN — nothing to compare against yet. Restart, redeploy or move the machine, then run the probe again; this is the only check that needs a second run.")
		return
	}

	crossBoot := false
	for _, p := range prior {
		if p.BootID != me.BootID || p.Hostname != me.Hostname {
			crossBoot = true
		}
		r.fact("prior run      %s on %s (boot %s)", p.At, p.Hostname, p.BootID)
	}
	if crossBoot {
		r.pass(name, "%d prior run(s) found, at least one from a different boot or host — the data genuinely survived", len(prior))
	} else {
		r.fail(name, false, "%d prior run(s) found, but all from this same boot (%s) — restart the machine and run again to prove survival", len(prior), me.BootID)
	}
}

func (r *report) print() {
	fmt.Println("================================================================")
	fmt.Println(" SQLite POSIX byte-range lock probe — wayfinder #96 (THROWAWAY)")
	fmt.Println("================================================================")
	fmt.Println()
	for _, f := range r.facts {
		fmt.Println("  " + f)
	}
	fmt.Println()

	if r.aborted != "" {
		fmt.Println("  ABORTED: " + r.aborted)
		fmt.Println()
		fmt.Println("  VERDICT: FAIL — could not even prepare the probe directory.")
		os.Exit(1)
	}

	fatal, soft := 0, 0
	for _, c := range r.checks {
		mark := "FAIL"
		switch {
		case c.ok:
			mark = "PASS"
		case c.fatal:
			fatal++
		default:
			mark = "WARN"
			soft++
		}
		fmt.Printf("  [%s] %s\n         %s\n", mark, c.name, c.detail)
	}

	fmt.Println()
	switch {
	case fatal > 0:
		fmt.Printf("  VERDICT: FAIL — %d disqualifying failure(s). This host cannot hold the SQLite file.\n", fatal)
	case soft > 0:
		fmt.Printf("  VERDICT: PASS so far, with %d check(s) still unproven (see WARN above).\n", soft)
		fmt.Println("           Locking and snapshotting work; re-run after a restart to close out persistence.")
	default:
		fmt.Println("  VERDICT: PASS — genuine POSIX locks, real WAL, a consistent VACUUM INTO, and data that survives a restart.")
	}

	if runtime.GOOS != "linux" {
		fmt.Println()
		fmt.Println("  !! This did not run on Linux. Per ADR-0003 a pass here is the dev-parity false pass. Discard it.")
	}
	if fatal > 0 {
		os.Exit(1)
	}
}
