package expense

import "time"

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
