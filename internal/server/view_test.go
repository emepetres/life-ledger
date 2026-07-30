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
	// A recent standalone income is editable, gated by the same one-year cutoff as
	// an expense (ADR-0008); its row carries the id so its kind-qualified controls
	// can target /edit/income and /delete/income (ADR-0009).
	if !g.Rows[0].Editable {
		t.Errorf("recent credit row Editable = false, want true (within the edit cutoff)")
	}
	if g.Rows[0].ID != 1 {
		t.Errorf("credit row ID = %d, want 1 (threaded through for its controls)", g.Rows[0].ID)
	}
	for _, r := range g.Rows[1:] {
		if r.IsCredit {
			t.Errorf("expense row marked IsCredit; got %+v", r)
		}
	}
}

// AC: a linked payback is not a standalone credit row — it is bucketed onto its
// parent expense rather than interleaved into the feed — and it nets the
// parent's cost down, so the day total reflects the net (ADR-0009).
func TestGroupByDayAttachesPaybackAndNetsTotal(t *testing.T) {
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
	// The payback is not a standalone row: only the expense and the standalone
	// credit are feed rows.
	if len(g.Rows) != 2 {
		t.Fatalf("rows = %d, want 2 (expense + standalone credit; payback attached)", len(g.Rows))
	}
	for _, r := range g.Rows {
		if r.IsCredit && r.Description == "Bob's share" {
			t.Errorf("linked payback rendered as a standalone row; got %+v", r)
		}
	}
	// The expense row carries its payback and derived net cost (6000 − 2000).
	var expenseRow rowView
	for _, r := range g.Rows {
		if !r.IsCredit {
			expenseRow = r
		}
	}
	if !expenseRow.HasPaybacks {
		t.Fatalf("expense row should carry its payback; got %+v", expenseRow)
	}
	if expenseRow.PaybackCount != 1 || len(expenseRow.Paybacks) != 1 {
		t.Errorf("expense row PaybackCount = %d / len = %d, want 1", expenseRow.PaybackCount, len(expenseRow.Paybacks))
	}
	if expenseRow.Net != "€40.00" {
		t.Errorf("net cost = %q, want €40.00 (6000 − 2000)", expenseRow.Net)
	}
	if expenseRow.Paid != "€60.00" {
		t.Errorf("paid = %q, want the full €60.00 struck-through", expenseRow.Paid)
	}
	if expenseRow.OverRepaid {
		t.Errorf("net is positive, OverRepaid should be false")
	}
	if expenseRow.PaybackSum != "−€20.00" {
		t.Errorf("payback summary = %q, want −€20.00", expenseRow.PaybackSum)
	}
	// Day total = net cost of the expense (4000); the standalone credit does not
	// move it.
	if g.Total != "€40.00" {
		t.Errorf("day total = %q, want €40.00 (net cost, credit unmoved)", g.Total)
	}
}

// AC: when Σ paybacks exceeds the paid amount the derived net cost goes negative
// (over-repaid) and is flagged so it renders green; the day total follows it
// negative (ADR-0009, ungated).
func TestGroupByDayOverRepaidGoesNegativeGreen(t *testing.T) {
	day := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	parent := int64(1)

	expenses := []expense.Expense{mkExpense(1, day, day.Add(time.Hour), 5000, "dinner")}
	incomes := []expense.Income{
		mkIncome(1, day, day.Add(2*time.Hour), 3000, "Bob", &parent),
		mkIncome(2, day, day.Add(3*time.Hour), 3000, "Alice", &parent),
	}

	groups := groupByDay(expenses, incomes, now)
	g := groups[0]
	r := g.Rows[0]
	if !r.HasPaybacks || r.PaybackCount != 2 {
		t.Fatalf("expected 2 paybacks attached; got %+v", r)
	}
	// Net = 5000 − 6000 = −1000, rendered as a green credit "−€10.00".
	if !r.OverRepaid {
		t.Errorf("over-repaid expense should set OverRepaid")
	}
	if r.Net != "−€10.00" {
		t.Errorf("over-repaid net = %q, want −€10.00", r.Net)
	}
	if g.Total != "−€10.00" {
		t.Errorf("day total = %q, want −€10.00 (net over-repaid)", g.Total)
	}
}

// AC: an expense with no paybacks carries no net-cost fields, so it renders
// exactly as it does today.
func TestGroupByDayNoPaybacksLeavesRowPlain(t *testing.T) {
	day := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)

	expenses := []expense.Expense{mkExpense(1, day, day.Add(time.Hour), 6000, "team lunch")}
	groups := groupByDay(expenses, nil, now)
	r := groups[0].Rows[0]
	if r.HasPaybacks {
		t.Errorf("expense with no paybacks should not set HasPaybacks; got %+v", r)
	}
	if r.Net != "" || r.Paid != "" || len(r.Paybacks) != 0 {
		t.Errorf("expense with no paybacks should carry no net-cost fields; got %+v", r)
	}
	if groups[0].Total != "€60.00" {
		t.Errorf("day total = %q, want €60.00", groups[0].Total)
	}
}
