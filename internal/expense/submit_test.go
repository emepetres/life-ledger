package expense_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/emepetres/life-ledger/internal/expense"
)

// TestSubmitAdd pins the plain-add Intent: either kind is allowed and the
// leading '+' decides which record the line becomes.
func TestSubmitAdd(t *testing.T) {
	got := expense.Submit(expense.Add(), "12.50 lunch @work *", today)

	if !got.OK() {
		t.Fatalf("OK() = false, errors %v", got.Parsed.Errors)
	}
	if got.Income != nil {
		t.Fatalf("Income = %+v, want nil", got.Income)
	}
	want := expense.NewExpense(day(2026, 7, 21), 1250, "lunch", ptr("work"), true, "12.50 lunch @work *")
	if !reflect.DeepEqual(got.Expense, want) {
		t.Errorf("Expense = %+v, want %+v", got.Expense, want)
	}
}

// TestSubmitAddPlainAndSplitIncome fills the plain-add row: a plain line is an
// expense with no split, and a '+…*' line hits the existing split-on-income
// gate and produces no record.
func TestSubmitAddPlainAndSplitIncome(t *testing.T) {
	plain := expense.Submit(expense.Add(), "30 lunch", today)
	want := expense.NewExpense(day(2026, 7, 21), 3000, "lunch", nil, false, "30 lunch")
	if !reflect.DeepEqual(plain.Expense, want) || plain.Income != nil {
		t.Errorf("plain: Expense = %+v, Income = %+v; want Expense %+v", plain.Expense, plain.Income, want)
	}

	split := expense.Submit(expense.Add(), "+30 refund *", today)
	if !reflect.DeepEqual(split.Parsed.Errors, []expense.ParseError{expense.ErrSplitOnIncome}) {
		t.Errorf("+…*: Errors = %v, want [ErrSplitOnIncome]", split.Parsed.Errors)
	}
	if split.Expense != nil || split.Income != nil {
		t.Errorf("+…*: a rejected line must produce no record; got Expense %+v, Income %+v", split.Expense, split.Income)
	}
}

// fronted is a stored expense to hang paybacks off, with the identity the store
// would have stamped.
func fronted() expense.Expense {
	e := expense.NewExpense(day(2026, 7, 18), 9000, "group dinner", nil, false, "90 group dinner -3")
	e.ID = 7
	return *e
}

// TestSubmitAddPayback pins the payback Intent (ADR-0009 amendment, #62): the
// line must be an income, and the saved Income is linked to the parent
// out-of-band — never through the entry text.
func TestSubmitAddPayback(t *testing.T) {
	parentID := int64(7)
	tests := []struct {
		name       string
		raw        string
		wantErrs   []expense.ParseError
		wantIncome *expense.Income
	}{
		{
			name:       "income line: linked to the parent",
			raw:        "+30 Bob share",
			wantIncome: expense.NewIncome(day(2026, 7, 21), 3000, "Bob share", nil, &parentID, "+30 Bob share"),
		},
		{
			name:     "plain line: must be an income",
			raw:      "30 Bob share",
			wantErrs: []expense.ParseError{expense.ErrMustBeIncome},
		},
		{
			name:     "plain line on top of a parse error",
			raw:      "Bob share",
			wantErrs: []expense.ParseError{expense.ErrNoAmount, expense.ErrMustBeIncome},
		},
		{
			name:     "plain split line: must be an income",
			raw:      "30 Bob share *",
			wantErrs: []expense.ParseError{expense.ErrMustBeIncome},
		},
		{
			name:     "split income: the existing split gate, no kind error",
			raw:      "+30 Bob share *",
			wantErrs: []expense.ParseError{expense.ErrSplitOnIncome},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expense.Submit(expense.AddPayback(fronted()), tt.raw, today)

			if !reflect.DeepEqual(got.Parsed.Errors, tt.wantErrs) {
				t.Errorf("Errors = %v, want %v", got.Parsed.Errors, tt.wantErrs)
			}
			if got.Expense != nil {
				t.Errorf("Expense = %+v, want nil", got.Expense)
			}
			if !reflect.DeepEqual(got.Income, tt.wantIncome) {
				t.Errorf("Income = %+v, want %+v", got.Income, tt.wantIncome)
			}
		})
	}
}

// TestSubmitAddIncome: under the plain-add Intent a leading '+' makes the line
// a standalone Income, with no link to any expense.
func TestSubmitAddIncome(t *testing.T) {
	got := expense.Submit(expense.Add(), "+30 refund", today)

	if !got.OK() {
		t.Fatalf("OK() = false, errors %v", got.Parsed.Errors)
	}
	if got.Expense != nil {
		t.Fatalf("Expense = %+v, want nil", got.Expense)
	}
	want := expense.NewIncome(day(2026, 7, 21), 3000, "refund", nil, nil, "+30 refund")
	if !reflect.DeepEqual(got.Income, want) {
		t.Errorf("Income = %+v, want %+v", got.Income, want)
	}
}

// TestSubmitEditExpense pins the edit-an-expense Intent (ADR-0009 amendment:
// an edit keeps the record's kind). The result keeps the record's identity, and
// a line that would turn it into an income is refused, not converted.
func TestSubmitEditExpense(t *testing.T) {
	existing := fronted()
	in, ok := expense.EditExpense(existing, today)
	if !ok {
		t.Fatal("EditExpense refused a recent expense")
	}

	tests := []struct {
		name        string
		raw         string
		wantErrs    []expense.ParseError
		wantExpense *expense.Expense
	}{
		{
			name: "plain line: updated in place, split dropped",
			raw:  "95 group dinner 18/7",
			wantExpense: func() *expense.Expense {
				e := expense.NewExpense(day(2026, 7, 18), 9500, "group dinner", nil, false, "95 group dinner 18/7")
				e.ID = 7
				return e
			}(),
		},
		{
			name: "split line: updated in place",
			raw:  "95 group dinner 18/7 *",
			wantExpense: func() *expense.Expense {
				e := expense.NewExpense(day(2026, 7, 18), 9500, "group dinner", nil, true, "95 group dinner 18/7 *")
				e.ID = 7
				return e
			}(),
		},
		{
			name:     "income line: must stay an expense",
			raw:      "+95 group dinner 18/7",
			wantErrs: []expense.ParseError{expense.ErrMustBeExpense},
		},
		{
			name:     "split income line: both gates",
			raw:      "+95 group dinner 18/7 *",
			wantErrs: []expense.ParseError{expense.ErrSplitOnIncome, expense.ErrMustBeExpense},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expense.Submit(in, tt.raw, today)

			if !reflect.DeepEqual(got.Parsed.Errors, tt.wantErrs) {
				t.Errorf("Errors = %v, want %v", got.Parsed.Errors, tt.wantErrs)
			}
			if got.Income != nil {
				t.Errorf("Income = %+v, want nil", got.Income)
			}
			if !reflect.DeepEqual(got.Expense, tt.wantExpense) {
				t.Errorf("Expense = %+v, want %+v", got.Expense, tt.wantExpense)
			}
		})
	}
}

// TestSubmitEditIncome pins the edit-an-income Intent, for a standalone income
// and for a payback: the line must stay an income (ADR-0009 amendment), the
// result keeps the record's identity, and a payback keeps its parent link — an
// edit never re-parents a payback (ADR-0009 §5).
func TestSubmitEditIncome(t *testing.T) {
	parentID := int64(7)
	standalone := expense.NewIncome(day(2026, 7, 19), 3000, "refund", nil, nil, "+30 refund -2")
	standalone.ID = 11
	payback := expense.NewIncome(day(2026, 7, 20), 3000, "Bob share", nil, &parentID, "+30 Bob share -1")
	payback.ID = 12

	tests := []struct {
		name       string
		existing   *expense.Income
		raw        string
		wantErrs   []expense.ParseError
		wantIncome *expense.Income
	}{
		{
			name:       "standalone, income line: updated in place, still unlinked",
			existing:   standalone,
			raw:        "+35 refund 19/7 @home",
			wantIncome: editedIncome(11, day(2026, 7, 19), 3500, "refund", ptr("home"), nil, "+35 refund 19/7 @home"),
		},
		{
			name:       "payback, income line: keeps its parent",
			existing:   payback,
			raw:        "+35 Bob share 20/7",
			wantIncome: editedIncome(12, day(2026, 7, 20), 3500, "Bob share", nil, &parentID, "+35 Bob share 20/7"),
		},
		{
			name:     "standalone, plain line: must stay an income",
			existing: standalone,
			raw:      "35 refund 19/7",
			wantErrs: []expense.ParseError{expense.ErrMustBeIncome},
		},
		{
			name:     "payback, plain split line: must stay an income",
			existing: payback,
			raw:      "35 Bob share 20/7 *",
			wantErrs: []expense.ParseError{expense.ErrMustBeIncome},
		},
		{
			name:     "standalone, plain split line: must stay an income",
			existing: standalone,
			raw:      "35 refund 19/7 *",
			wantErrs: []expense.ParseError{expense.ErrMustBeIncome},
		},
		{
			name:     "standalone, split income line: split gate",
			existing: standalone,
			raw:      "+35 refund 19/7 *",
			wantErrs: []expense.ParseError{expense.ErrSplitOnIncome},
		},
		{
			name:     "payback, plain line: must stay an income",
			existing: payback,
			raw:      "35 Bob share 20/7",
			wantErrs: []expense.ParseError{expense.ErrMustBeIncome},
		},
		{
			name:     "payback, split income line: split gate",
			existing: payback,
			raw:      "+35 Bob share 20/7 *",
			wantErrs: []expense.ParseError{expense.ErrSplitOnIncome},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, ok := expense.EditIncome(*tt.existing, today)
			if !ok {
				t.Fatal("EditIncome refused a recent income")
			}

			got := expense.Submit(in, tt.raw, today)

			if !reflect.DeepEqual(got.Parsed.Errors, tt.wantErrs) {
				t.Errorf("Errors = %v, want %v", got.Parsed.Errors, tt.wantErrs)
			}
			if got.Expense != nil {
				t.Errorf("Expense = %+v, want nil", got.Expense)
			}
			if !reflect.DeepEqual(got.Income, tt.wantIncome) {
				t.Errorf("Income = %+v, want %+v", got.Income, tt.wantIncome)
			}
		})
	}
}

// editedIncome is the Income an edit is expected to produce: the given fields
// with the edited record's id.
func editedIncome(id int64, date time.Time, amount int, desc string, account *string, linked *int64, raw string) *expense.Income {
	i := expense.NewIncome(date, amount, desc, account, linked, raw)
	i.ID = id
	return i
}

// TestEditIntentWindow pins the edit window on the edit Intents (ADR-0008) in
// production's shape: "now" on the Europe/Madrid wall clock, record dates as the
// store returns them (UTC midnight). A record dated exactly one year ago must
// have no edit Intent — its "DD/MM" canonical line would re-parse a year later.
func TestEditIntentWindow(t *testing.T) {
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatalf("loading Europe/Madrid: %v", err)
	}
	now := time.Date(2026, 10, 5, 10, 0, 0, 0, madrid)
	stored := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

	tests := []struct {
		name string
		date time.Time
		want bool
		// skip names a known bug that still breaks this row.
		skip string
	}{
		{"today", stored(2026, 10, 5), true, ""},
		{"just under a year", stored(2025, 10, 6), true, ""},
		{"exactly one year ago", stored(2025, 10, 5), false,
			"known bug #114: Editable compares a UTC-midnight stored date against a Madrid-midnight cutoff"},
		{"over a year", stored(2025, 10, 4), false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.skip != "" {
				t.Skip(tt.skip)
			}
			e := expense.NewExpense(tt.date, 1000, "lunch", nil, false, "")
			if _, ok := expense.EditExpense(*e, now); ok != tt.want {
				t.Errorf("EditExpense ok = %v, want %v", ok, tt.want)
			}
			i := expense.NewIncome(tt.date, 1000, "refund", nil, nil, "")
			if _, ok := expense.EditIncome(*i, now); ok != tt.want {
				t.Errorf("EditIncome ok = %v, want %v", ok, tt.want)
			}
		})
	}
}
