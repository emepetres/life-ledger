package expense_test

import (
	"testing"
	"time"

	"github.com/emepetres/life-ledger/internal/expense"
)

func ptr(s string) *string { return &s }

// TestEntryLine pins the canonical entry-line rendering: amount-first, an
// absolute "DD/MM" date with no leading zeros, optional "@account" and "*", in a
// fixed order, with whole euros dropping the fractional part (ADR-0008).
func TestEntryLine(t *testing.T) {
	tests := []struct {
		name string
		e    *expense.Expense
		want string
	}{
		{
			name: "whole amount drops decimals",
			e:    expense.NewExpense(day(2026, 7, 5), 1000, "lunch", nil, false, ""),
			want: "10 lunch 5/7",
		},
		{
			name: "two decimals kept",
			e:    expense.NewExpense(day(2026, 12, 24), 1250, "lunch", nil, false, ""),
			want: "12.50 lunch 24/12",
		},
		{
			name: "single fractional digit padded",
			e:    expense.NewExpense(day(2026, 1, 9), 760, "coffee", nil, false, ""),
			want: "7.60 coffee 9/1",
		},
		{
			name: "account and split, fixed order",
			e:    expense.NewExpense(day(2026, 7, 5), 1000, "dinner", ptr("work"), true, ""),
			want: "10 dinner 5/7 @work *",
		},
		{
			name: "empty account pointer omitted",
			e:    expense.NewExpense(day(2026, 7, 5), 1000, "dinner", ptr(""), false, ""),
			want: "10 dinner 5/7",
		},
		{
			name: "today-dated expense still emits its date",
			e:    expense.NewExpense(day(2026, 7, 21), 500, "gum", nil, false, ""),
			want: "5 gum 21/7",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.e.EntryLine(); got != tt.want {
				t.Errorf("EntryLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestIncomeEntryLine pins the income rendering: the amount carries the leading
// '+' income sigil and a '*' is never emitted, over the same middle an expense
// uses (ADR-0009).
func TestIncomeEntryLine(t *testing.T) {
	tests := []struct {
		name string
		i    *expense.Income
		want string
	}{
		{
			name: "whole amount gains the + sigil",
			i:    expense.NewIncome(day(2026, 7, 5), 3000, "Bob's share", nil, nil, ""),
			want: "+30 Bob's share 5/7",
		},
		{
			name: "decimals kept behind the +",
			i:    expense.NewIncome(day(2026, 7, 19), 1250, "x", ptr("bbva"), nil, ""),
			want: "+12.50 x 19/7 @bbva",
		},
		{
			name: "a linked payback renders like any income (link is out-of-band)",
			i:    expense.NewIncome(day(2026, 7, 5), 500, "lunch payback", nil, ptr64(7), ""),
			want: "+5 lunch payback 5/7",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.i.EntryLine(); got != tt.want {
				t.Errorf("EntryLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ptr64 returns a pointer to an int64, for building a linked payback's parent id.
func ptr64(n int64) *int64 { return &n }

// TestEntryLineRoundTrips is the core invariant, stated as the coupling between
// EntryLine and Editable: whenever an expense is Editable as of some "now",
// parsing its canonical entry line against that now reproduces the same fields —
// so an edit that re-parses the box can never shift the date (ADR-0008). The
// non-editable nows (a year or more later, where a year-less DD/MM would drift)
// are exactly the ones the guard forbids, so they are skipped, not asserted.
func TestEntryLineRoundTrips(t *testing.T) {
	nows := []time.Time{
		day(2026, 7, 21),  // same day
		day(2026, 7, 26),  // a few days later
		day(2026, 12, 31), // months later, across the day-of-year
		day(2027, 7, 20),  // just under a year later — still editable
		day(2027, 7, 21),  // exactly a year later — NOT editable, must be skipped
	}
	exps := []*expense.Expense{
		expense.NewExpense(day(2026, 7, 21), 1000, "lunch", nil, false, ""),
		expense.NewExpense(day(2026, 7, 21), 1250, "dinner out", ptr("work"), true, ""),
		expense.NewExpense(day(2026, 7, 21), 760, "coffee 2 cups", nil, false, ""), // bare number stays in description
		expense.NewExpense(day(2025, 12, 24), 500, "gum", nil, false, ""),          // year-less DD/MM back-resolves
	}
	asserted := 0
	for _, e := range exps {
		line := e.EntryLine()
		for _, now := range nows {
			if !expense.Editable(e.Date, now) {
				continue // outside the guard's window; EntryLine is never re-parsed here
			}
			asserted++
			got := expense.Parse(line, now)
			if !got.OK() {
				t.Errorf("Parse(%q, %s) has errors %v, want none", line, now.Format("2006-01-02"), got.Errors)
				continue
			}
			acc := ""
			if e.Account != nil {
				acc = *e.Account
			}
			if got.Amount != e.Amount || got.Description != e.Description ||
				got.Account != acc || got.Split != e.Split || got.IsIncome || !got.Date.Equal(e.Date) {
				t.Errorf("round-trip of %q at now=%s\n got  amount=%d desc=%q acc=%q split=%v income=%v date=%s\n want amount=%d desc=%q acc=%q split=%v income=false date=%s",
					line, now.Format("2006-01-02"),
					got.Amount, got.Description, got.Account, got.Split, got.IsIncome, got.Date.Format("2006-01-02"),
					e.Amount, e.Description, acc, e.Split, e.Date.Format("2006-01-02"))
			}
		}
	}
	if asserted == 0 {
		t.Fatal("round-trip invariant was never exercised; check the now/expense fixtures")
	}
}

// TestIncomeEntryLineRoundTrips mirrors the expense invariant for incomes: an
// income's canonical entry line re-parses (within the editable window) to
// IsIncome with the same fields and never a split (ADR-0009). LinkedExpenseID is
// out-of-band and not part of the entry line, so it is preserved by the caller,
// not the round-trip.
func TestIncomeEntryLineRoundTrips(t *testing.T) {
	nows := []time.Time{
		day(2026, 7, 21),  // same day
		day(2026, 12, 31), // months later, across the day-of-year
		day(2027, 7, 20),  // just under a year later — still editable
	}
	incomes := []*expense.Income{
		expense.NewIncome(day(2026, 7, 21), 3000, "Bob's share", nil, nil, ""),
		expense.NewIncome(day(2026, 7, 21), 1250, "lunch payback", ptr("bbva"), ptr64(7), ""),
		expense.NewIncome(day(2025, 12, 24), 500, "gift back", nil, nil, ""),
	}
	asserted := 0
	for _, in := range incomes {
		line := in.EntryLine()
		for _, now := range nows {
			if !expense.Editable(in.Date, now) {
				continue
			}
			asserted++
			got := expense.Parse(line, now)
			if !got.OK() {
				t.Errorf("Parse(%q, %s) has errors %v, want none", line, now.Format("2006-01-02"), got.Errors)
				continue
			}
			acc := ""
			if in.Account != nil {
				acc = *in.Account
			}
			if got.Amount != in.Amount || got.Description != in.Description ||
				got.Account != acc || got.Split || !got.IsIncome || !got.Date.Equal(in.Date) {
				t.Errorf("round-trip of %q at now=%s\n got  amount=%d desc=%q acc=%q split=%v income=%v date=%s\n want amount=%d desc=%q acc=%q split=false income=true date=%s",
					line, now.Format("2006-01-02"),
					got.Amount, got.Description, got.Account, got.Split, got.IsIncome, got.Date.Format("2006-01-02"),
					in.Amount, in.Description, acc, in.Date.Format("2006-01-02"))
			}
		}
	}
	if asserted == 0 {
		t.Fatal("income round-trip invariant was never exercised; check the fixtures")
	}
}

// TestEditable pins the cutoff: an expense is editable iff its date is strictly
// less than a year old (strictly after the same calendar day one year before
// today). Exactly one year ago is NOT editable — that is the day a year-less
// DD/MM starts drifting, so it is exactly the "DD/MM" round-trip boundary
// (ADR-0008). now carries clock fields to confirm they are ignored.
func TestEditable(t *testing.T) {
	now := time.Date(2026, 7, 21, 15, 4, 5, 0, time.UTC)
	tests := []struct {
		name string
		date time.Time
		want bool
	}{
		{"today", day(2026, 7, 21), true},
		{"recent", day(2026, 7, 1), true},
		{"just under a year", day(2025, 7, 22), true},
		{"exactly one year ago", day(2025, 7, 21), false},
		{"one day past a year", day(2025, 7, 20), false},
		{"years old", day(2023, 1, 1), false},
		{"future", day(2026, 12, 25), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := expense.Editable(tt.date, now); got != tt.want {
				t.Errorf("Editable(%s, %s) = %v, want %v", tt.date.Format("2006-01-02"), now.Format("2006-01-02"), got, tt.want)
			}
		})
	}
}
