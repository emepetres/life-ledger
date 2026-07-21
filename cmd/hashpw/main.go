// Command hashpw generates the bcrypt hash of a password to paste into
// LIFELEDGER_PASSWORD_HASH, so the plaintext password is never stored in config
// (ADR-0004). It reads the password from standard input (one line) and writes
// the hash to standard output — keeping the plaintext off the command line and
// out of shell history.
//
// Usage:
//
//	make hash-password
//	# or: echo -n 'my password' | go run ./cmd/hashpw
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

func main() {
	// Prompt only when attached to a terminal keeps piped input (echo … | hashpw)
	// clean, while still guiding an interactive run.
	fmt.Fprint(os.Stderr, "Enter password: ")

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		fmt.Fprintln(os.Stderr, "no password read from stdin")
		os.Exit(1)
	}
	password := strings.TrimRight(scanner.Text(), "\r\n")
	if password == "" {
		fmt.Fprintln(os.Stderr, "password must not be empty")
		os.Exit(1)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hashing password: %v\n", err)
		os.Exit(1)
	}

	// The hash alone goes to stdout so it can be captured cleanly:
	//   export LIFELEDGER_PASSWORD_HASH="$(make -s hash-password)"
	fmt.Println(string(hash))
}
