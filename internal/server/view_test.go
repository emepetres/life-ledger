package server

import (
	"testing"
	"time"

	"github.com/emepetres/life-ledger/internal/expense"
)

// mkExpense / mkIncome build stored records with explicit identity + timestamps,
// since groupByDay interleaves by date then created_at.
func mkExpense(id int64, date, created time.Time, amount int, desc string) expense.Expense {
	e := expense.NewExpense(date, amount, desc, nil, false, desc)
	e.ID = id
	e.CreatedAt = created
	e.UpdatedAt = created
	return *e
}

func mkIncome(id int64, date, created time.Time, amount int, desc string, link *int64) expense.Income {
	i := expense.NewIncome(date, amount, desc, nil, link, desc)
	i.ID = id
	i.CreatedAt = created
	i.UpdatedAt = created
	return *i
}

// AC: a standalone income interleaves by its own date into the day groups and
// renders as a credit row, and the day total is the sum of expenses only — the
// income does not move it (ADR-0009).
func TestGroupByDayInterleavesCreditWithoutMovingTotal(t *testing.T) {
	day := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)

	// Two expenses and one standalone income, all on the same day. created_at
	// order (newest first): income (t2), expense-b (t1), expense-a (t0).
	t0 := day.Add(9 * time.Hour)
	t1 := day.Add(10 * time.Hour)
	t2 := day.Add(11 * time.Hour)
	expenses := []expense.Expense{
		mkExpense(2, day, t1, 2000, "expense-b"),
		mkExpense(1, day, t0, 1000, "expense-a"),
	}
	incomes := []expense.Income{
		mkIncome(1, day, t2, 4000, "refund", nil),
	}

	groups := groupByDay(expenses, incomes, now)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1 (all same day)", len(groups))
	}
	g := groups[0]
	if len(g.Rows) != 3 {
		t.Fatalf("rows = %d, want 3 (2 expenses + 1 credit)", len(g.Rows))
	}
	// Day total = 1000 + 2000, unmoved by the 4000 credit.
	if g.Total != "€30.00" {
		t.Errorf("day total = %q, want €30.00 (expenses only)", g.Total)
	}
	// Interleaved newest-first by created_at: the credit came last, so it heads
	// the day.
	if !g.Rows[0].IsCredit {
		t.Errorf("row 0 IsCredit = false, want the most-recent income first")
	}
	if g.Rows[0].Amount != "−€40.00" {
		t.Errorf("credit amount = %q, want −€40.00", g.Rows[0].Amount)
	}
	// The credit row is display-only: no edit/delete affordance.
	if g.Rows[0].Editable {
		t.Errorf("credit row Editable = true, want false (display-only this slice)")
	}
	for _, r := range g.Rows[1:] {
		if r.IsCredit {
			t.Errorf("expense row marked IsCredit; got %+v", r)
		}
	}
}

// AC: a linked payback is not a standalone credit row and is skipped by
// groupByDay (it attaches to its expense in a later slice), so it never appears
// in the feed nor moves a total.
func TestGroupByDaySkipsLinkedPaybacks(t *testing.T) {
	day := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	parent := int64(1)

	expenses := []expense.Expense{mkExpense(1, day, day.Add(time.Hour), 6000, "team lunch")}
	incomes := []expense.Income{
		mkIncome(1, day, day.Add(2*time.Hour), 2000, "Bob's share", &parent), // linked
		mkIncome(2, day, day.Add(3*time.Hour), 500, "standalone", nil),
	}

	groups := groupByDay(expenses, incomes, now)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	g := groups[0]
	if len(g.Rows) != 2 {
		t.Fatalf("rows = %d, want 2 (expense + standalone credit; payback skipped)", len(g.Rows))
	}
	for _, r := range g.Rows {
		if r.IsCredit && r.Description == "Bob's share" {
			t.Errorf("linked payback rendered as a standalone row; got %+v", r)
		}
	}
	// Day total is the full expense; no payback nets it in this slice.
	if g.Total != "€60.00" {
		t.Errorf("day total = %q, want €60.00", g.Total)
	}
}
