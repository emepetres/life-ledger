package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Backup is a durable sink the store snapshots itself to after every write and
// restores itself from on a cold boot. It is the one seam that makes SQLite on
// an ephemeral EmptyDir volume durable (ADR-0003): the local file is fast and
// POSIX-lockable, the sink is the survivable copy.
//
// Both methods move exactly one file — the store hands Save a consistent,
// sidecar-free snapshot (produced by VACUUM INTO), and Load writes the latest
// snapshot back to destPath.
//
// Load reports (false, nil) when no backup exists yet — the legitimate
// first-ever boot — and a non-nil error only for a genuine failure (e.g. the
// blob store is unreachable). The store treats that distinction as load-bearing:
// a false lets it start a fresh DB, an error aborts the boot rather than risk
// starting empty over a good backup.
type Backup interface {
	// Save persists the snapshot at snapshotPath as the latest durable copy.
	Save(ctx context.Context, snapshotPath string) error
	// Load writes the latest durable snapshot to destPath, reporting whether one
	// existed. (false, nil) means "no backup yet"; a non-nil error means failure.
	Load(ctx context.Context, destPath string) (restored bool, err error)
}

// WithBackup injects the durable sink the store backs up to after each write and
// restores from on boot. It mirrors WithClock: an injected collaborator that
// tests can fake in-process. With no sink the store is a pure local file —
// today's behaviour, unchanged.
func WithBackup(sink Backup) Option {
	return func(s *Store) { s.backup = sink }
}

// bootOrigin names which boot path restorePlan took, for Open to log. The
// values are a load-bearing contract — TestBootLogsOrigin asserts them — so they
// live here as constants rather than as inline string literals.
type bootOrigin string

const (
	originFresh   bootOrigin = "started fresh"
	originRestore bootOrigin = "restored from backup"
	originReuse   bootOrigin = "reused existing local file"
)

// restorePlan runs the boot-time restore decision (ADR-0003), returning which
// path ran for the caller to log. Order matters:
//
//   - a local file already present is authoritative — an in-place restart reuses
//     it and never clobbers newer local data with an older snapshot, so restore
//     is skipped entirely;
//   - otherwise, with a sink configured, Load from the sink. A Load error is
//     fatal: never start empty over a good backup. (false, nil) — no backup yet
//     — proceeds to a fresh DB;
//   - with no sink and no file, it is simply a fresh DB.
//
// It runs before the DB is opened, so the restored file is in place when Open
// then opens and migrates it.
func (s *Store) restorePlan(ctx context.Context, path string) (bootOrigin, error) {
	if pathExists(path) {
		return originReuse, nil
	}
	if s.backup == nil {
		return originFresh, nil
	}
	restored, err := s.backup.Load(ctx, path)
	if err != nil {
		return "", fmt.Errorf("restoring database from backup: %w", err)
	}
	if restored {
		return originRestore, nil
	}
	return originFresh, nil
}

// backupAfterWrite is called at the tail of every successful mutation. When a
// sink is configured it produces a consistent snapshot and saves it
// synchronously before returning, so "write returned" implies "backup durable";
// a Save failure surfaces as the operation's error. With no sink it is a no-op,
// leaving the pure-local-file behaviour untouched.
func (s *Store) backupAfterWrite(ctx context.Context) error {
	if s.backup == nil {
		return nil
	}

	// VACUUM INTO yields a single sidecar-free file at a consistent point — never
	// a raw copy of a live WAL-mode database (ADR-0003). It requires the target
	// not already exist, so snapshot into a fresh temp directory we own and delete.
	dir, err := os.MkdirTemp("", "lifeledger-snapshot-")
	if err != nil {
		return fmt.Errorf("preparing snapshot dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	snapshot := filepath.Join(dir, "snapshot.db")
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, snapshot); err != nil {
		return fmt.Errorf("snapshotting database: %w", err)
	}

	if err := s.backup.Save(ctx, snapshot); err != nil {
		return fmt.Errorf("backing up after write: %w", err)
	}
	return nil
}

// pathExists reports whether anything exists at path. Any Stat error — most
// importantly os.ErrNotExist — is read as "absent"; the boot decision only needs
// to know whether a local DB is already there to reuse.
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
