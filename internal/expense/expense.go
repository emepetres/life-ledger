package expense

import (
	"fmt"
	"strings"
	"time"
)

// entry is the unexported base shared by Expense and Income — the fields common
// to both stored records (CONTEXT.md, ADR-0001, ADR-0009). It is embedded, never
// held on its own: callers always work with a whole Expense or Income, so each
// public type stays honest about the one field the other lacks (Split for an
// expense, LinkedExpenseID for an income). Because the base is unexported,
// records are built through NewExpense / NewIncome rather than a composite
// literal.
//
// Field choices follow ADR-0001:
//   - Amount is the positive magnitude in integer minor units (cents) of the
//     single implied currency, EUR — never a float or decimal. For an expense it
//     is the full amount paid; for an income the credit's magnitude.
//   - Date carries only the calendar day; its clock fields are zero.
//   - Description is required (the ErrEmptyDescription save gate).
//   - Account is an optional free-text tag: nil when omitted (stored NULL, never
//     an empty string). "personal" is a display-time default, never written here.
//   - RawText is the verbatim entry line, retained so records can be re-parsed if
//     the syntax evolves.
//   - CreatedAt and UpdatedAt are UTC timestamps owned by the store; UpdatedAt is
//     refreshed on every edit while the identity (ID, CreatedAt) is preserved.
type entry struct {
	ID          int64
	Date        time.Time
	Amount      int
	Description string
	Account     *string
	RawText     string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Expense is a single stored spending record — what one line of the mobile note
// becomes once saved on the server (CONTEXT.md, ADR-0001). It is the persisted
// counterpart of a ParsedEntry with IsIncome false: the parser produces the
// resolved fields, and the store fills in the identity and timestamps. Split is
// a plain boolean set only by the explicit '*' marker; it is independent of
// Account and the full Amount is always stored regardless.
type Expense struct {
	entry
	Split bool
}

// Income is a single stored credit record — a "money in" line marked with the
// leading '+' sigil (ADR-0009). It reuses the whole entry shape but never
// carries a '*' split; instead LinkedExpenseID ties a payback to the expense it
// nets down, nil for a standalone income. The link is set out-of-band, never
// through the entry line, and is preserved across edits.
type Income struct {
	entry
	LinkedExpenseID *int64
}

// NewExpense assembles an Expense from its resolved fields, leaving the
// store-owned identity (ID) and timestamps zero for the store to stamp. It
// exists because entry is unexported — callers outside this package build
// records here rather than with a composite literal.
func NewExpense(date time.Time, amount int, description string, account *string, split bool, rawText string) *Expense {
	return &Expense{
		entry: entry{
			Date:        date,
			Amount:      amount,
			Description: description,
			Account:     account,
			RawText:     rawText,
		},
		Split: split,
	}
}

// NewIncome assembles an Income from its resolved fields, leaving the store-owned
// identity (ID) and timestamps zero. linkedExpenseID is nil for a standalone
// income and set for a payback (ADR-0009).
func NewIncome(date time.Time, amount int, description string, account *string, linkedExpenseID *int64, rawText string) *Income {
	return &Income{
		entry: entry{
			Date:        date,
			Amount:      amount,
			Description: description,
			Account:     account,
			RawText:     rawText,
		},
		LinkedExpenseID: linkedExpenseID,
	}
}

// EntryLine renders an expense back into the free-text entry syntax (ADR-0002) —
// the inverse of Parse. Layout is "<amount> <description> <DD/MM> [@account] [*]";
// it appends '*' when the expense is split, over the shared middle that
// entryLineParts builds. See entry.entryLineParts for the round-trip guarantee.
func (e Expense) EntryLine() string {
	parts := e.entryLineParts(formatAmount(e.Amount))
	if e.Split {
		parts = append(parts, "*")
	}
	return strings.Join(parts, " ")
}

// EntryLine renders an income back into the free-text entry syntax, prepending
// the '+' income sigil to the amount and never emitting a '*' (an income can't
// be split, ADR-0009). Layout is "+<amount> <description> <DD/MM> [@account]",
// sharing entryLineParts with Expense so the common middle can't drift.
func (i Income) EntryLine() string {
	return strings.Join(i.entryLineParts("+"+formatAmount(i.Amount)), " ")
}

// entryLineParts builds the canonical "<amount> <description> <DD/MM> [@account]"
// middle shared by both record types, given the already-formatted amount token
// (bare for an expense, '+'-prefixed for an income). Unlike RawText, which may
// hold whatever the user typed, it always names the date as an absolute "DD/MM"
// (never a relative "-N", never omitted) with no leading zeros, so the line
// re-parses to the same record regardless of when it is parsed — what fills the
// box when editing (CONTEXT.md: "Canonical entry line"), keeping an edit from
// silently shifting the date. This round-trips only while the date is within the
// last year — see Editable, whose cutoff is exactly that boundary.
func (e entry) entryLineParts(amount string) []string {
	parts := []string{amount}
	if e.Description != "" {
		parts = append(parts, e.Description)
	}
	parts = append(parts, fmt.Sprintf("%d/%d", e.Date.Day(), int(e.Date.Month())))
	if e.Account != nil && *e.Account != "" {
		parts = append(parts, "@"+*e.Account)
	}
	return parts
}

// Editable reports whether an expense dated date may be edited as of now. An
// edit re-parses its canonical entry line (see EntryLine), whose year-less
// "DD/MM" date only re-parses back to the same day while that day is strictly
// less than a year old: a date exactly one year ago resolves to this year's
// occurrence (which is at or before now) and so would drift. Editing is capped
// there — editable iff date is strictly after the same calendar day one year
// before today. This cutoff is deliberately the "DD/MM" round-trip boundary
// (ADR-0008) — loosen it and EntryLine starts drifting, which is why the two
// live together.
func Editable(date, now time.Time) bool {
	return dateOnly(date).After(dateOnly(now).AddDate(-1, 0, 0))
}

// formatAmount renders integer minor units (cents) as a bare entry-syntax
// amount: a plain integer for whole euros ("10"), else two decimals with a '.'
// separator ("12.50", "7.60"). No currency token — the entry syntax implies EUR.
func formatAmount(minorUnits int) string {
	if minorUnits%100 == 0 {
		return fmt.Sprintf("%d", minorUnits/100)
	}
	return fmt.Sprintf("%d.%02d", minorUnits/100, minorUnits%100)
}
