package expense

import "time"

// Intent is what the user is doing with an entry line (CONTEXT.md): adding a new
// record, adding a payback to a specific expense, or editing a specific existing
// expense or income. It decides which kind of record the line may become, so
// every path that turns a line into a record — the live preview, the add, and
// the edit — asks the same question in one place (ADR-0009). Build one only
// through its constructors.
type Intent struct {
	kind intentKind
	// id is the record being edited; zero when adding.
	id int64
	// linkedExpenseID is the expense the resulting income is linked to: the
	// parent when adding a payback, the preserved link when editing one, nil
	// otherwise.
	linkedExpenseID *int64
	// parentDescription names the parent expense when adding a payback.
	parentDescription string
}

// intentKind discriminates the four Intents.
type intentKind int

const (
	intentAdd intentKind = iota
	intentAddPayback
	intentEditExpense
	intentEditIncome
)

// Add is the plain quick-add Intent: either kind is allowed and the leading '+'
// decides (ADR-0009).
func Add() Intent { return Intent{kind: intentAdd} }

// AddPayback is the Intent of logging a payback against parent: the line must be
// an income, and the saved Income is linked to parent out-of-band — the link is
// never expressible in the entry text (ADR-0009 amendment, #62).
func AddPayback(parent Expense) Intent {
	id := parent.ID
	return Intent{kind: intentAddPayback, linkedExpenseID: &id, parentDescription: parent.Description}
}

// EditExpense is the Intent of editing existing as of now. The line must stay an
// expense — an edit keeps the record's kind (ADR-0009 amendment) — and the
// result keeps the record's identity. ok is false when existing is past the
// edit window (see Editable, ADR-0008): no edit Intent exists for it.
func EditExpense(existing Expense, now time.Time) (in Intent, ok bool) {
	if !Editable(existing.Date, now) {
		return Intent{}, false
	}
	return Intent{kind: intentEditExpense, id: existing.ID}, true
}

// EditIncome is the Intent of editing existing — a standalone income or a
// payback — as of now. The line must stay an income (ADR-0009 amendment), and
// the result keeps the record's identity and its parent link, so an edit never
// re-parents a payback (ADR-0009 §5). ok is false past the edit window (see
// Editable, ADR-0008).
func EditIncome(existing Income, now time.Time) (in Intent, ok bool) {
	if !Editable(existing.Date, now) {
		return Intent{}, false
	}
	return Intent{kind: intentEditIncome, id: existing.ID, linkedExpenseID: existing.LinkedExpenseID}, true
}

// IsEdit reports whether the Intent edits an existing record rather than adding
// one.
func (in Intent) IsEdit() bool {
	return in.kind == intentEditExpense || in.kind == intentEditIncome
}

// EditsIncome reports whether the Intent edits an existing income (standalone or
// payback).
func (in Intent) EditsIncome() bool { return in.kind == intentEditIncome }

// AddsPayback reports whether the Intent adds a new payback to a parent expense.
func (in Intent) AddsPayback() bool { return in.kind == intentAddPayback }

// IsPayback reports whether a line submitted under the Intent becomes a payback:
// adding one, or editing an income that is linked to an expense.
func (in Intent) IsPayback() bool { return in.linkedExpenseID != nil }

// ID is the record being edited; zero when adding.
func (in Intent) ID() int64 { return in.id }

// LinkedExpenseID is the expense a payback links to; zero when the Intent makes
// no payback.
func (in Intent) LinkedExpenseID() int64 {
	if in.linkedExpenseID == nil {
		return 0
	}
	return *in.linkedExpenseID
}

// ParentDescription names the parent expense when adding a payback; empty
// otherwise.
func (in Intent) ParentDescription() string { return in.parentDescription }

// Submission is the result of submitting an entry line under an Intent. Parsed
// is always populated, so a preview can show the resolved fields even for a
// rejected line. When OK, exactly one of Expense and Income is set — the
// ready-to-store record; when not OK, both are nil.
type Submission struct {
	Parsed  ParsedEntry
	Expense *Expense
	Income  *Income
}

// OK reports whether the line passed every save gate, i.e. a record is ready to
// store.
func (s Submission) OK() bool { return s.Parsed.OK() }

// Submit parses raw as of today and applies the Intent's kind rules on top of
// Parse's own save gates, returning the record to store or the violations.
func Submit(in Intent, raw string, today time.Time) Submission {
	p := Parse(raw, today)
	switch {
	case in.mustBeIncome() && !p.IsIncome:
		p.Errors = append(p.Errors, ErrMustBeIncome)
	case in.kind == intentEditExpense && p.IsIncome:
		p.Errors = append(p.Errors, ErrMustBeExpense)
	}
	s := Submission{Parsed: p}
	if !p.OK() {
		return s
	}
	account := accountPtr(p.Account)
	if p.IsIncome {
		var linked *int64
		if in.linkedExpenseID != nil {
			id := *in.linkedExpenseID // a fresh copy: the record never aliases the Intent
			linked = &id
		}
		s.Income = NewIncome(p.Date, p.Amount, p.Description, account, linked, raw)
		s.Income.ID = in.id
		return s
	}
	s.Expense = NewExpense(p.Date, p.Amount, p.Description, account, p.Split, raw)
	s.Expense.ID = in.id
	return s
}

// mustBeIncome reports whether the Intent refuses a line that is not an income.
func (in Intent) mustBeIncome() bool {
	return in.kind == intentAddPayback || in.kind == intentEditIncome
}

// accountPtr turns the parser's bare account string into the nullable pointer a
// record holds: nil for a blank account (stored as SQL NULL, never an empty
// string, ADR-0001), else its address.
func accountPtr(account string) *string {
	if account == "" {
		return nil
	}
	return &account
}
