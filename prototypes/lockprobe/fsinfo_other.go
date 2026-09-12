//go:build !linux

package main

import (
	"errors"
	"os"
)

// Stubs so the probe still compiles — and loudly refuses to mean anything — on
// the Windows dev box. ADR-0003's dev-parity trap lives right here: the SQLite
// checks PASS on Windows against the very SMB share that fails from Linux.

const (
	classLocal     = "local"
	classNetwork   = "network"
	classEphemeral = "ephemeral"
	classForeign   = "foreign"
	classUnknown   = "unknown"
)

var errNotLinux = errors.New("not linux — this probe is only meaningful inside the deployed container")

func classifyFS(string) (string, string, error) { return "", classUnknown, errNotLinux }
func mountFor(string) (string, error)           { return "", errNotLinux }
func takeWriteLock(*os.File) error              { return errNotLinux }
func isLockConflict(error) bool                 { return false }
func bootID() string                            { return "not-linux" }
