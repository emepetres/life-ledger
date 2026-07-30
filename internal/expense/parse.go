// Package expense holds the domain logic for the expense register. Its first
// citizen is Parse, the single source of parse truth that turns a free-text
// entry line into a ParsedEntry per ADR-0002 (extended by ADR-0009). It is pure
// and I/O-free: the same input plus the same injected "today" always yields the
// same result, so the add, live-preview, and edit paths can all reuse it and be
// tested as a black box.
package expense

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParsedEntry is the resolved result of parsing one entry line — an expense or,
// when the amount is prefixed with '+', an income (ADR-0009). Amount is in
// integer minor units (cents) of the single implied currency, EUR (ADR-0001).
// Date is always populated (today when the line names no date) and carries only
// a calendar day — its clock fields are zero, in the injected today's location.
// Account is the bare tag with its leading '@' stripped, empty when omitted.
// IsIncome flags the leading-'+' income sigil; the server branches on it to
// build the right record. Errors holds the save-gate violations, in a fixed
// order (see ParseError); an empty slice means the line is safe to store.
type ParsedEntry struct {
	Amount      int
	HasAmount   bool
	Description string
	Account     string
	Split       bool
	IsIncome    bool
	Date        time.Time
	Errors      []ParseError
}

// OK reports whether the parse produced no save-gate errors, i.e. the entry can
// be stored as-is.
func (p ParsedEntry) OK() bool { return len(p.Errors) == 0 }

// ParseError enumerates the save-gate violations from ADR-0002 (extended by
// ADR-0009). Parsing is otherwise lenient; these are the only conditions that
// block a save.
type ParseError int

const (
	// ErrNoAmount: the line contains no bare positive number to use as the amount.
	ErrNoAmount ParseError = iota
	// ErrEmptyDescription: nothing is left as description once amount and markers
	// are removed.
	ErrEmptyDescription
	// ErrTwoDateTokens: more than one date token was given (e.g. "-1 17/7").
	ErrTwoDateTokens
	// ErrTwoAccounts: more than one @account token was given.
	ErrTwoAccounts
	// ErrSplitOnIncome: a '*' split marker was given on an income (ADR-0009); an
	// income can never be split.
	ErrSplitOnIncome
)

// parseErrorText holds the terse message for each save-gate violation. The
// user-facing wording lives with the server's presentation layer, not here.
var parseErrorText = map[ParseError]string{
	ErrNoAmount:         "no amount",
	ErrEmptyDescription: "empty description",
	ErrTwoDateTokens:    "two date tokens",
	ErrTwoAccounts:      "two @account tokens",
	ErrSplitOnIncome:    "* not allowed on an income",
}

// Error implements the error interface so a ParseError can be surfaced directly.
func (e ParseError) Error() string {
	if s, ok := parseErrorText[e]; ok {
		return s
	}
	return "unknown parse error"
}

var (
	// A bare number: digits with an optional single decimal group using '.' or
	// ',' (12, 12.5, 12,50). No sign — a leading '-' is a date offset, never an
	// amount — and no thousands separator (ADR-0002).
	bareNumberRe = regexp.MustCompile(`^\d+(?:[.,]\d+)?$`)
	// An income amount: a bare number with a leading '+' immediately before it,
	// e.g. "+30", "+12,50" (ADR-0009). Group 1 is the bare number to parse; the
	// '+' marks the whole entry as an income.
	incomeNumberRe = regexp.MustCompile(`^\+(\d+(?:[.,]\d+)?)$`)
	// Relative date: "-N" days before today.
	relativeDateRe = regexp.MustCompile(`^-(\d+)$`)
	// Absolute date "DD/MM", no leading zeros required.
	dayMonthRe = regexp.MustCompile(`^(\d{1,2})/(\d{1,2})$`)
	// Absolute date "DD/MM/YYYY", overriding the inferred year.
	dayMonthYearRe = regexp.MustCompile(`^(\d{1,2})/(\d{1,2})/(\d{4})$`)
)

// Parse turns a raw entry line into a ParsedEntry, resolving dates relative to
// the injected today so callers control "now" (and tests stay deterministic).
//
// Rules (ADR-0002, extended by ADR-0009): the amount is the first bare positive
// number, with '.'/',' as the decimal separator into minor units; a '+'
// immediately before that first number marks the entry as an income and is
// stripped, while a leading '-' is always a date offset. The @account,
// standalone '*' split, and date token float in any order after the amount;
// whatever text remains is the description. Extra bare numbers stay in the
// description. An omitted date defaults to today. Save-gate errors are collected
// for a missing amount, empty description, two date tokens, two @account tokens,
// or a '*' on an income; everything else parses leniently.
func Parse(raw string, today time.Time) ParsedEntry {
	today = dateOnly(today)

	var (
		p          ParsedEntry
		descTokens []string
		dateTokens []string
		accountN   int
	)

	for _, tok := range strings.Fields(raw) {
		switch {
		case !p.HasAmount && bareNumberRe.MatchString(tok):
			// First bare *positive* number is the amount (ADR-0002); later ones
			// fall through to the description via the default branch. A zero is
			// not positive, so it is left in the description like any other
			// non-amount bare number and the save gate still reports no amount.
			if units := amountMinorUnits(tok); units > 0 {
				p.Amount = units
				p.HasAmount = true
			} else {
				descTokens = append(descTokens, tok)
			}
		case !p.HasAmount && incomeNumberRe.MatchString(tok):
			// A '+' immediately before the first bare number marks an income
			// (ADR-0009): strip it and parse the number as the amount. A
			// non-positive value is left verbatim in the description like a bare
			// zero, and the income marker is not set — there is no income amount.
			num := incomeNumberRe.FindStringSubmatch(tok)[1]
			if units := amountMinorUnits(num); units > 0 {
				p.Amount = units
				p.HasAmount = true
				p.IsIncome = true
			} else {
				descTokens = append(descTokens, tok)
			}
		case isDateToken(tok):
			dateTokens = append(dateTokens, tok)
		case len(tok) > 1 && tok[0] == '@':
			accountN++
			if accountN == 1 {
				p.Account = tok[1:]
			}
		case tok == "*":
			p.Split = true
		default:
			descTokens = append(descTokens, tok)
		}
	}

	// Date: default to today, else resolve the first token; a second is a
	// save-gate error but the first still drives the preview.
	if len(dateTokens) == 0 {
		p.Date = today
	} else {
		p.Date = resolveDate(dateTokens[0], today)
	}

	p.Description = strings.Join(descTokens, " ")

	// Collect save-gate errors in a fixed order so callers and tests see a stable
	// slice regardless of token order in the input.
	if !p.HasAmount {
		p.Errors = append(p.Errors, ErrNoAmount)
	}
	if p.Description == "" {
		p.Errors = append(p.Errors, ErrEmptyDescription)
	}
	if len(dateTokens) > 1 {
		p.Errors = append(p.Errors, ErrTwoDateTokens)
	}
	if accountN > 1 {
		p.Errors = append(p.Errors, ErrTwoAccounts)
	}
	if p.IsIncome && p.Split {
		p.Errors = append(p.Errors, ErrSplitOnIncome)
	}

	return p
}

// isDateToken reports whether tok is any of the three date forms.
func isDateToken(tok string) bool {
	return relativeDateRe.MatchString(tok) ||
		dayMonthRe.MatchString(tok) ||
		dayMonthYearRe.MatchString(tok)
}

// amountMinorUnits converts a bare-number token (already matched by
// bareNumberRe) to integer minor units. The fractional part is normalised to
// two digits: a single digit is scaled up ("7.6" -> 760) and any digits beyond
// the second are rounded to the nearest cent, half rounding up ("12.567" ->
// 1257, "3.999" -> 400) — matching the reference parseEntry's Math.round(...*100).
func amountMinorUnits(tok string) int {
	intPart, fracPart, _ := splitDecimal(tok)
	units, _ := strconv.Atoi(intPart)
	// Pad so at least two fractional digits exist, then read cents from the first
	// two; a padding zero never triggers a round-up.
	frac := fracPart + "00"
	cents, _ := strconv.Atoi(frac[:2])
	total := units*100 + cents
	// Round the dropped sub-cent digits to the nearest cent: a remainder whose
	// leading digit is >= 5 is at or above half, so the cent rounds up.
	if rest := frac[2:]; rest != "" && rest[0] >= '5' {
		total++
	}
	return total
}

// splitDecimal splits a bare number into its integer and fractional parts,
// accepting either decimal separator.
func splitDecimal(tok string) (intPart, fracPart string, hasFrac bool) {
	if i := strings.IndexAny(tok, ".,"); i >= 0 {
		return tok[:i], tok[i+1:], true
	}
	return tok, "", false
}

// resolveDate turns a date token into a calendar day relative to today.
//   - "-N"          -> today minus N days.
//   - "DD/MM/YYYY"  -> that exact day (the only way to name a future or
//     more-than-a-year-old date).
//   - "DD/MM"       -> the most recent occurrence at or before today, rolling
//     back a year when this year's occurrence is still in the future.
//
// The token is assumed to have matched isDateToken.
func resolveDate(tok string, today time.Time) time.Time {
	if m := relativeDateRe.FindStringSubmatch(tok); m != nil {
		n, _ := strconv.Atoi(m[1])
		return today.AddDate(0, 0, -n)
	}
	if m := dayMonthYearRe.FindStringSubmatch(tok); m != nil {
		day, _ := strconv.Atoi(m[1])
		month, _ := strconv.Atoi(m[2])
		year, _ := strconv.Atoi(m[3])
		return time.Date(year, time.Month(month), day, 0, 0, 0, 0, today.Location())
	}
	m := dayMonthRe.FindStringSubmatch(tok)
	day, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	candidate := time.Date(today.Year(), time.Month(month), day, 0, 0, 0, 0, today.Location())
	if candidate.After(today) {
		candidate = candidate.AddDate(-1, 0, 0)
	}
	return candidate
}

// dateOnly strips the clock fields, keeping the calendar day in t's location.
func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
