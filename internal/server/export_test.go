package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emepetres/life-ledger/internal/expense"
	"github.com/emepetres/life-ledger/internal/server"
	"github.com/emepetres/life-ledger/internal/store"
)

// day builds a calendar date in July 2026 — the frozen test month (testToday is
// 21 Jul 2026) — or in another month via dayIn.
func day(d int) time.Time { return dayIn(time.July, d) }

func dayIn(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }

// seedExpense stores an expense directly, so export tests can pin exact dates
// and amounts without going through the entry parser. It returns the new id.
func seedExpense(t *testing.T, st *store.Store, date time.Time, cents int, desc string, split bool) int64 {
	t.Helper()
	e := expense.NewExpense(date, cents, desc, nil, split, desc)
	if err := st.Create(context.Background(), e); err != nil {
		t.Fatalf("seeding expense %q: %v", desc, err)
	}
	return e.ID
}

// seedPayback stores an income linked to parent — a Payback (ADR-0009).
func seedPayback(t *testing.T, st *store.Store, date time.Time, cents int, parent int64) {
	t.Helper()
	i := expense.NewIncome(date, cents, "payback", nil, &parent, "+payback")
	if err := st.CreateIncome(context.Background(), i); err != nil {
		t.Fatalf("seeding payback: %v", err)
	}
}

// AC: a Split expense in range downloads as one splittypie quick-add line, as a
// UTF-8 text attachment named after the range.
func TestExportSplitExpenseDownloadsQuickAddLine(t *testing.T) {
	st := openTempStore(t)
	ts := newTestServerWithStore(t, st)
	seedExpense(t, st, day(10), 4050, "museum tickets", true)

	resp, body := get(t, ts, "/export/splittypie?from=2026-07-01")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body:\n%s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", ct)
	}
	wantCD := `attachment; filename="splittypie-2026-07-01_2026-07-21.txt"`
	if cd := resp.Header.Get("Content-Disposition"); cd != wantCD {
		t.Errorf("Content-Disposition = %q, want %q", cd, wantCD)
	}
	if want := "2026-07-10 40.50 museum tickets\n"; body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}

// AC: only Split expenses dated from "from" (inclusive) through today are
// exported, whatever their account; non-split expenses, incomes, the day before
// "from" and future-dated expenses are left out. Lines run oldest first, with
// same-day expenses in entry (id) order.
func TestExportSelectsSplitExpensesInRangeOldestFirst(t *testing.T) {
	st := openTempStore(t)
	ts := newTestServerWithStore(t, st)
	seedExpense(t, st, day(4), 1000, "day before from", true)
	seedExpense(t, st, day(12), 2000, "dinner", true)
	seedExpense(t, st, day(5), 300, "coffee", true) // exactly "from"
	seedExpense(t, st, day(12), 700, "taxi", true)  // same day as dinner, later id
	seedExpense(t, st, day(8), 900, "personal lunch", false)
	seedExpense(t, st, day(21), 1500, "cinema", true) // today
	seedExpense(t, st, day(22), 5000, "future hotel", true)
	work := "work"
	if err := st.Create(context.Background(), expense.NewExpense(day(9), 1200, "work trip", &work, true, "")); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateIncome(context.Background(), expense.NewIncome(day(9), 3000, "salary", nil, nil, "")); err != nil {
		t.Fatal(err)
	}

	_, body := get(t, ts, "/export/splittypie?from=2026-07-05")
	want := "2026-07-05 3 coffee\n" +
		"2026-07-09 12 work trip\n" +
		"2026-07-12 20 dinner\n" +
		"2026-07-12 7 taxi\n" +
		"2026-07-21 15 cinema\n"
	if body != want {
		t.Errorf("body =\n%s\nwant\n%s", body, want)
	}
}

// AC: each line carries the expense's Net cost — its amount less every linked
// payback, whatever the payback's own date — and an expense repaid in full or
// over-repaid (net ≤ 0) is left out.
func TestExportUsesNetCostAndDropsFullyRepaid(t *testing.T) {
	st := openTempStore(t)
	ts := newTestServerWithStore(t, st)
	dinner := seedExpense(t, st, day(10), 8000, "group dinner", true)
	seedPayback(t, st, day(11), 2000, dinner)
	seedPayback(t, st, dayIn(time.September, 1), 1950, dinner) // after the range, still counts
	repaid := seedExpense(t, st, day(10), 3000, "concert tickets", true)
	seedPayback(t, st, day(10), 3000, repaid)
	over := seedExpense(t, st, day(10), 1000, "taxi", true)
	seedPayback(t, st, day(10), 1500, over)

	_, body := get(t, ts, "/export/splittypie?from=2026-07-01")
	if want := "2026-07-10 40.50 group dinner\n"; body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}

// AC: descriptions are made safe for splittypie's quick-add: runs of whitespace
// collapse to one space (its tokenizer splits on single spaces), and anything
// over its 50-character limit is cut to 50 characters — counted as characters,
// never splitting a multi-byte one — with trailing whitespace trimmed.
func TestExportTidiesDescriptionsForSplittypie(t *testing.T) {
	st := openTempStore(t)
	ts := newTestServerWithStore(t, st)
	seedExpense(t, st, day(10), 500, "helados   en  la playa", true)
	// 48 ASCII letters then "íñ" make exactly 50 characters (52 bytes): the cut
	// lands right after two multi-byte runes, which must survive intact.
	seedExpense(t, st, day(11), 600, strings.Repeat("a", 48)+"íñ extra words", true)
	// The 50th character is a space: trimmed after the cut.
	seedExpense(t, st, day(12), 700, strings.Repeat("b", 49)+" tail", true)

	_, body := get(t, ts, "/export/splittypie?from=2026-07-01")
	want := "2026-07-10 5 helados en la playa\n" +
		"2026-07-11 6 " + strings.Repeat("a", 48) + "íñ\n" +
		"2026-07-12 7 " + strings.Repeat("b", 49) + "\n"
	if body != want {
		t.Errorf("body =\n%q\nwant\n%q", body, want)
	}
}

// AC: the home page offers the export as a plain GET form whose "from" date
// defaults to the 1st of the current month.
func TestHomeOffersExportFormDefaultingToFirstOfMonth(t *testing.T) {
	ts := newTestServer(t)
	_, body := get(t, ts, "/")
	for _, want := range []string{
		`action="/export/splittypie"`,
		`type="date"`,
		`name="from"`,
		`value="2026-07-01"`,
		"Export to splittypie",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("home page missing %q; got:\n%s", want, body)
		}
	}
}

// AC: when no expense matches, no file downloads — the home page renders with a
// one-line "nothing to export" notice, the list and quick-add box intact.
func TestExportNothingToExportShowsNoticeOnHome(t *testing.T) {
	st := openTempStore(t)
	ts := newTestServerWithStore(t, st)
	seedExpense(t, st, day(3), 1000, "old split", true)
	seedExpense(t, st, day(10), 1000, "personal", false)

	resp, body := get(t, ts, "/export/splittypie?from=2026-07-05")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		t.Errorf("Content-Disposition = %q, want no attachment", cd)
	}
	for _, want := range []string{"Nothing to export since 2026-07-05.", `name="raw"`, "personal"} {
		if !strings.Contains(body, want) {
			t.Errorf("home page missing %q; got:\n%s", want, body)
		}
	}
	// The export form keeps the chosen day rather than snapping back.
	if !strings.Contains(body, `value="2026-07-05"`) {
		t.Errorf("export form should keep from=2026-07-05; got:\n%s", body)
	}
}

// AC: a missing, unreadable, or future "from" downloads nothing and shows a
// one-line notice on the home page instead of guessing a range.
func TestExportInvalidFromShowsNoticeOnHome(t *testing.T) {
	st := openTempStore(t)
	ts := newTestServerWithStore(t, st)
	seedExpense(t, st, day(10), 1000, "dinner", true)

	for _, q := range []string{"", "?from=", "?from=05/07/2026", "?from=2026-07-22"} {
		resp, body := get(t, ts, "/export/splittypie"+q)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%q: status = %d, want 422", q, resp.StatusCode)
		}
		if cd := resp.Header.Get("Content-Disposition"); cd != "" {
			t.Errorf("%q: Content-Disposition = %q, want no attachment", q, cd)
		}
		if !strings.Contains(body, "Pick a valid &#39;from&#39; date on or before today.") {
			t.Errorf("%q: home page missing the invalid-date notice; got:\n%s", q, body)
		}
	}
}

// AC: the export sits behind the same login as the rest of the app.
func TestExportRequiresLogin(t *testing.T) {
	ts := newGuardedServer(t)
	resp, err := noRedirectClient().Get(ts.URL + "/export/splittypie?from=2026-07-01")
	if err != nil {
		t.Fatalf("GET /export/splittypie: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Errorf("unauth export = %d → %q, want 303 → /login", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// AC: the range is by calendar day even when the server's clock runs in a
// non-UTC zone (production uses Europe/Madrid): "from" may be today, and an
// expense dated today is exported.
func TestExportRangeByCalendarDayInLocalZone(t *testing.T) {
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	st := openTempStore(t)
	h, err := server.New(st, server.WithClock(func() time.Time {
		return time.Date(2026, 7, 21, 10, 30, 0, 0, madrid)
	}))
	if err != nil {
		t.Fatalf("server.New(): %v", err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	seedExpense(t, st, day(21), 1500, "cinema", true)

	resp, body := get(t, ts, "/export/splittypie?from=2026-07-21")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body:\n%s", resp.StatusCode, body)
	}
	if want := "2026-07-21 15 cinema\n"; body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}
