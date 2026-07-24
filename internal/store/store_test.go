package store_test

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/emepetres/life-ledger/internal/expense"
	"github.com/emepetres/life-ledger/internal/store"
)

// baseTime is a fixed UTC instant (whole seconds) so timestamp round-trips are
// exact through the store's second-precision ISO-8601 serialisation.
var baseTime = time.Date(2026, 7, 21, 10, 30, 0, 0, time.UTC)

// fakeClock is a controllable clock so tests can stamp created_at / updated_at
// deterministically and prove an update advances updated_at.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// openTemp opens a store at a fresh temp LIFELEDGER_DB_PATH under a not-yet-
// existing parent directory, so every test exercises the self-creating,
// migrate-on-open startup path in isolation.
func openTemp(t *testing.T, opts ...store.Option) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data", "expenses.db")
	s, err := store.Open(path, opts...)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func ptr(s string) *string { return &s }

// fakeSink is an in-process Backup, the durability analog of the fake clock: it
// holds the latest snapshot bytes in memory so a store opened at a fresh path
// can be restored from it without any Azure. saveErr / loadErr stub genuine
// failures; a nil data field is the "no backup yet" state Load reports as
// (false, nil).
type fakeSink struct {
	mu      sync.Mutex
	data    []byte // latest snapshot, or nil when nothing saved yet
	saves   int
	saveErr error
	loadErr error
}

func (f *fakeSink) Save(_ context.Context, snapshotPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	b, err := os.ReadFile(snapshotPath)
	if err != nil {
		return err
	}
	f.data = b
	f.saves++
	return nil
}

func (f *fakeSink) Load(_ context.Context, destPath string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loadErr != nil {
		return false, f.loadErr
	}
	if f.data == nil {
		return false, nil
	}
	if err := os.WriteFile(destPath, f.data, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// freshDBPath returns a not-yet-existing db path under its own temp dir, so each
// call is an independent empty EmptyDir-like volume.
func freshDBPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "data", "expenses.db")
}

// AC: Backup-on-write — after Create through a sink, opening a *new* store at a
// fresh empty path with the *same* sink restores the expense. Also covers
// snapshot consistency (N writes → exactly N rows) and that the restored DB is
// at the current schema (a further Create on it succeeds).
func TestBackupOnWriteRestoresAtFreshPath(t *testing.T) {
	sink := &fakeSink{}
	ctx := context.Background()

	first, err := store.Open(freshDBPath(t), store.WithBackup(sink))
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	descs := []string{"coffee", "parking", "lunch"}
	for _, d := range descs {
		if err := first.Create(ctx, &expense.Expense{Date: baseTime, Amount: 100, Description: d, RawText: "1 " + d}); err != nil {
			t.Fatalf("Create %s: %v", d, err)
		}
	}
	_ = first.Close()

	// A brand-new store at a different empty path, same sink: it must rebuild
	// itself from the latest snapshot.
	second, err := store.Open(freshDBPath(t), store.WithBackup(sink))
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	got, err := second.List(ctx)
	if err != nil {
		t.Fatalf("List after restore: %v", err)
	}
	if len(got) != len(descs) {
		t.Fatalf("restored List = %d rows, want %d (snapshot consistency)", len(got), len(descs))
	}

	// Schema is current after restore: a write on the restored DB succeeds.
	if err := second.Create(ctx, &expense.Expense{Date: baseTime, Amount: 200, Description: "post-restore", RawText: "2 post-restore"}); err != nil {
		t.Fatalf("Create on restored store: %v", err)
	}
}

// AC: Restore only when local absent — a boot with an existing local file
// reuses it and never restores. Proven by pointing the second boot at a sink
// whose Load would fail: because the file already exists, Load must not be
// called, so Open succeeds and the local data is intact.
func TestExistingLocalFileReusedNotRestored(t *testing.T) {
	path := freshDBPath(t)
	ctx := context.Background()

	first, err := store.Open(path, store.WithBackup(&fakeSink{}))
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	if err := first.Create(ctx, &expense.Expense{Date: baseTime, Amount: 100, Description: "keep", RawText: "1 keep"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = first.Close()

	poison := &fakeSink{loadErr: errors.New("Load must not be called when a local file exists")}
	second, err := store.Open(path, store.WithBackup(poison))
	if err != nil {
		t.Fatalf("reopen over existing local file: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	got, err := second.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Description != "keep" {
		t.Fatalf("reused local file lost data: got %d rows %+v", len(got), got)
	}
}

// AC: First-ever boot — no local file, empty sink — starts a clean empty DB
// with no error and no restore.
func TestFirstBootEmptySinkStartsFresh(t *testing.T) {
	sink := &fakeSink{}
	s := openTemp(t, store.WithBackup(sink))

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List on fresh DB with empty sink: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("fresh DB List = %d rows, want 0", len(got))
	}
}

// AC: A sink stubbed to fail on Save makes the write operation return an error —
// "write returned" must imply "backup durable".
func TestSaveFailureFailsWrite(t *testing.T) {
	sink := &fakeSink{saveErr: errors.New("blob unreachable")}
	s := openTemp(t, store.WithBackup(sink))
	ctx := context.Background()

	err := s.Create(ctx, &expense.Expense{Date: baseTime, Amount: 100, Description: "x", RawText: "1 x"})
	if err == nil {
		t.Fatal("Create returned nil despite Save failure")
	}
	if !errors.Is(err, sink.saveErr) {
		t.Errorf("Create err = %v, want it to wrap %v", err, sink.saveErr)
	}
}

// AC: A Load error on boot is fatal — never start empty over a good backup.
func TestLoadErrorOnBootIsFatal(t *testing.T) {
	sink := &fakeSink{loadErr: errors.New("blob down")}

	_, err := store.Open(freshDBPath(t), store.WithBackup(sink))
	if err == nil {
		t.Fatal("Open returned nil despite Load failure; must abort rather than start empty")
	}
	if !errors.Is(err, sink.loadErr) {
		t.Errorf("Open err = %v, want it to wrap %v", err, sink.loadErr)
	}
}

// AC: Boot logs which path ran — restored / started fresh / reused local.
func TestBootLogsOrigin(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	ctx := context.Background()

	assertLogged := func(want string) {
		t.Helper()
		if !bytes.Contains(buf.Bytes(), []byte(want)) {
			t.Errorf("boot log = %q, want it to contain %q", buf.String(), want)
		}
	}

	// Fresh, no sink.
	buf.Reset()
	sFresh, err := store.Open(freshDBPath(t))
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	_ = sFresh.Close()
	assertLogged("started fresh")

	// Restored from backup.
	sink := &fakeSink{}
	seed, err := store.Open(freshDBPath(t), store.WithBackup(sink))
	if err != nil {
		t.Fatalf("open seed: %v", err)
	}
	if err := seed.Create(ctx, &expense.Expense{Date: baseTime, Amount: 100, Description: "seed", RawText: "1 seed"}); err != nil {
		t.Fatalf("Create seed: %v", err)
	}
	_ = seed.Close()

	buf.Reset()
	sRestored, err := store.Open(freshDBPath(t), store.WithBackup(sink))
	if err != nil {
		t.Fatalf("open restored: %v", err)
	}
	_ = sRestored.Close()
	assertLogged("restored from backup")

	// Reused existing local file.
	path := freshDBPath(t)
	sInit, err := store.Open(path)
	if err != nil {
		t.Fatalf("open init: %v", err)
	}
	_ = sInit.Close()

	buf.Reset()
	sReuse, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = sReuse.Close()
	assertLogged("reused existing local file")
}

// AC: Startup opens DB at the path, creating file + parent dir if absent, and
// embedded migrations apply automatically (the expense table is queryable).
func TestOpenCreatesFileParentDirAndSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "sub", "expenses.db")

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if _, err := os.Stat(path); err != nil {
		t.Errorf("db file not created at %q: %v", path, err)
	}

	// The expense table exists only if migrations ran; an empty list proves it.
	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List on fresh DB: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("fresh DB List = %d rows, want 0", len(got))
	}
}

// AC: Repository create + get round-trips; Create assigns an id and timestamps.
func TestCreateAssignsIdentityAndRoundTrips(t *testing.T) {
	clk := &fakeClock{t: baseTime}
	s := openTemp(t, store.WithClock(clk.now))
	ctx := context.Background()

	e := &expense.Expense{
		Date:        time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
		Amount:      1250,
		Description: "parking",
		Split:       true,
		Account:     ptr("work"),
		RawText:     "9.95 parking @work *",
	}
	if err := s.Create(ctx, e); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if e.ID == 0 {
		t.Error("Create did not assign an ID")
	}
	if !e.CreatedAt.Equal(baseTime) || !e.UpdatedAt.Equal(baseTime) {
		t.Errorf("Create stamps = created %v / updated %v, want both %v", e.CreatedAt, e.UpdatedAt, baseTime)
	}

	got, err := s.Get(ctx, e.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Date.Equal(e.Date) {
		t.Errorf("Date = %v, want %v", got.Date, e.Date)
	}
	if got.Amount != 1250 {
		t.Errorf("Amount = %d, want 1250", got.Amount)
	}
	if got.Description != "parking" {
		t.Errorf("Description = %q, want %q", got.Description, "parking")
	}
	if !got.Split {
		t.Error("Split = false, want true")
	}
	if got.Account == nil || *got.Account != "work" {
		t.Errorf("Account = %v, want \"work\"", got.Account)
	}
	if got.RawText != e.RawText {
		t.Errorf("RawText = %q, want %q", got.RawText, e.RawText)
	}
	if !got.CreatedAt.Equal(baseTime) || !got.UpdatedAt.Equal(baseTime) {
		t.Errorf("timestamps = created %v / updated %v, want both %v", got.CreatedAt, got.UpdatedAt, baseTime)
	}
}

// AC: account persists as NULL when omitted (a nil pointer, not an empty
// string); the full amount is stored regardless of split.
func TestOmittedAccountPersistsAsNull(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	e := &expense.Expense{
		Date:        baseTime,
		Amount:      4999,
		Description: "groceries",
		Split:       true, // full amount must still be stored
		Account:     nil,  // omitted
		RawText:     "49.99 groceries *",
	}
	if err := s.Create(ctx, e); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Get(ctx, e.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Account != nil {
		t.Errorf("Account = %q, want NULL (nil pointer)", *got.Account)
	}
	if got.Amount != 4999 {
		t.Errorf("Amount = %d, want 4999 (full amount stored despite split)", got.Amount)
	}
	if !got.Split {
		t.Error("Split = false, want true")
	}
}

// AC: list is newest-first — date descending, then id descending within a day.
func TestListNewestFirst(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	day1 := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)

	// Insertion order deliberately not sorted; ids increase with insertion.
	mk := func(date time.Time, desc string) {
		if err := s.Create(ctx, &expense.Expense{Date: date, Amount: 100, Description: desc, RawText: "1 " + desc}); err != nil {
			t.Fatalf("Create %s: %v", desc, err)
		}
	}
	mk(day1, "older-a") // id 1
	mk(day2, "newer-a") // id 2
	mk(day2, "newer-b") // id 3
	mk(day1, "older-b") // id 4

	got, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	wantOrder := []string{"newer-b", "newer-a", "older-b", "older-a"}
	if len(got) != len(wantOrder) {
		t.Fatalf("List = %d rows, want %d", len(got), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got[i].Description != want {
			t.Errorf("row %d = %q, want %q (order: %v)", i, got[i].Description, want, wantOrder)
		}
	}
}

// AC: update refreshes updated_at and keeps identity; changes persist, and an
// account can be cleared back to NULL.
func TestUpdateRefreshesUpdatedAtKeepsIdentity(t *testing.T) {
	clk := &fakeClock{t: baseTime}
	s := openTemp(t, store.WithClock(clk.now))
	ctx := context.Background()

	e := &expense.Expense{
		Date:        baseTime,
		Amount:      1000,
		Description: "coffee",
		Account:     ptr("personal"),
		RawText:     "10 coffee @personal",
	}
	if err := s.Create(ctx, e); err != nil {
		t.Fatalf("Create: %v", err)
	}
	origID, origCreated := e.ID, e.CreatedAt

	clk.advance(48 * time.Hour)
	e.Amount = 1200
	e.Description = "coffee and cake"
	e.Account = nil // clear the account
	if err := s.Update(ctx, e); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := s.Get(ctx, origID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != origID {
		t.Errorf("ID = %d, want %d (identity must be kept)", got.ID, origID)
	}
	if !got.CreatedAt.Equal(origCreated) {
		t.Errorf("CreatedAt = %v, want %v (must not change on update)", got.CreatedAt, origCreated)
	}
	if !got.UpdatedAt.After(got.CreatedAt) {
		t.Errorf("UpdatedAt = %v, want after CreatedAt %v", got.UpdatedAt, got.CreatedAt)
	}
	if !got.UpdatedAt.Equal(baseTime.Add(48 * time.Hour)) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, baseTime.Add(48*time.Hour))
	}
	if got.Amount != 1200 || got.Description != "coffee and cake" {
		t.Errorf("updated fields not persisted: amount %d, desc %q", got.Amount, got.Description)
	}
	if got.Account != nil {
		t.Errorf("Account = %q, want NULL after clearing", *got.Account)
	}
}

// AC: delete removes the row.
func TestDeleteRemoves(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	e := &expense.Expense{Date: baseTime, Amount: 500, Description: "snack", RawText: "5 snack"}
	if err := s.Create(ctx, e); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Delete(ctx, e.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, e.ID); err != store.ErrNotFound {
		t.Errorf("Get after Delete err = %v, want ErrNotFound", err)
	}
	got, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List after Delete = %d rows, want 0", len(got))
	}
}

// Unknown ids surface ErrNotFound rather than a nil/zero result, on every
// single-row operation.
func TestUnknownIDReturnsNotFound(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	if _, err := s.Get(ctx, 999); err != store.ErrNotFound {
		t.Errorf("Get unknown err = %v, want ErrNotFound", err)
	}
	if err := s.Update(ctx, &expense.Expense{ID: 999, Date: baseTime, Amount: 1, Description: "x", RawText: "x"}); err != store.ErrNotFound {
		t.Errorf("Update unknown err = %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, 999); err != store.ErrNotFound {
		t.Errorf("Delete unknown err = %v, want ErrNotFound", err)
	}
}
