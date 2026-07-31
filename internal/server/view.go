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
	// "/edit/{kind}/{id}" while editing that row in place.
	FormAction string
	// Editing is true when the quick-add box is editing an existing record
	// rather than adding a new one. It highlights the form, relabels the save
	// control, and reveals the cancel affordance.
	Editing bool
	// Income is true when the record being edited is an income rather than an
	// expense, so the edit-mode heading names the right kind (ADR-0009). It is
	// meaningful only when Editing is true.
	Income bool
	// Preview is the live-preview fragment rendered inline for the current Raw,
	// so a no-JS load (and a save-gate rejection) shows the same preview htmx
	// would swap in. It is non-interactive here — the save control stays enabled
	// so a no-JS submit still reaches the server-side gate.
	Preview previewView
	// Groups is the list, newest day first, each with its rows and day total.
	Groups []dayGroup
	// Payback is true when the box was opened via a row's "+ payback" action
	// (GET /payback/{expenseID}): it reveals the non-editable payback chip and
	// carries the hidden LinkedExpenseID so the income saved from this box is
	// linked to its parent out-of-band, never through the entry text (ADR-0009).
	Payback bool
	// LinkedExpenseID is the parent expense a payback links to, rendered as a
	// hidden form field; meaningful only when Payback is true.
	LinkedExpenseID int64
	// LinkedExpenseDesc names the parent in the payback chip ("↩ payback → <desc>")
	// so the user sees which expense this credit nets down.
	LinkedExpenseDesc string
}

// previewView is the view model for the live-preview fragment: the resolved
// fields shown as chips, the save-gate state, and whether the save control is
// disabled. It is the single rendering of "what will be stored" reused by the
// inline home render and the htmx /preview swaps.
type previewView struct {
	// Empty is true for a blank entry, shown as an unobtrusive placeholder.
	Empty bool
	// HasAmount gates the amount chip; Amount is "€X.XX" for an expense, a plain
	// "€X.XX" styled blue for a standalone income (IsIncome), or the green
	// credit "−€X.XX" for a payback (IsPayback).
	HasAmount bool
	Amount    string
	// IsIncome marks any '+' line, standalone or payback; an income never shows
	// a split badge regardless of link state (ADR-0009).
	IsIncome bool
	// IsPayback marks a '+' line typed while a payback link is active: the
	// preview colours by link state — a payback stays green with the credit
	// '−', while a standalone income (IsIncome but not IsPayback) previews blue
	// with no '−' (ADR-0009 amendment, #59/#63).
	IsPayback bool
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
// an edit keep the edit-mode chrome. paybackActive is whether a payback link is
// currently active (the hidden linked_expense_id is present): it decides whether
// a '+' line previews as a payback (green, '−') or a standalone income (blue, no
// '−'), mirroring the same distinction GatePayback enforces on save (ADR-0009
// amendment, #59/#63).
func buildPreview(raw string, p expense.ParsedEntry, interactive, editing, paybackActive bool) previewView {
	if strings.TrimSpace(raw) == "" {
		return previewView{Empty: true, DisableSave: interactive, Editing: editing}
	}
	account, isDefault := accountDisplay(p.Account)
	v := previewView{
		HasAmount:      p.HasAmount,
		IsIncome:       p.IsIncome,
		IsPayback:      p.IsIncome && paybackActive,
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
		if v.IsPayback {
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
// discriminated by IsIncome (the html/template idiom, since it has no type
// switch, ADR-0009). Account is always non-blank — an account-less row surfaces
// as "@personal" (AccountDefault true) rather than blank, so the default account
// is visible and consistent everywhere.
type rowView struct {
	// ID is the stored record's identity, threaded through so the row's edit and
	// delete controls can target its kind-qualified endpoints — /edit/income and
	// /delete/income for an income row, the expense twins otherwise (ADR-0009).
	ID int64
	// IsIncome marks a standalone income: it renders blue with no leading '−'
	// and, unlike an expense, carries no split marker and does not move the day
	// total (ADR-0009 amendment, #59/#63). It still exposes edit/delete controls
	// (to its income endpoints). A payback is never a row of its own — it nets
	// its parent expense instead (see Paybacks below) and stays green with '−'.
	IsIncome       bool
	Amount         string // "€X.XX" for an expense; plain "€X.XX" (styled blue) for a standalone income
	Description    string
	Account        string // "@work", or "@personal" when defaulted
	AccountDefault bool
	Split          bool
	// Editable gates the row's Edit link: false for a record too old to edit
	// (its date a year old or older), so the list only offers edits that can't
	// shift the date (ADR-0008). Set for both expense and standalone-income rows.
	Editable bool

	// Net-cost fields (ADR-0009), set only on a fronted expense — one with at
	// least one linked payback (HasPaybacks). Net cost is derived at render time
	// as paid − Σ paybacks and is never stored; the plain Amount above stays the
	// full paid figure. An expense with no paybacks leaves all of these zero and
	// renders exactly as it does today.
	HasPaybacks bool
	// Net is the derived net-cost headline, signed: "€40.00" when positive, or a
	// green credit "−€10.00" when over-repaid (OverRepaid).
	Net string
	// Paid is the full paid amount ("€60.00"), shown struck-through beneath the
	// net headline — the faithful transcript the stored amount still holds.
	Paid string
	// OverRepaid is true when Σ paybacks exceeds paid, so net is negative and
	// renders green (ADR-0009 is ungated — over-repayment is allowed).
	OverRepaid bool
	// PaybackSum is the total credited by the paybacks, as a green "−€X.XX", shown
	// in the "−€X.XX from N paybacks ▾" disclosure summary.
	PaybackSum string
	// PaybackCount is len(Paybacks), carried out so the template can pluralise the
	// summary without a range.
	PaybackCount int
	// Paybacks is the nested breakdown revealed by the disclosure, each a credit
	// with its own description, account, and date (ADR-0009).
	Paybacks []paybackView
}

// paybackView is one linked payback in a fronted expense's disclosure: a credit
// carrying its own amount, description, account, and date — each independent of
// the parent expense (ADR-0009). Account is always non-blank, surfacing as
// "@personal" (AccountDefault true) when the payback named none, exactly like a
// row.
type paybackView struct {
	// ID is the payback income's identity, so its edit/delete controls in the
	// disclosure target the /edit/income and /delete/income endpoints (ADR-0009).
	ID             int64
	Amount         string // the credit magnitude as "−€X.XX"
	Description    string
	Account        string // "@bbva", or "@personal" when defaulted
	AccountDefault bool
	DateLabel      string // the payback's own date, e.g. "Thu 30 Jul"
	// Editable gates the payback's Edit link by the same one-year cutoff as any
	// other record (ADR-0008), on the payback's own date.
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
// standalone income, which renders blue but must not move the day total
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
// a standalone income renders as a blue income row but does not move it, so the
// total keeps meaning "what the day cost me". Linked paybacks (a non-nil
// LinkedExpenseID) are not standalone rows: they are bucketed onto their parent
// expense, where the derived net cost (paid − Σ paybacks) nets the parent's
// contribution to its day total.
func groupByDay(expenses []expense.Expense, incomes []expense.Income, now time.Time) []dayGroup {
	// Two bulk reads, one in-memory stitch (ADR-0009): bucket the incomes by their
	// linked_expense_id, so each expense can attach its paybacks; a nil link is a
	// standalone income that stays a feed row of its own.
	paybacks := make(map[int64][]expense.Income)
	var standalone []expense.Income
	for _, i := range incomes {
		if i.LinkedExpenseID != nil {
			id := *i.LinkedExpenseID
			paybacks[id] = append(paybacks[id], i)
		} else {
			standalone = append(standalone, i)
		}
	}

	feed := make([]feedItem, 0, len(expenses)+len(standalone))
	for _, e := range expenses {
		row := rowView{
			ID:             e.ID,
			Amount:         formatEuro(e.Amount),
			Description:    e.Description,
			Account:        accountLabel(e.Account),
			AccountDefault: e.Account == nil,
			Split:          e.Split,
			Editable:       expense.Editable(e.Date, now),
		}
		// Net cost is derived here, never stored (ADR-0001 untouched): the row's
		// contribution to its day total is the full paid amount, less its paybacks.
		cost := e.Amount
		if pbs := paybacks[e.ID]; len(pbs) > 0 {
			sum := 0
			views := make([]paybackView, 0, len(pbs))
			for _, p := range pbs {
				sum += p.Amount
				views = append(views, paybackView{
					ID:             p.ID,
					Amount:         formatCredit(p.Amount),
					Description:    p.Description,
					Account:        accountLabel(p.Account),
					AccountDefault: p.Account == nil,
					DateLabel:      p.Date.Format(dayLabelLayout),
					Editable:       expense.Editable(p.Date, now),
				})
			}
			net := e.Amount - sum
			cost = net
			row.HasPaybacks = true
			row.Net = formatNet(net)
			row.Paid = formatEuro(e.Amount)
			row.OverRepaid = net < 0
			row.PaybackSum = formatCredit(sum)
			row.PaybackCount = len(pbs)
			row.Paybacks = views
		}
		feed = append(feed, feedItem{
			date:      e.Date,
			createdAt: e.CreatedAt,
			cost:      cost,
			row:       row,
		})
	}
	for _, i := range standalone {
		feed = append(feed, feedItem{
			date:      i.Date,
			createdAt: i.CreatedAt,
			cost:      0, // a standalone income does not move the day total
			row: rowView{
				ID:             i.ID,
				IsIncome:       true,
				// Plain formatEuro, not formatCredit: a standalone income has no leading
				// '−' (ADR-0009 amendment, #59/#63) — the blue colour comes from the
				// .row.income CSS rule, keyed off IsIncome, not from the amount string.
				Amount:         formatEuro(i.Amount),
				Description:    i.Description,
				Account:        accountLabel(i.Account),
				AccountDefault: i.Account == nil,
				Editable:       expense.Editable(i.Date, now),
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
		// formatNet, not formatEuro: an over-repaid expense can net a whole day's
		// cost below zero (ADR-0009 is ungated), which must render as a signed
		// green credit rather than a malformed euro string.
		g.Total = formatNet(curTotal)
	}
	return groups
}

// formatEuro renders integer minor units (cents) as "€X.XX".
func formatEuro(minorUnits int) string {
	return fmt.Sprintf("€%d.%02d", minorUnits/100, minorUnits%100)
}

// formatNet renders a derived net cost, which may be negative when an expense is
// over-repaid (ADR-0009). A non-negative net is a plain "€X.XX"; a negative net
// is a green credit "−€X.XX" of its magnitude, reusing formatCredit so the minus
// glyph and formatting can't drift from the credit chip.
func formatNet(net int) string {
	if net < 0 {
		return formatCredit(-net)
	}
	return formatEuro(net)
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
