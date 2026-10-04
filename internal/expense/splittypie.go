package expense

import "strings"

// splittypieMaxDescription is splittypie's quick-add limit on a transaction
// name; a longer one blocks its Add button.
const splittypieMaxDescription = 50

// SplittypieLine renders a Split expense as one splittypie quick-add line —
// "<YYYY-MM-DD> <amount> <description>" (CONTEXT.md: "Splittypie export"). The
// amount is the caller's net cost, formatted like the canonical entry line. The
// date is always the full ISO form: splittypie reads a 3–5 character date as
// month-first in the current year, so only the year-first form survives a year
// boundary and can't be misread as DD/MM. No account or '*' is carried over —
// splittypie would read them as part of the name.
func SplittypieLine(e Expense, net int) string {
	return strings.Join([]string{e.Date.Format("2006-01-02"), formatAmount(net), splittypieDescription(e.Description)}, " ")
}

// splittypieDescription fits a description to splittypie's quick-add: whitespace
// runs collapse to one space (its tokenizer splits on single spaces), then it is
// cut to the first 50 characters — runes, so a multi-byte letter is never split
// — and any trailing space the cut exposes is trimmed. A trailing ".me" (its
// "only me" token) is deliberately left alone.
func splittypieDescription(desc string) string {
	runes := []rune(strings.Join(strings.Fields(desc), " "))
	if len(runes) > splittypieMaxDescription {
		runes = runes[:splittypieMaxDescription]
	}
	return strings.TrimRight(string(runes), " ")
}
