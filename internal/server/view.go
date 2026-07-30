package server

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emepetres/life-ledger/internal/expense"
)

// loginView is the view model for the login page: an optional error message
// shown after a rejected or rate-limited attempt.
type loginView struct {
	// Error is the user-facing message, empty on the first render.
	Error string
}

// homeView is the view model for the home page template: the quick-add form
// state plus the day-grouped list.
type homeView struct {
	// HTMXSrc is the local URL of the vendored, version-pinned htmx script.
	HTMXSrc string
	// Raw is the submitted line echoed back so a rejected entry stays in the box.
	// In edit mode it is pre-filled with the edited row's original raw_text.
	Raw string
	// FormAction is where the quick-add form posts: "/add" normally, or
	// "/edit/{id}" while editing that row in place.
	FormAction string
	// Editing is true when the quick-add box is editing an existing expense
	// rather than adding a new one. It highlights the form, relabels the save
	// control, and reveals the cancel affordance.
	Editing bool
	// Preview is the live-preview fragment rendered inline for the current Raw,
	// so a no-JS load (and a save-gate rejection) shows the same preview htmx
	// would swap in. It is non-interactive here — the save control stays enabled
	// so a no-JS submit still reaches the server-side gate.
	Preview previewView
	// Groups is the list, newest day first, each with its rows and day total.
	Groups []dayGroup
}

// previewView is the view model for the live-preview fragment: the resolved
// fields shown as chips, the save-gate state, and whether the save control is
// disabled. It is the single rendering of "what will be stored" reused by the
// inline home render and the htmx /preview swaps.
type previewView struct {
	// Empty is true for a blank entry, shown as an unobtrusive placeholder.
	Empty bool
	// HasAmount gates the amount chip; Amount is "€X.XX" for an expense and the
	// green credit "−€X.XX" for an income (IsCredit).
	HasAmount bool
	Amount    string
	// IsCredit marks a '+' income line so the amount chip renders green as a
	// credit that subtracts (ADR-0009); an income never shows a split badge.
	IsCredit bool
	// DateLabel is the resolved date as a real date, e.g. "Fri 17 Jul" — never
	// a "-N" offset or a raw token.
	DateLabel string
	// Description is the free text left after amount and markers are removed.
	Description string
	// Account is the "@tag" display, or "@personal" when omitted (AccountDefault).
	Account        string
	AccountDefault bool
	// Split gates the "½ split" badge; true only when a standalone '*' is present.
	Split bool
	// Errors are the user-facing save-gate fix messages; empty means ready.
	Errors []string
	// DisableSave disables the save control when the entry is invalid. Set only
	// for interactive (htmx) renders so a no-JS page keeps the control usable and
	// leans on the server-side gate.
	DisableSave bool
	// Editing relabels the save control ("Save changes" vs "Add") and reveals the
	// cancel affordance. It travels on the preview because the save control lives
	// in the swapped-in fragment, so live /preview swaps during an edit must keep
	// the edit-mode label rather than reverting to "Add".
	Editing bool
}

// buildPreview turns a parsed entry into the preview fragment's view model. When
// interactive (an htmx /preview swap), the save control is disabled for a blank
// or invalid entry; on the inline home render it is left enabled so a no-JS
// submit still reaches the server-side save gate. editing relabels the save
// control and reveals the cancel affordance, carried through so live swaps during
// an edit keep the edit-mode chrome.
func buildPreview(raw string, p expense.ParsedEntry, interactive, editing bool) previewView {
	if strings.TrimSpace(raw) == "" {
		return previewView{Empty: true, DisableSave: interactive, Editing: editing}
	}
	account, isDefault := accountDisplay(p.Account)
	v := previewView{
		HasAmount:      p.HasAmount,
		IsCredit:       p.IsIncome,
		DateLabel:      p.Date.Format(dayLabelLayout),
		Description:    p.Description,
		Account:        account,
		AccountDefault: isDefault,
		// An income is never split, so suppress the badge even if the line carried a
		// stray '*' (which the save gate rejects anyway, ADR-0009).
		Split:   p.Split && !p.IsIncome,
		Errors:  errorMessages(p.Errors),
		Editing: editing,
	}
	if p.HasAmount {
		if p.IsIncome {
			v.Amount = formatCredit(p.Amount)
		} else {
			v.Amount = formatEuro(p.Amount)
		}
	}
	if interactive {
		v.DisableSave = !p.OK()
	}
	return v
}

// accountDefaultLabel is shown wherever an expense has no account, so the
// default account is always visible and consistent rather than blank.
const accountDefaultLabel = "@personal"

// accountDisplay is the "@tag" display for a bare account string: the stored tag
// when present, or the accountDefaultLabel default when blank (never shown
// blank). isDefault flags the default so callers can style it.
func accountDisplay(account string) (label string, isDefault bool) {
	if account == "" {
		return accountDefaultLabel, true
	}
	return "@" + account, false
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

// rowView is one row as shown in the list — an expense or a standalone income,
// discriminated by IsCredit (the html/template idiom, since it has no type
// switch, ADR-0009). Account is always non-blank — an account-less row surfaces
// as "@personal" (AccountDefault true) rather than blank, so the default account
// is visible and consistent everywhere.
type rowView struct {
	// ID is the stored record's identity, threaded through so an expense row's
	// edit and delete controls can target its id-scoped endpoints. (Income rows
	// are display-only in this slice; their kind-qualified controls arrive later.)
	ID int64
	// IsCredit marks a standalone income: it renders as a green "−€X.XX" credit
	// row and, unlike an expense, carries no split marker or edit/delete controls
	// and does not move the day total.
	IsCredit       bool
	Amount         string // "€X.XX" for an expense; "−€X.XX" for a credit
	Description    string
	Account        string // "@work", or "@personal" when defaulted
	AccountDefault bool
	Split          bool
	// Editable gates the row's Edit link: false for an expense too old to edit
	// (its date a year old or older), so the list only offers edits that can't
	// shift the date (ADR-0008). Always false for a credit row.
	Editable bool
}

const (
	// dayLabelLayout renders a day header like "Fri 17 Jul".
	dayLabelLayout = "Mon 2 Jan"
	// dayKeyLayout groups rows by calendar day regardless of any clock fields.
	dayKeyLayout = "2006-01-02"
)

// feedItem is one entry in the interleaved list feed before day grouping: its
// row view plus the sort keys and the day-total contribution. cost is the net
// cost this item adds to its day's total — an expense's amount, and zero for a
// standalone income, which renders green but must not move the day total
// (ADR-0009).
type feedItem struct {
	date      time.Time
	createdAt time.Time
	cost      int
	row       rowView
}

// groupByDay merges the store's newest-first expense and income lists into day
// groups, interleaving standalone incomes among the expenses by their own date
// (ADR-0009). Each list arrives ordered by date then id descending, but id
// sequences are per-table and not comparable across the two, so the merged feed
// is ordered by date then created_at descending — the one recency key both kinds
// share — keeping a day's rows most-recently-added-first across expenses and
// incomes alike. Each group's total is the sum of its expenses' net cost only —
// a standalone income renders as a green credit row but does not move it, so the
// total keeps meaning "what the day cost me". Linked paybacks (a non-nil
// LinkedExpenseID) are not standalone rows and are skipped here; they attach to
// their expense in a later slice.
func groupByDay(expenses []expense.Expense, incomes []expense.Income, now time.Time) []dayGroup {
	feed := make([]feedItem, 0, len(expenses)+len(incomes))
	for _, e := range expenses {
		feed = append(feed, feedItem{
			date:      e.Date,
			createdAt: e.CreatedAt,
			cost:      e.Amount,
			row: rowView{
				ID:             e.ID,
				Amount:         formatEuro(e.Amount),
				Description:    e.Description,
				Account:        accountLabel(e.Account),
				AccountDefault: e.Account == nil,
				Split:          e.Split,
				Editable:       expense.Editable(e.Date, now),
			},
		})
	}
	for _, i := range incomes {
		if i.LinkedExpenseID != nil {
			continue // a payback, attached to its expense in a later slice
		}
		feed = append(feed, feedItem{
			date:      i.Date,
			createdAt: i.CreatedAt,
			cost:      0, // a standalone income does not move the day total
			row: rowView{
				ID:             i.ID,
				IsCredit:       true,
				Amount:         formatCredit(i.Amount),
				Description:    i.Description,
				Account:        accountLabel(i.Account),
				AccountDefault: i.Account == nil,
			},
		})
	}

	// Newest first: date descending, then created_at descending within a day so
	// the two kinds interleave by recency. A stable sort preserves each source
	// list's incoming order for items sharing an exact timestamp.
	sort.SliceStable(feed, func(a, b int) bool {
		if !feed[a].date.Equal(feed[b].date) {
			return feed[a].date.After(feed[b].date)
		}
		return feed[a].createdAt.After(feed[b].createdAt)
	})

	var groups []dayGroup
	var curKey string
	var curTotal int
	for _, it := range feed {
		key := it.date.Format(dayKeyLayout)
		if len(groups) == 0 || key != curKey {
			groups = append(groups, dayGroup{Label: it.date.Format(dayLabelLayout)})
			curKey = key
			curTotal = 0
		}
		g := &groups[len(groups)-1]
		g.Rows = append(g.Rows, it.row)
		curTotal += it.cost
		g.Total = formatEuro(curTotal)
	}
	return groups
}

// formatEuro renders integer minor units (cents) as "€X.XX".
func formatEuro(minorUnits int) string {
	return fmt.Sprintf("€%d.%02d", minorUnits/100, minorUnits%100)
}

// formatCredit renders an income's positive magnitude as a credit "−€X.XX",
// prefixing the U+2212 MINUS SIGN to signal that it subtracts (ADR-0009). The
// glyph is the true minus sign, not a hyphen, matching the credit chip.
func formatCredit(minorUnits int) string {
	return "−" + formatEuro(minorUnits)
}

// accountLabel is the "@tag" display for a row's nullable stored account: the
// stored tag when present, or the accountDefaultLabel default when NULL. It
// shares accountDisplay's rendering so the row and preview never drift.
func accountLabel(account *string) string {
	tag := ""
	if account != nil {
		tag = *account
	}
	label, _ := accountDisplay(tag)
	return label
}
