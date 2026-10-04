package server

import (
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/emepetres/life-ledger/internal/expense"
)

// isoDate is the YYYY-MM-DD layout the export uses for its "from" query value,
// its filename, and each line's date.
const isoDate = "2006-01-02"

// handleSplittypieExport serves the Splittypie export (CONTEXT.md): every Split
// expense dated from the "from" query day through today, one splittypie
// quick-add line each, oldest first, as a downloadable text file. It is
// stateless — nothing about the export is remembered.
func (s *Server) handleSplittypieExport(w http.ResponseWriter, r *http.Request) {
	today := calendarDay(s.now())
	from, err := time.Parse(isoDate, r.URL.Query().Get("from"))
	if err != nil || from.After(today) {
		// Never guess a range: say what's wrong on the home page instead.
		s.renderHome(w, r, http.StatusUnprocessableEntity, homeView{
			FormAction: addAction,
			Notice:     "Pick a valid 'from' date on or before today.",
		})
		return
	}

	expenses, err := s.store.List(r.Context())
	if err != nil {
		log.Printf("listing expenses: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	incomes, err := s.store.ListIncomes(r.Context())
	if err != nil {
		log.Printf("listing incomes: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	paybacks, _ := bucketIncomes(incomes)

	// Each picked expense carries its Net cost: the full amount less every
	// payback linked to it now, whatever the payback's own date. Nothing is left
	// to split once paybacks cover it, so net ≤ 0 is dropped.
	type line struct {
		e   expense.Expense
		net int
	}
	var picked []line
	for _, e := range expenses {
		d := calendarDay(e.Date)
		if !e.Split || d.Before(from) || d.After(today) {
			continue
		}
		if net := netCost(e, paybacks[e.ID]); net > 0 {
			picked = append(picked, line{e, net})
		}
	}
	// Oldest first, same-day expenses in entry order, so the file reads the way
	// things happened. Sorted explicitly rather than relying on the store's order.
	sort.Slice(picked, func(a, b int) bool {
		ea, eb := picked[a].e, picked[b].e
		if !ea.Date.Equal(eb.Date) {
			return ea.Date.Before(eb.Date)
		}
		return ea.ID < eb.ID
	})

	if len(picked) == 0 {
		// No empty download: an empty file reads as "did it work?".
		s.renderHome(w, r, http.StatusOK, homeView{
			FormAction: addAction,
			ExportFrom: from.Format(isoDate),
			Notice:     fmt.Sprintf("Nothing to export since %s.", from.Format(isoDate)),
		})
		return
	}

	var b strings.Builder
	for _, l := range picked {
		b.WriteString(expense.SplittypieLine(l.e, l.net))
		b.WriteByte('\n')
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="splittypie-%s_%s.txt"`,
		from.Format(isoDate), today.Format(isoDate)))
	_, _ = w.Write([]byte(b.String()))
}

// calendarDay is t's calendar day as UTC midnight — the form stored dates and
// the parsed "from" already take — so day comparisons hold whatever zone the
// server clock runs in (production is Europe/Madrid). Comparing local midnight
// against UTC midnight would shift "today" back a day.
func calendarDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// firstOfMonth is the calendar day the Splittypie export form defaults to.
func firstOfMonth(t time.Time) time.Time {
	return calendarDay(t).AddDate(0, 0, 1-t.Day())
}
