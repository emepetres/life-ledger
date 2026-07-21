package server_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// postAdd submits the quick-add form and follows the Post/Redirect/Get, so the
// returned response is the re-rendered home page (200 after a valid add, or 422
// carrying the save-gate errors when the entry is refused).
func postAdd(t *testing.T, ts *httptest.Server, raw string) (*http.Response, string) {
	t.Helper()
	resp, err := http.PostForm(ts.URL+"/add", url.Values{"raw": {raw}})
	if err != nil {
		t.Fatalf("POST /add %q: %v", raw, err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("reading POST /add %q body: %v", raw, err)
	}
	return resp, string(body)
}

// AC: submitting a valid line persists an Expense and it appears in the list,
// with the account pill and euro-formatted amount.
func TestAddValidLinePersistsAndAppears(t *testing.T) {
	ts := newTestServer(t)

	resp, body := postAdd(t, ts, "12.50 lunch @work")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /add status = %d, want 200 after redirect", resp.StatusCode)
	}
	for _, want := range []string{"€12.50", "lunch", "@work"} {
		if !strings.Contains(body, want) {
			t.Errorf("list after add missing %q; got:\n%s", want, body)
		}
	}

	// It is genuinely persisted: a fresh GET / still shows it.
	_, home := get(t, ts, "/")
	if !strings.Contains(home, "lunch") {
		t.Errorf("GET / after add missing the expense; got:\n%s", home)
	}
}

// AC: a valid add uses Post/Redirect/Get (303 to /) so the POST isn't
// resubmittable from history.
func TestAddValidRedirects(t *testing.T) {
	ts := newTestServer(t)

	// A client that does not follow redirects, so we can inspect the 303 itself.
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.PostForm(ts.URL+"/add", url.Values{"raw": {"5 coffee"}})
	if err != nil {
		t.Fatalf("POST /add: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("POST /add status = %d, want %d (303)", resp.StatusCode, http.StatusSeeOther)
	}
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("POST /add Location = %q, want %q", loc, "/")
	}
}

// AC: the full paid amount is stored regardless of '*' (the half is display/math
// for a later slice; nothing is halved on save).
func TestAddStoresFullAmountRegardlessOfSplit(t *testing.T) {
	ts := newTestServer(t)

	_, body := postAdd(t, ts, "10 dinner *")
	if !strings.Contains(body, "€10.00") {
		t.Errorf("split expense should store the full €10.00; got:\n%s", body)
	}
	if strings.Contains(body, "€5.00") {
		t.Errorf("amount must not be halved on save; got:\n%s", body)
	}
	// The half is surfaced as the split badge/marker.
	if !strings.Contains(body, "½") {
		t.Errorf("split expense should surface a ½ marker; got:\n%s", body)
	}
}

// AC: an account-less row shows "@personal" everywhere, never blank.
func TestAddBlankAccountShowsPersonal(t *testing.T) {
	ts := newTestServer(t)

	_, body := postAdd(t, ts, "5 coffee")
	if !strings.Contains(body, "@personal") {
		t.Errorf("account-less row should show @personal; got:\n%s", body)
	}
}

// AC: the server-side save gate rejects invalid POSTs even without JS, storing
// nothing. Each case covers one of the four save-gate violations.
func TestAddSaveGateRejects(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantMsg string
	}{
		{"no amount", "lunch @work", "needs an amount"},
		{"empty description", "12.50 @work", "needs a description"},
		{"two dates", "12 lunch -1 -2", "two dates"},
		{"two accounts", "12 lunch @a @b", "two @accounts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServer(t)

			resp, body := postAdd(t, ts, tc.raw)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("POST /add %q status = %d, want %d", tc.raw, resp.StatusCode, http.StatusUnprocessableEntity)
			}
			if !strings.Contains(body, tc.wantMsg) {
				t.Errorf("rejection should surface %q; got:\n%s", tc.wantMsg, body)
			}
			// The offending text is echoed back so it isn't lost.
			if !strings.Contains(body, tc.raw) {
				t.Errorf("rejected entry %q should be echoed back into the box; got:\n%s", tc.raw, body)
			}

			// Nothing was stored: the list is still empty.
			_, home := get(t, ts, "/")
			if !strings.Contains(home, "No expenses yet") {
				t.Errorf("a rejected entry must persist nothing; got:\n%s", home)
			}
		})
	}
}

// AC: the list is newest-first, grouped by day, with a per-day header and a
// light day total. Entries dated across two days must land in separate,
// correctly ordered groups with the right totals.
func TestListGroupedByDayNewestFirst(t *testing.T) {
	ts := newTestServer(t)

	// testToday is 2026-07-21 (Tue). Post two on today and one on yesterday.
	postAdd(t, ts, "10 today-a")
	postAdd(t, ts, "20 today-b")
	postAdd(t, ts, "30 yesterday -1")

	_, body := get(t, ts, "/")

	// Two day headers, today's (Tue 21 Jul) before yesterday's (Mon 20 Jul).
	today := strings.Index(body, "Tue 21 Jul")
	yesterday := strings.Index(body, "Mon 20 Jul")
	if today < 0 || yesterday < 0 {
		t.Fatalf("missing day headers; got:\n%s", body)
	}
	if today > yesterday {
		t.Errorf("today's group should come before yesterday's (newest first)")
	}

	// Within today, the most recently added (today-b) comes first.
	if strings.Index(body, "today-b") > strings.Index(body, "today-a") {
		t.Errorf("within a day, most-recently-added should be first")
	}

	// Day totals: today = €30.00 (10 + 20), yesterday = €30.00 (30).
	if !strings.Contains(body, "€30.00") {
		t.Errorf("expected a €30.00 day total; got:\n%s", body)
	}
}

// AC: an empty ledger renders an explicit empty state, not a blank list.
func TestEmptyLedgerShowsEmptyState(t *testing.T) {
	ts := newTestServer(t)
	_, body := get(t, ts, "/")
	if !strings.Contains(body, "No expenses yet") {
		t.Errorf("empty ledger should show an empty state; got:\n%s", body)
	}
}
