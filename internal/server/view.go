package server

import (
	"fmt"

	"github.com/emepetres/life-ledger/internal/expense"
)

// homeView is the view model for the home page template: the quick-add form
// state plus the day-grouped list.
type homeView struct {
	// HTMXSrc is the local URL of the vendored, version-pinned htmx script.
	HTMXSrc string
	// Raw is the submitted line echoed back so a rejected entry stays in the box.
	Raw string
	// Errors are the user-facing save-gate messages for a rejected submission.
	Errors []string
	// Groups is the list, newest day first, each with its rows and day total.
	Groups []dayGroup
}

// dayGroup is one day's worth of rows under a per-day header.
type dayGroup struct {
	// Label is the day header, e.g. "Fri 17 Jul".
	Label string
	// Total is the summed amount for the day, formatted "€X.XX".
	Total string
	// Rows are the day's expenses, most-recently-added first.
	Rows []rowView
}

// rowView is one expense as shown in the list. Account is always non-blank —
// an account-less expense surfaces as "@personal" (AccountDefault true) rather
// than blank, so the default account is visible and consistent everywhere.
type rowView struct {
	Amount         string // "€X.XX"
	Description    string
	Account        string // "@work", or "@personal" when defaulted
	AccountDefault bool
	Split          bool
}

const (
	// dayLabelLayout renders a day header like "Fri 17 Jul".
	dayLabelLayout = "Mon 2 Jan"
	// dayKeyLayout groups rows by calendar day regardless of any clock fields.
	dayKeyLayout = "2006-01-02"
)

// groupByDay turns the store's newest-first expense list into day groups,
// preserving that order (the list arrives ordered by date then id, both
// descending). Rows within a day keep their most-recently-added-first order and
// each group carries the day's summed total.
func groupByDay(expenses []expense.Expense) []dayGroup {
	var groups []dayGroup
	var curKey string
	var curTotal int

	for _, e := range expenses {
		key := e.Date.Format(dayKeyLayout)
		if len(groups) == 0 || key != curKey {
			groups = append(groups, dayGroup{Label: e.Date.Format(dayLabelLayout)})
			curKey = key
			curTotal = 0
		}
		g := &groups[len(groups)-1]
		g.Rows = append(g.Rows, rowView{
			Amount:         formatEuro(e.Amount),
			Description:    e.Description,
			Account:        accountLabel(e.Account),
			AccountDefault: e.Account == nil,
			Split:          e.Split,
		})
		curTotal += e.Amount
		g.Total = formatEuro(curTotal)
	}
	return groups
}

// formatEuro renders integer minor units (cents) as "€X.XX".
func formatEuro(minorUnits int) string {
	return fmt.Sprintf("€%d.%02d", minorUnits/100, minorUnits%100)
}

// accountLabel is the "@tag" display for a row: the stored tag when present, or
// the "@personal" default when the account is blank (never shown blank).
func accountLabel(account *string) string {
	if account == nil {
		return "@personal"
	}
	return "@" + *account
}
