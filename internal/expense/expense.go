package expense

import (
	"fmt"
	"strings"
	"time"
)

// Expense is a single stored spending record — what one line of the mobile note
// becomes once saved on the server (CONTEXT.md, ADR-0001). It is the persisted
// counterpart of a ParsedExpense: the parser produces the resolved fields, and
// the store fills in the identity and timestamps.
//
// Field choices follow ADR-0001:
//   - Amount is the full amount paid, held as integer minor units (cents) of the
//     single implied currency, EUR — never a float or decimal.
//   - Date carries only the calendar day of the expense; its clock fields are
//     zero.
//   - Split is a plain boolean set only by the explicit '*' marker; it is
//     independent of Account and the full Amount is always stored regardless.
//   - Account is an optional free-text tag: nil when omitted (stored NULL, never
//     an empty string). "personal" is a display-time default, never written here.
//   - RawText is the verbatim entry line, retained so records can be re-parsed if
//     the syntax evolves.
//   - CreatedAt and UpdatedAt are UTC timestamps owned by the store; UpdatedAt is
//     refreshed on every edit while the identity (ID, CreatedAt) is preserved.
type Expense struct {
	ID          int64
	Date        time.Time
	Amount      int
	Description string
	Split       bool
	Account     *string
	RawText     string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// EntryLine renders an expense back into the free-text entry syntax (ADR-0002) —
// the inverse of Parse. Unlike RawText, which may hold whatever the user typed,
// the canonical entry line always names the date as an absolute "DD/MM" (never a
// relative "-N", never omitted), so it re-parses to the same expense regardless
// of when it is parsed. It is what fills the box when editing (CONTEXT.md:
// "Canonical entry line"), which is what keeps an edit from silently shifting the
// date. Layout is "<amount> <description> <DD/MM> [@account] [*]"; the amount
// drops its fractional part for whole euros and the date uses no leading zeros,
// matching ADR-0002 syntax. This round-trips only while the date is within the
// last year — see Editable, whose cutoff is exactly that boundary.
func (e Expense) EntryLine() string {
	parts := []string{formatAmount(e.Amount)}
	if e.Description != "" {
		parts = append(parts, e.Description)
	}
	parts = append(parts, fmt.Sprintf("%d/%d", e.Date.Day(), int(e.Date.Month())))
	if e.Account != nil && *e.Account != "" {
		parts = append(parts, "@"+*e.Account)
	}
	if e.Split {
		parts = append(parts, "*")
	}
	return strings.Join(parts, " ")
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
