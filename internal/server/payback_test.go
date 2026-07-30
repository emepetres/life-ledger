package server_test

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// paybackParentID reads a stored expense's id back out of a rendered home page
// via its per-row "/payback/{id}" link, so a payback test can target a real
// parent without reaching into the store directly.
func paybackParentID(t *testing.T, body string) int64 {
	t.Helper()
	// The per-row action links carry the id, e.g. href="/payback/1".
	i := strings.Index(body, "/payback/")
	if i < 0 {
		t.Fatalf("no /payback/{id} link in body; got:\n%s", body)
	}
	rest := body[i+len("/payback/"):]
	j := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
	if j < 0 {
		j = len(rest)
	}
	id, err := strconv.ParseInt(rest[:j], 10, 64)
	if err != nil {
		t.Fatalf("parsing payback id %q: %v", rest[:j], err)
	}
	return id
}

// AC: GET /payback/{expenseID} primes the quick-add box in income mode with a
// non-editable payback chip naming the parent expense and a hidden
// linked_expense_id, plus a Cancel affordance (ADR-0009).
func TestPaybackStartPrimesIncomeModeWithChipAndHiddenLink(t *testing.T) {
	ts := newTestServer(t)
	_, added := postAdd(t, ts, "90 team lunch @amex")
	id := paybackParentID(t, added)

	resp, body := get(t, ts, "/payback/"+strconv.FormatInt(id, 10))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /payback/%d status = %d, want 200", id, resp.StatusCode)
	}
	// A hidden linked_expense_id carrying the parent id (the link is never typed).
	if !strings.Contains(body, `name="linked_expense_id"`) {
		t.Errorf("payback start should carry a hidden linked_expense_id; got:\n%s", body)
	}
	if !strings.Contains(body, `value="`+strconv.FormatInt(id, 10)+`"`) {
		t.Errorf("hidden link should carry the parent id %d; got:\n%s", id, body)
	}
	// The non-editable chip names the parent expense, with a Cancel back to home.
	if !strings.Contains(body, "payback") || !strings.Contains(body, "team lunch") {
		t.Errorf("payback start should show a chip naming the parent; got:\n%s", body)
	}
	if !strings.Contains(body, `href="/"`) {
		t.Errorf("payback start should offer a Cancel affordance; got:\n%s", body)
	}
	// The box is primed in income mode (the '+' sigil), and still posts to /add.
	if !strings.Contains(body, `action="/add"`) {
		t.Errorf("payback form should post to /add; got:\n%s", body)
	}
}

// AC: GET /payback/{expenseID} for an unknown id is a 404.
func TestPaybackStartUnknownIs404(t *testing.T) {
	ts := newTestServer(t)
	resp, _ := get(t, ts, "/payback/9999")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /payback/9999 status = %d, want 404", resp.StatusCode)
	}
	// A non-numeric id is a 404 too, never reaching the store.
	resp, _ = get(t, ts, "/payback/abc")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /payback/abc status = %d, want 404", resp.StatusCode)
	}
}

// AC: POST /add with a "+…" line and a hidden linked_expense_id creates a
// payback; the parent expense row then shows the reduced net cost as headline
// with the full paid amount struck-through beneath, and the disclosure summary
// counts the payback (ADR-0009).
func TestAddPaybackNetsParentAndShowsBreakdown(t *testing.T) {
	ts := newTestServer(t)
	_, added := postAdd(t, ts, "90 team lunch @amex")
	id := paybackParentID(t, added)

	// Log the payback out-of-band: a '+' income line plus the hidden link.
	resp, body := postForm(t, ts, "/add", url.Values{
		"raw":               {"+30 Bob share @bbva"},
		"linked_expense_id": {strconv.FormatInt(id, 10)},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /add payback status = %d, want 200", resp.StatusCode)
	}

	// Net headline €60.00, with the full €90.00 paid still shown (struck-through).
	if !strings.Contains(body, "€60.00") {
		t.Errorf("parent should show net cost €60.00 as headline; got:\n%s", body)
	}
	if !strings.Contains(body, "€90.00") {
		t.Errorf("parent should still show the full paid €90.00; got:\n%s", body)
	}
	// The disclosure summarises the payback and expands to its detail: amount,
	// description, own account, own date. Match "from 1 payback" so the assertion
	// targets the disclosure summary specifically, not the delete-confirm text.
	if !strings.Contains(body, "from 1 payback") {
		t.Errorf("parent should summarise 'from 1 payback'; got:\n%s", body)
	}
	if !strings.Contains(body, "−€30.00") {
		t.Errorf("breakdown should show the −€30.00 payback; got:\n%s", body)
	}
	for _, want := range []string{"Bob share", "@bbva"} {
		if !strings.Contains(body, want) {
			t.Errorf("breakdown missing %q; got:\n%s", want, body)
		}
	}
	// It is not a standalone credit row: a payback is nested under its parent, not
	// interleaved into the feed.
	if strings.Contains(body, `class="row credit"`) {
		t.Errorf("payback must not render as a standalone credit row; got:\n%s", body)
	}
}

// AC: over-repaying a parent (Σ paybacks > paid) renders a green negative net,
// and the stored expense.amount is never changed by any payback (net is
// display-only).
func TestAddPaybackOverRepaidGreenNetStoredAmountUnchanged(t *testing.T) {
	ts := newTestServer(t)
	_, added := postAdd(t, ts, "50 dinner")
	id := strconv.FormatInt(paybackParentID(t, added), 10)

	postForm(t, ts, "/add", url.Values{"raw": {"+30 Bob"}, "linked_expense_id": {id}})
	_, body := postForm(t, ts, "/add", url.Values{"raw": {"+30 Alice"}, "linked_expense_id": {id}})

	// Net = 5000 − 6000 = −1000, a green credit "−€10.00".
	if !strings.Contains(body, "−€10.00") {
		t.Errorf("over-repaid parent should show green net −€10.00; got:\n%s", body)
	}
	// The stored paid amount is untouched: the full €50.00 is still shown.
	if !strings.Contains(body, "€50.00") {
		t.Errorf("stored paid amount must stay €50.00 (net is display-only); got:\n%s", body)
	}
}
