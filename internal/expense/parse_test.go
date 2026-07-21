package expense_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/emepetres/life-ledger/internal/expense"
)

// today is a fixed injected "now" so date-relative cases are deterministic.
// 2026-07-21 is a Tuesday; well clear of month/year boundaries.
var today = time.Date(2026, 7, 21, 12, 30, 0, 0, time.UTC)

// day builds a calendar-day time in the same location Parse uses, for comparing
// resolved dates.
func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestParse(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantAmount  int
		wantHasAmt  bool
		wantDesc    string
		wantAccount string
		wantSplit   bool
		wantDate    time.Time
		wantErrs    []expense.ParseError
	}{
		{
			name:       "amount with dot decimal and description",
			raw:        "12.50 coffee",
			wantAmount: 1250, wantHasAmt: true, wantDesc: "coffee", wantDate: day(2026, 7, 21),
		},
		{
			name:       "amount with comma decimal equals dot",
			raw:        "12,50 coffee",
			wantAmount: 1250, wantHasAmt: true, wantDesc: "coffee", wantDate: day(2026, 7, 21),
		},
		{
			name:       "integer amount, multi-word description",
			raw:        "5 cervezas entrelineas",
			wantAmount: 500, wantHasAmt: true, wantDesc: "cervezas entrelineas", wantDate: day(2026, 7, 21),
		},
		{
			name:       "single fractional digit scales to two",
			raw:        "7.6 helados",
			wantAmount: 760, wantHasAmt: true, wantDesc: "helados", wantDate: day(2026, 7, 21),
		},
		{
			name:       "more than two fractional digits are truncated",
			raw:        "12.567 thing",
			wantAmount: 1256, wantHasAmt: true, wantDesc: "thing", wantDate: day(2026, 7, 21),
		},
		{
			name:       "relative date -1 is yesterday",
			raw:        "10 lunch -1",
			wantAmount: 1000, wantHasAmt: true, wantDesc: "lunch", wantDate: day(2026, 7, 20),
		},
		{
			name:       "relative date -0 is today",
			raw:        "10 lunch -0",
			wantAmount: 1000, wantHasAmt: true, wantDesc: "lunch", wantDate: day(2026, 7, 21),
		},
		{
			name:       "DD/MM most recent past this year",
			raw:        "10 lunch 17/7",
			wantAmount: 1000, wantHasAmt: true, wantDesc: "lunch", wantDate: day(2026, 7, 17),
		},
		{
			name:       "DD/MM equal to today resolves to today",
			raw:        "10 lunch 21/7",
			wantAmount: 1000, wantHasAmt: true, wantDesc: "lunch", wantDate: day(2026, 7, 21),
		},
		{
			name:       "DD/MM in the future rolls back a year",
			raw:        "10 gift 24/12",
			wantAmount: 1000, wantHasAmt: true, wantDesc: "gift", wantDate: day(2025, 12, 24),
		},
		{
			name:       "DD/MM/YYYY overrides the inferred year, future allowed",
			raw:        "10 gift 24/12/2030",
			wantAmount: 1000, wantHasAmt: true, wantDesc: "gift", wantDate: day(2030, 12, 24),
		},
		{
			name:       "DD/MM/YYYY can name a more-than-a-year-old date",
			raw:        "10 gift 1/1/2020",
			wantAmount: 1000, wantHasAmt: true, wantDesc: "gift", wantDate: day(2020, 1, 1),
		},
		{
			name:       "account tag",
			raw:        "10 lunch @work",
			wantAmount: 1000, wantHasAmt: true, wantDesc: "lunch", wantAccount: "work", wantDate: day(2026, 7, 21),
		},
		{
			name:       "hyphenated account is a single token",
			raw:        "10 lunch @credit-card",
			wantAmount: 1000, wantHasAmt: true, wantDesc: "lunch", wantAccount: "credit-card", wantDate: day(2026, 7, 21),
		},
		{
			name:       "standalone star sets split",
			raw:        "10 lunch *",
			wantAmount: 1000, wantHasAmt: true, wantDesc: "lunch", wantSplit: true, wantDate: day(2026, 7, 21),
		},
		{
			name:       "markers float in arbitrary order",
			raw:        "10 @work lunch * -1",
			wantAmount: 1000, wantHasAmt: true, wantDesc: "lunch", wantAccount: "work", wantSplit: true, wantDate: day(2026, 7, 20),
		},
		{
			name:       "multiple bare numbers: first is amount, rest stay in description",
			raw:        "12 gallo 5 rojo",
			wantAmount: 1200, wantHasAmt: true, wantDesc: "gallo 5 rojo", wantDate: day(2026, 7, 21),
		},
		{
			name:       "omitted date defaults to today",
			raw:        "10 lunch",
			wantAmount: 1000, wantHasAmt: true, wantDesc: "lunch", wantDate: day(2026, 7, 21),
		},
		{
			name:       "leading minus is a date offset, never a negative amount",
			raw:        "-1 coffee",
			wantAmount: 0, wantHasAmt: false, wantDesc: "coffee", wantDate: day(2026, 7, 20),
			wantErrs: []expense.ParseError{expense.ErrNoAmount},
		},
		{
			name:       "zero is not a positive amount; stays in description, no amount",
			raw:        "0 coffee",
			wantHasAmt: false, wantDesc: "0 coffee", wantDate: day(2026, 7, 21),
			wantErrs: []expense.ParseError{expense.ErrNoAmount},
		},
		{
			name:       "zero rejected, later positive number becomes the amount",
			raw:        "0 5 coffee",
			wantAmount: 500, wantHasAmt: true, wantDesc: "0 coffee", wantDate: day(2026, 7, 21),
		},
		{
			name:       "no amount error",
			raw:        "coffee please",
			wantHasAmt: false, wantDesc: "coffee please", wantDate: day(2026, 7, 21),
			wantErrs: []expense.ParseError{expense.ErrNoAmount},
		},
		{
			name:       "empty description error (amount only)",
			raw:        "12.50",
			wantAmount: 1250, wantHasAmt: true, wantDesc: "", wantDate: day(2026, 7, 21),
			wantErrs: []expense.ParseError{expense.ErrEmptyDescription},
		},
		{
			name:       "empty description error with only markers left",
			raw:        "12.50 @work *",
			wantAmount: 1250, wantHasAmt: true, wantDesc: "", wantAccount: "work", wantSplit: true, wantDate: day(2026, 7, 21),
			wantErrs: []expense.ParseError{expense.ErrEmptyDescription},
		},
		{
			name:       "two date tokens error, first still resolves",
			raw:        "10 lunch -1 17/7",
			wantAmount: 1000, wantHasAmt: true, wantDesc: "lunch", wantDate: day(2026, 7, 20),
			wantErrs: []expense.ParseError{expense.ErrTwoDateTokens},
		},
		{
			name:       "two account tokens error, first kept",
			raw:        "10 lunch @work @home",
			wantAmount: 1000, wantHasAmt: true, wantDesc: "lunch", wantAccount: "work", wantDate: day(2026, 7, 21),
			wantErrs: []expense.ParseError{expense.ErrTwoAccounts},
		},
		{
			name:     "multiple errors surfaced together in fixed order",
			raw:      "@work @home",
			wantDesc: "", wantAccount: "work", wantDate: day(2026, 7, 21),
			wantErrs: []expense.ParseError{expense.ErrNoAmount, expense.ErrEmptyDescription, expense.ErrTwoAccounts},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expense.Parse(tt.raw, today)

			if got.Amount != tt.wantAmount {
				t.Errorf("Amount = %d, want %d", got.Amount, tt.wantAmount)
			}
			if got.HasAmount != tt.wantHasAmt {
				t.Errorf("HasAmount = %v, want %v", got.HasAmount, tt.wantHasAmt)
			}
			if got.Description != tt.wantDesc {
				t.Errorf("Description = %q, want %q", got.Description, tt.wantDesc)
			}
			if got.Account != tt.wantAccount {
				t.Errorf("Account = %q, want %q", got.Account, tt.wantAccount)
			}
			if got.Split != tt.wantSplit {
				t.Errorf("Split = %v, want %v", got.Split, tt.wantSplit)
			}
			if !got.Date.Equal(tt.wantDate) {
				t.Errorf("Date = %s, want %s", got.Date.Format("2006-01-02"), tt.wantDate.Format("2006-01-02"))
			}
			if !reflect.DeepEqual(got.Errors, tt.wantErrs) {
				t.Errorf("Errors = %v, want %v", got.Errors, tt.wantErrs)
			}
			if wantOK := len(tt.wantErrs) == 0; got.OK() != wantOK {
				t.Errorf("OK() = %v, want %v", got.OK(), wantOK)
			}
		})
	}
}

// TestParseDateResolutionAcrossYearBoundary pins the "logged a few days late in
// January" case from ADR-0002: a DD/MM in December, parsed in January, is last
// year's date.
func TestParseDateResolutionAcrossYearBoundary(t *testing.T) {
	jan := time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC)
	got := expense.Parse("10 gift 24/12", jan)
	want := day(2025, 12, 24)
	if !got.Date.Equal(want) {
		t.Errorf("Date = %s, want %s", got.Date.Format("2006-01-02"), want.Format("2006-01-02"))
	}
}

// TestParseIsPure verifies Parse does not mutate shared state: the same input
// and today yield an equal result on repeated calls, and the injected today is
// unchanged.
func TestParseIsPure(t *testing.T) {
	in := "12,50 lunch @work * -2"
	before := today
	first := expense.Parse(in, today)
	second := expense.Parse(in, today)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("Parse not deterministic:\n first  = %+v\n second = %+v", first, second)
	}
	if !today.Equal(before) {
		t.Errorf("Parse mutated injected today: %s != %s", today, before)
	}
}
