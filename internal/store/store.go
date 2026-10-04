// Package store is the persistence layer for Life Ledger: a repository over a
// single SQLite file, reached through the pure-Go modernc.org/sqlite driver so
// the app stays a single fully-static binary (ADR-0003). It owns the unified
// startup path — open (creating the file and its parent dir if absent), apply
// the embedded migrations, then serve — so local QA and production boot along
// the identical route with no separate provisioning step.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/pressly/goose/v3"

	_ "modernc.org/sqlite" // register the pure-Go "sqlite" driver
)

// migrationsFS holds the versioned goose migrations, embedded in the binary so
// the schema ships inside the single deployable unit (ADR-0003).
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// DefaultDBPath is where the database lives when LIFELEDGER_DB_PATH is unset. It
// is relative to the working directory so local QA needs zero configuration; the
// parent dir is created on first run.
const DefaultDBPath = "./data/expenses.db"

// Store is a repository over the expense table. It is safe for concurrent use;
// the underlying *sql.DB manages its own connection pool.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Option customises a Store at open time.
type Option func(*Store)

// WithClock injects the clock the store stamps created_at / updated_at with. It
// exists so tests can drive time deterministically and assert that an update
// refreshes updated_at; production leaves it at the default UTC wall clock.
func WithClock(now func() time.Time) Option {
	return func(s *Store) { s.now = now }
}

// bootOrigin names which boot path Open took, for it to log. The values are a
// load-bearing contract — TestBootLogsOrigin asserts them — so they live here as
// constants rather than as inline string literals.
type bootOrigin string

const (
	originCreated bootOrigin = "created new"
	originOpened  bootOrigin = "opened existing"
)

// Open opens the SQLite database at path — creating the file and its parent
// directory if absent — sets the connection pragmas, applies any pending
// embedded migrations, and returns a ready Store. It is the single startup path
// used identically by local QA and production (ADR-0003); the only difference is
// the configured path. The binary has no backup role (ADR-0012): the local file
// is all it knows about. Call Close when done.
func Open(path string, opts ...Option) (*Store, error) {
	s := &Store{now: func() time.Time { return time.Now().UTC() }}
	for _, opt := range opts {
		opt(s)
	}

	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating db directory %q: %w", dir, err)
		}
	}

	origin := originCreated
	if _, err := os.Stat(path); err == nil {
		origin = originOpened
	}

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("opening database %q: %w", path, err)
	}
	s.db = db

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	log.Printf("database ready at %s (%s)", path, origin)
	return s, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

// dsn builds the modernc.org/sqlite connection string, appending the pragmas
// that must hold on every connection the pool opens (ADR-0003):
//   - journal_mode=WAL     — the concurrency/resilience mode the single-writer
//     deployment relies on.
//   - foreign_keys=ON      — off by default in SQLite; the correct default for
//     when a second table arrives.
//   - synchronous=FULL     — fsync on every commit; the local disk is the only
//     copy of the day's writes, so durability is set explicitly rather than left
//     to the WAL-mode default (NORMAL).
//   - busy_timeout=5000    — wait rather than fail immediately on a momentary
//     write lock, keeping WAL robust under the app's light load.
func dsn(path string) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(ON)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Add("_pragma", "busy_timeout(5000)")
	return "file:" + filepath.ToSlash(path) + "?" + q.Encode()
}

// migrate applies all pending embedded migrations. It uses goose's instance
// Provider rather than the package-global API so nothing mutates shared state —
// concurrent Opens stay independent — and the Provider is quiet by default, so a
// normal boot prints nothing while a failure still surfaces as an error.
func migrate(db *sql.DB) error {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("locating migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, sub)
	if err != nil {
		return fmt.Errorf("preparing migrations: %w", err)
	}
	if _, err := provider.Up(context.Background()); err != nil {
		return fmt.Errorf("applying migrations: %w", err)
	}
	return nil
}
