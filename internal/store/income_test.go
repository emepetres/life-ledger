package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/emepetres/life-ledger/internal/expense"
	"github.com/emepetres/life-ledger/internal/store"
)

// AC: CreateIncome + ListIncomes round-trip a standalone income, assigning an id
// and timestamps and preserving every field (a nil link = standalone).
func TestCreateIncomeRoundTrips(t *testing.T) {
	clk := &fakeClock{t: baseTime}
	s := openTemp(t, store.WithClock(clk.now))
	ctx := context.Background()

	i := expense.NewIncome(
		time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
		4000, "refund", ptr("bbva"), nil, "+40 refund @bbva",
	)
	if err := s.CreateIncome(ctx, i); err != nil {
		t.Fatalf("CreateIncome: %v", err)
	}
	if i.ID == 0 {
		t.Error("CreateIncome did not assign an ID")
	}
	if !i.CreatedAt.Equal(baseTime) || !i.UpdatedAt.Equal(baseTime) {
		t.Errorf("CreateIncome stamps = created %v / updated %v, want both %v", i.CreatedAt, i.UpdatedAt, baseTime)
	}

	got, err := s.ListIncomes(ctx)
	if err != nil {
		t.Fatalf("ListIncomes: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListIncomes = %d rows, want 1", len(got))
	}
	g := got[0]
	if !g.Date.Equal(i.Date) {
		t.Errorf("Date = %v, want %v", g.Date, i.Date)
	}
	if g.Amount != 4000 {
		t.Errorf("Amount = %d, want 4000", g.Amount)
	}
	if g.Description != "refund" {
		t.Errorf("Description = %q, want %q", g.Description, "refund")
	}
	if g.Account == nil || *g.Account != "bbva" {
		t.Errorf("Account = %v, want \"bbva\"", g.Account)
	}
	if g.LinkedExpenseID != nil {
		t.Errorf("LinkedExpenseID = %v, want nil (standalone)", *g.LinkedExpenseID)
	}
	if g.RawText != i.RawText {
		t.Errorf("RawText = %q, want %q", g.RawText, i.RawText)
	}
	if !g.CreatedAt.Equal(baseTime) || !g.UpdatedAt.Equal(baseTime) {
		t.Errorf("timestamps = created %v / updated %v, want both %v", g.CreatedAt, g.UpdatedAt, baseTime)
	}
}

// AC: an omitted account persists as NULL (a nil pointer, not an empty string).
func TestCreateIncomeOmittedAccountPersistsAsNull(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	i := expense.NewIncome(baseTime, 500, "cash back", nil, nil, "+5 cash back")
	if err := s.CreateIncome(ctx, i); err != nil {
		t.Fatalf("CreateIncome: %v", err)
	}
	got, err := s.ListIncomes(ctx)
	if err != nil {
		t.Fatalf("ListIncomes: %v", err)
	}
	if len(got) != 1 || got[0].Account != nil {
		t.Errorf("Account = %v, want NULL (nil pointer)", got[0].Account)
	}
}

// AC: amount <= 0 is rejected by the CHECK constraint, so a non-positive income
// can never be stored.
func TestCreateIncomeRejectsNonPositiveAmount(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	for _, amount := range []int{0, -100} {
		i := expense.NewIncome(baseTime, amount, "bad", nil, nil, "+0 bad")
		if err := s.CreateIncome(ctx, i); err == nil {
			t.Errorf("CreateIncome amount=%d returned nil, want CHECK violation", amount)
		}
	}
	got, err := s.ListIncomes(ctx)
	if err != nil {
		t.Fatalf("ListIncomes: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListIncomes = %d rows, want 0 (nothing stored)", len(got))
	}
}

// AC: ListIncomes is newest-first — date descending, then id descending within a
// day — and returns all incomes (linked paybacks and standalone alike).
func TestListIncomesNewestFirstIncludesLinked(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	// A parent expense to link a payback to, exercising the FK.
	parent := expense.NewExpense(baseTime, 6000, "team lunch", nil, true, "60 team lunch *")
	if err := s.Create(ctx, parent); err != nil {
		t.Fatalf("Create parent: %v", err)
	}

	day1 := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	mk := func(date time.Time, desc string, link *int64) {
		if err := s.CreateIncome(ctx, expense.NewIncome(date, 1000, desc, nil, link, "+10 "+desc)); err != nil {
			t.Fatalf("CreateIncome %s: %v", desc, err)
		}
	}
	mk(day1, "older", nil)                // id 1
	mk(day2, "newer-standalone", nil)     // id 2
	mk(day2, "newer-payback", &parent.ID) // id 3, linked

	got, err := s.ListIncomes(ctx)
	if err != nil {
		t.Fatalf("ListIncomes: %v", err)
	}
	wantOrder := []string{"newer-payback", "newer-standalone", "older"}
	if len(got) != len(wantOrder) {
		t.Fatalf("ListIncomes = %d rows, want %d (linked + standalone)", len(got), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got[i].Description != want {
			t.Errorf("row %d = %q, want %q (order %v)", i, got[i].Description, want, wantOrder)
		}
	}
	// The payback carries its parent link; the standalone incomes do not.
	if got[0].LinkedExpenseID == nil || *got[0].LinkedExpenseID != parent.ID {
		t.Errorf("payback LinkedExpenseID = %v, want %d", got[0].LinkedExpenseID, parent.ID)
	}
	if got[1].LinkedExpenseID != nil {
		t.Errorf("standalone LinkedExpenseID = %v, want nil", *got[1].LinkedExpenseID)
	}
}

// AC: ON DELETE CASCADE — deleting a fronted expense deletes its linked
// paybacks with it (ADR-0009), while standalone incomes are untouched.
func TestDeleteExpenseCascadesPaybacks(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	parent := expense.NewExpense(baseTime, 6000, "team lunch", nil, true, "60 team lunch *")
	if err := s.Create(ctx, parent); err != nil {
		t.Fatalf("Create parent: %v", err)
	}
	if err := s.CreateIncome(ctx, expense.NewIncome(baseTime, 2000, "Bob's share", nil, &parent.ID, "+20 Bob's share")); err != nil {
		t.Fatalf("CreateIncome payback: %v", err)
	}
	if err := s.CreateIncome(ctx, expense.NewIncome(baseTime, 500, "standalone", nil, nil, "+5 standalone")); err != nil {
		t.Fatalf("CreateIncome standalone: %v", err)
	}

	if err := s.Delete(ctx, parent.ID); err != nil {
		t.Fatalf("Delete parent: %v", err)
	}

	got, err := s.ListIncomes(ctx)
	if err != nil {
		t.Fatalf("ListIncomes: %v", err)
	}
	if len(got) != 1 || got[0].Description != "standalone" {
		t.Errorf("after cascade ListIncomes = %+v, want only the standalone income", got)
	}
}

// AC: GetIncome round-trips a stored income by id, and reports ErrNotFound for
// an unknown id (mirroring Get for expenses).
func TestGetIncomeRoundTripsAndNotFound(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	i := expense.NewIncome(baseTime, 3000, "Bob share", ptr("bbva"), nil, "+30 Bob share @bbva")
	if err := s.CreateIncome(ctx, i); err != nil {
		t.Fatalf("CreateIncome: %v", err)
	}

	got, err := s.GetIncome(ctx, i.ID)
	if err != nil {
		t.Fatalf("GetIncome: %v", err)
	}
	if got.ID != i.ID || got.Amount != 3000 || got.Description != "Bob share" {
		t.Errorf("GetIncome = %+v, want the seeded income", got)
	}
	if got.Account == nil || *got.Account != "bbva" {
		t.Errorf("Account = %v, want \"bbva\"", got.Account)
	}

	if _, err := s.GetIncome(ctx, 999); err != store.ErrNotFound {
		t.Errorf("GetIncome unknown err = %v, want ErrNotFound", err)
	}
}

// AC: UpdateIncome refreshes updated_at and keeps identity (id, created_at); it
// preserves linked_expense_id even when the passed record carries a nil link, so
// fixing a payback's amount/account never re-parents it (ADR-0009). An unknown id
// is ErrNotFound.
func TestUpdateIncomePreservesLinkAndIdentity(t *testing.T) {
	clk := &fakeClock{t: baseTime}
	s := openTemp(t, store.WithClock(clk.now))
	ctx := context.Background()

	// A parent expense and a payback linked to it.
	parent := expense.NewExpense(baseTime, 9000, "team lunch", nil, true, "90 team lunch *")
	if err := s.Create(ctx, parent); err != nil {
		t.Fatalf("Create parent: %v", err)
	}
	pb := expense.NewIncome(baseTime, 3000, "Bob share", ptr("bbva"), &parent.ID, "+30 Bob share @bbva")
	if err := s.CreateIncome(ctx, pb); err != nil {
		t.Fatalf("CreateIncome payback: %v", err)
	}
	origID, origCreated := pb.ID, pb.CreatedAt

	// Edit the payback: change amount and account, and deliberately pass a nil
	// link — the store must keep the stored parent link rather than detach it.
	clk.advance(48 * time.Hour)
	edited := expense.NewIncome(baseTime, 3500, "Bob share", ptr("amex"), nil, "+35 Bob share @amex")
	edited.ID = origID
	if err := s.UpdateIncome(ctx, edited); err != nil {
		t.Fatalf("UpdateIncome: %v", err)
	}

	got, err := s.GetIncome(ctx, origID)
	if err != nil {
		t.Fatalf("GetIncome after update: %v", err)
	}
	if got.ID != origID {
		t.Errorf("ID = %d, want %d (identity must be kept)", got.ID, origID)
	}
	if !got.CreatedAt.Equal(origCreated) {
		t.Errorf("CreatedAt = %v, want %v (must not change on update)", got.CreatedAt, origCreated)
	}
	if !got.UpdatedAt.Equal(baseTime.Add(48 * time.Hour)) {
		t.Errorf("UpdatedAt = %v, want %v (refreshed)", got.UpdatedAt, baseTime.Add(48*time.Hour))
	}
	if got.LinkedExpenseID == nil || *got.LinkedExpenseID != parent.ID {
		t.Errorf("LinkedExpenseID = %v, want %d (link must survive the edit)", got.LinkedExpenseID, parent.ID)
	}
	if got.Amount != 3500 || got.Account == nil || *got.Account != "amex" {
		t.Errorf("edited fields not persisted: %+v", got)
	}

	unknown := expense.NewIncome(baseTime, 1, "x", nil, nil, "+1 x")
	unknown.ID = 999
	if err := s.UpdateIncome(ctx, unknown); err != store.ErrNotFound {
		t.Errorf("UpdateIncome unknown err = %v, want ErrNotFound", err)
	}
}

// AC: DeleteIncome removes a single income by id and reports ErrNotFound for an
// unknown id. It targets the income table only, leaving expenses untouched.
func TestDeleteIncomeRemovesAndNotFound(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	i := expense.NewIncome(baseTime, 500, "cash back", nil, nil, "+5 cash back")
	if err := s.CreateIncome(ctx, i); err != nil {
		t.Fatalf("CreateIncome: %v", err)
	}
	if err := s.DeleteIncome(ctx, i.ID); err != nil {
		t.Fatalf("DeleteIncome: %v", err)
	}
	if _, err := s.GetIncome(ctx, i.ID); err != store.ErrNotFound {
		t.Errorf("GetIncome after DeleteIncome err = %v, want ErrNotFound", err)
	}
	got, err := s.ListIncomes(ctx)
	if err != nil {
		t.Fatalf("ListIncomes: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListIncomes after delete = %d rows, want 0", len(got))
	}

	if err := s.DeleteIncome(ctx, 999); err != store.ErrNotFound {
		t.Errorf("DeleteIncome unknown err = %v, want ErrNotFound", err)
	}
}

// The description save gate lives in the parser, not the store; an empty
// description still stores here (the store is a thin repository). Guard against
// accidentally adding a store-level constraint that would surprise callers.
func TestCreateIncomeStoresEmptyDescription(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	err := s.CreateIncome(ctx, expense.NewIncome(baseTime, 100, "", nil, nil, "+1"))
	if err != nil && strings.Contains(err.Error(), "CHECK") {
		t.Errorf("store must not gate description: %v", err)
	}
}
