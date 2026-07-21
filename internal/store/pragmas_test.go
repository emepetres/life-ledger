package store

// White-box test: the connection pragmas (ADR-0003) are a configuration concern
// with no other externally observable surface in this slice (there are no
// foreign-key relationships yet to exercise behaviourally), so we assert them
// directly against a pooled connection.

import (
	"context"
	"path/filepath"
	"testing"
)

// AC: WAL and foreign_keys=ON pragmas are set on every connection open.
func TestPragmasSetOnConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "expenses.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()

	var journalMode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("querying journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want %q", journalMode, "wal")
	}

	var foreignKeys int
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("querying foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1 (ON)", foreignKeys)
	}
}
