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
	// Under an edit Intent it is pre-filled with the record's canonical entry
	// line, never its verbatim raw_text (ADR-0008).
	Raw string
	// Intent is what the quick-add box is doing: a plain add, adding a payback
	// to a parent expense, or editing an existing record (CONTEXT.md). The form
	// chrome — heading, payback banner, hidden fields, save label — all derive
	// from it, so a render can't disagree with what a save would do.
	Intent expense.Intent
	// FormAction is where the quick-add form posts, derived from Intent by
	// renderHome: "/add", or "/edit/{kind}/{id}" while editing that record.
	FormAction string
	// EditKind is the {kind} of the record being edited ("expense" or
	// "income"), posted as a hidden field so /preview can rebuild the same edit
	// Intent; empty when not editing.
	EditKind string
	// Preview is the live-preview fragment rendered inline for the current Raw,
	// so a no-JS load (and a save-gate rejection) shows the same preview htmx
	// would swap in. It is non-interactive here — the save control stays enabled
	// so a no-JS submit still reaches the server-side gate.
	Preview previewView
	// Groups is the list, newest day first, each with its rows and day total.
	Groups []dayGroup
	// Notice is a one-line message shown above the list — set when a payback's
	// parent expense no longer exists.
	Notice string
	// Shared is true on the Shared tab (derived from Intent): the page lists the
	// shared expenses and settlements, opens with the Balance, and its form posts
	// a hidden tab=shared so /preview and /add know which tab the line belongs to.
	Shared bool
	// TabPath is where the page's own tab lives ("/" or "/shared"): the target of
	// the cancel links and the active tab.
	TabPath string
	// ShowTabs gates the tab navigation: Carla has no ordinary tab to link to.
	ShowTabs bool
	// Balance is who owes whom, shown at the top of the Shared tab.
	Balance balanceView
	// IsJavi is true for Javi: only he can forgive what Carla owes him, or undo
	// that by deleting a Forgiveness (ADR-0016).
	IsJavi bool
	// Today is today's date as the Forgive form's date input wants it
	// ("2026-07-21"): its default and its upper bound.
	Today string
}

// balanceView is the Shared balance as shown: one sentence, and whether the two
// users are even.
type balanceView struct {
	// Text is the whole sentence ("Carla owes Javi €50.00"), also the status
	// announcement; Who and Amount are its two halves, laid out apart.
	Text   string
	Who    string
	Amount string
	Even   bool
	// SettleRaw prefills the entry box ("=50.00") for the user who owes, so
	// settling up is one tap; empty for the creditor and when even.
	SettleRaw string
	// ForgiveAmount prefills the Forgive form ("50.25") for Javi while Carla owes
	// him; empty otherwise, which hides the form (ADR-0016).
	ForgiveAmount string
}

// payerName is a Payer as shown to people.
func payerName(p expense.Payer) string {
	if p == expense.Carla {
		return "Carla"
	}
	return "Javi"
}

// buildBalance words the Shared balance: "Carla owes Javi €50.00", or that the
// two are all square. viewer is who is looking: only the debtor is offered the
// one-tap settle-up.
func buildBalance(b expense.Balance, viewer expense.Payer) balanceView {
	if b.Amount == 0 {
		return balanceView{Text: "All square", Who: "All square", Even: true}
	}
	creditor := expense.Javi
	if b.Debtor == expense.Javi {
		creditor = expense.Carla
	}
	amount := formatEuro(b.Amount)
	who := fmt.Sprintf("%s owes %s", payerName(b.Debtor), payerName(creditor))
	v := balanceView{Text: who + " " + amount, Who: who, Amount: amount}
	if viewer == b.Debtor {
		v.SettleRaw = fmt.Sprintf("=%d.%02d", b.Amount/100, b.Amount%100)
	}
	if viewer == expense.Javi && b.Debtor == expense.Carla {
		v.ForgiveAmount = fmt.Sprintf("%d.%02d", b.Amount/100, b.Amount%100)
	}
	return v
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
	// IsIncome marks any '+' line, standalone or payback (ADR-0009).
	IsIncome bool
	// IsSettlement marks a '=' line: money handed to the other user to settle the
	// Shared balance.
	IsSettlement bool
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
	// Errors are the user-facing save-gate fix messages; empty means ready.
	Errors []string
	// DisableSave disables the save control when the entry is invalid. Set only
	// for interactive (htmx) renders so a no-JS page keeps the control usable and
	// leans on the server-side gate.
	DisableSave bool
	// TabPath is the tab the cancel link returns to ("/" or "/shared").
	TabPath string
	// Editing relabels the save control ("Save changes" vs "Add") and reveals the
	// cancel affordance. It travels on the preview because the save control lives
	// in the swapped-in fragment, so live /preview swaps during an edit must keep
	// the edit-mode label rather than reverting to "Add".
	Editing bool
}

// buildPreview turns a Submission into the preview fragment's view model. When
// interactive (an htmx /preview swap), the save control is disabled for a blank
// or invalid entry; on the inline home render it is left enabled so a no-JS
// submit still reaches the server-side save gate. The Intent the line was
// submitted under carries the rest of the chrome: an edit relabels the save
// control and reveals the cancel affordance, and a payback — added or edited —
// previews a '+' line as the green '−' credit rather than a blue standalone
// income (ADR-0009 amendment, #59/#63).
func buildPreview(raw string, in expense.Intent, sub expense.Submission, interactive bool) previewView {
	editing := in.IsEdit()
	if strings.TrimSpace(raw) == "" {
		return previewView{Empty: true, DisableSave: interactive, Editing: editing, TabPath: tabPath(in)}
	}
	p := sub.Parsed
	account, isDefault := accountDisplay(p.Account)
	v := previewView{
		HasAmount:      p.HasAmount,
		IsIncome:       p.IsIncome,
		IsSettlement:   p.IsSettlement,
		IsPayback:      p.IsIncome && in.IsPayback(),
		DateLabel:      p.Date.Format(dayLabelLayout),
		Description:    p.Description,
		Account:        account,
		AccountDefault: isDefault,
		Errors:         errorMessages(p.Errors, in),
		Editing:        editing,
		TabPath:        tabPath(in),
	}
	if p.HasAmount {
		if v.IsPayback {
			v.Amount = formatCredit(p.Amount)
		} else {
			v.Amount = formatEuro(p.Amount)
		}
	}
	if interactive {
		v.DisableSave = !sub.OK()
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
	// Rel is "Today" or "Yesterday" for those days, else empty.
	Rel string
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
	// PaidBy names who paid a Shared expense ("Carla"); empty on an ordinary one.
	PaidBy string
	// IsSettlement marks a transfer between the two users; Direction words it,
	// e.g. "Javi → Carla". A settlement is an expense row when Javi pays and an
	// income row (IsIncome) when Carla does, so its controls hit the matching
	// kind-qualified endpoints.
	IsSettlement bool
	Direction    string
	// IsForgiveness marks a Forgiveness (ADR-0016): listed in a neutral colour,
	// outside the day total, deletable by Javi and never editable.
	IsForgiveness bool
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
func groupByDay(expenses []expense.Expense, incomes []expense.Income, forgivenesses []expense.Forgiveness, now time.Time) []dayGroup {
	paybacks, standalone := bucketIncomes(incomes)

	feed := make([]feedItem, 0, len(expenses)+len(standalone))
	for _, e := range expenses {
		row := rowView{
			ID:             e.ID,
			Amount:         formatEuro(e.Amount),
			Description:    e.Description,
			Account:        accountLabel(e.Account),
			AccountDefault: e.Account == nil,
			Editable:       expense.Editable(e.Date, now),
		}
		if e.Shared {
			row.PaidBy = payerName(e.PaidBy)
		}
		// Net cost is derived here, never stored (ADR-0001 untouched): the row's
		// contribution to its day total is the full paid amount, less its paybacks.
		cost := e.Amount
		if e.Settlement {
			// A settlement moves money between the users; it is not a cost.
			row.IsSettlement = true
			row.Direction = settlementDirection(expense.Javi)
			cost = 0
		}
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
			net := netCost(e, pbs)
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
		var direction string
		if i.Settlement {
			direction = settlementDirection(expense.Carla)
		}
		feed = append(feed, feedItem{
			date:      i.Date,
			createdAt: i.CreatedAt,
			cost:      0, // a standalone income does not move the day total
			row: rowView{
				ID:           i.ID,
				IsIncome:     true,
				IsSettlement: i.Settlement,
				Direction:    direction,
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

	// A Forgiveness is neither an expense nor an income: it is listed for the
	// record only, with no cost, so it never moves its day's total (ADR-0016).
	for _, f := range forgivenesses {
		feed = append(feed, feedItem{
			date:      f.Date,
			createdAt: f.CreatedAt,
			cost:      0,
			row: rowView{
				ID:            f.ID,
				IsForgiveness: true,
				Amount:        formatEuro(f.Amount),
				Description:   "Javi forgave what Carla owed",
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
			groups = append(groups, dayGroup{Label: it.date.Format(dayLabelLayout), Rel: relativeDay(it.date, now)})
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

// settlementDirection words a settlement by who pays whom: "Javi → Carla".
func settlementDirection(payer expense.Payer) string {
	if payer == expense.Carla {
		return "Carla → Javi"
	}
	return "Javi → Carla"
}

// tabRecords picks what a tab lists from every stored record. The Shared tab
// shows the shared expenses, the settlements (both the expense and income kind),
// and the paybacks of a shared expense; the ordinary tab shows the rest — the
// non-shared, non-settlement expenses and the incomes outside settlements and
// shared paybacks. A payback follows its parent expense.
func tabRecords(expenses []expense.Expense, incomes []expense.Income, shared bool) ([]expense.Expense, []expense.Income) {
	parentShared := make(map[int64]bool, len(expenses))
	var es []expense.Expense
	for _, e := range expenses {
		parentShared[e.ID] = e.Shared
		if inSharedTab(e) == shared {
			es = append(es, e)
		}
	}
	var is []expense.Income
	for _, i := range incomes {
		switch {
		case i.Settlement:
			if shared {
				is = append(is, i)
			}
		case i.LinkedExpenseID != nil:
			if parentShared[*i.LinkedExpenseID] == shared {
				is = append(is, i)
			}
		case !shared:
			is = append(is, i)
		}
	}
	return es, is
}

// inSharedTab reports whether an expense belongs to the Shared tab.
func inSharedTab(e expense.Expense) bool { return e.Shared || e.Settlement }

// bucketIncomes is the in-memory stitch behind "two bulk reads, one stitch"
// (ADR-0009): it buckets the incomes by their linked_expense_id, so each expense
// can attach its paybacks; a nil link is a standalone income, returned apart.
func bucketIncomes(incomes []expense.Income) (paybacks map[int64][]expense.Income, standalone []expense.Income) {
	paybacks = make(map[int64][]expense.Income)
	for _, i := range incomes {
		if i.LinkedExpenseID != nil {
			id := *i.LinkedExpenseID
			paybacks[id] = append(paybacks[id], i)
		} else {
			standalone = append(standalone, i)
		}
	}
	return paybacks, standalone
}

// netCost is an expense's Net cost (CONTEXT.md, ADR-0009): its full Amount less
// the sum of its linked paybacks — derived, never stored. It may be negative
// when the expense is over-repaid.
func netCost(e expense.Expense, paybacks []expense.Income) int {
	net := e.Amount
	for _, p := range paybacks {
		net -= p.Amount
	}
	return net
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

// relativeDay names today and yesterday so the list's newest headers read as
// people say them; older days keep only their date.
func relativeDay(d, now time.Time) string {
	y, m, day := now.Date()
	today := time.Date(y, m, day, 0, 0, 0, 0, d.Location())
	switch {
	case d.Format(dayKeyLayout) == today.Format(dayKeyLayout):
		return "Today"
	case d.Format(dayKeyLayout) == today.AddDate(0, 0, -1).Format(dayKeyLayout):
		return "Yesterday"
	}
	return ""
}
