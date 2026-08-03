// Command afk-dispatch is the thin entry the AFK activation job shells out to
// for its one routing decision (spec #77). It reads an afk.Input as JSON on
// stdin — the issue's labels, body, parent spec, existing branches, and open
// blockers, all already fetched from the GitHub API by the job — runs the pure
// afk.Decide, and emits the resulting afk.Decision two ways:
//
//   - the full Decision as JSON on stdout, for logs and jq;
//   - flat key=value lines appended to the file named by $GITHUB_OUTPUT (when
//     set), so later workflow steps read them as step outputs.
//
// All the logic lives in internal/afk, which is unit-tested; this wrapper only
// marshals I/O so it can stay trivial.
//
// Usage:
//
//	echo '{"number":79,"title":"…","labels":["wayfinder:task"],"existingBranches":["main"]}' \
//	  | afk-dispatch
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/emepetres/life-ledger/internal/afk"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "afk-dispatch:", err)
		os.Exit(1)
	}
}

func run() error {
	var in afk.Input
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		return fmt.Errorf("decoding Input from stdin: %w", err)
	}

	decision := afk.Decide(in)

	// Full decision to stdout for logs / jq.
	out, err := json.MarshalIndent(decision, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling Decision: %w", err)
	}
	fmt.Println(string(out))

	// Flat outputs for the activation job's later steps, when running in Actions.
	if err := writeGitHubOutput(decision); err != nil {
		return fmt.Errorf("writing GITHUB_OUTPUT: %w", err)
	}
	return nil
}

// writeGitHubOutput appends the decision's fields as step outputs to the file
// named by $GITHUB_OUTPUT. It is a no-op outside GitHub Actions (the variable
// unset), so the command still works for local runs and tests. The reason is a
// single line, so plain key=value is safe.
func writeGitHubOutput(d afk.Decision) error {
	path := os.Getenv("GITHUB_OUTPUT")
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = fmt.Fprintf(f,
		"skill=%s\nbranch=%s\nconcurrency_group=%s\naction=%s\nreason=%s\n",
		d.Skill, d.Branch, d.ConcurrencyGroup, d.Action, d.Reason)
	return err
}
