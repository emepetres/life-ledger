package store_test

import (
	"context"
	"os"
	"path/filepath"
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
