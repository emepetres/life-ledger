package server_test

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// incomeControlID reads a rendered income row's id back out of its
// "/delete/income/{id}" control, so an income edit/delete test can target a real
// record without reaching into the store. The delete control is present on every
// income row (unlike the edit link, which a too-old row omits), so it is the
// reliable place to find the id.
func incomeControlID(t *testing.T, body string) int64 {
	t.Helper()
	const marker = "/delete/income/"
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("no %s{id} control in body; got:\n%s", marker, body)
	}
	rest := body[i+len(marker):]
	j := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
	if j < 0 {
		j = len(rest)
	}
	id, err := strconv.ParseInt(rest[:j], 10, 64)
	if err != nil {
		t.Fatalf("parsing income id %q: %v", rest[:j], err)
	}
	return id
}

// AC: GET /edit/income/{id} loads the income's canonical entry line (leading '+'
// sigil and absolute date) back into the box in income edit mode — a "Save
// changes" control, an "Edit income" heading, and a form posting to the
// kind-qualified income endpoint (ADR-0009).
func TestEditIncomeFormLoadsCanonicalLineInIncomeMode(t *testing.T) {
	ts := newTestServer(t)
	_, added := postAdd(t, ts, "+40 refund @bbva")
	id := incomeControlID(t, added)
	ids := strconv.FormatInt(id, 10)

	resp, body := get(t, ts, "/edit/income/"+ids)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /edit/income/%s status = %d, want 200", ids, resp.StatusCode)
	}
	// The canonical income line: the '+' sigil (attribute-escaped to &#43;), the
	// absolute date "21/7" (testToday), and the account. Match on the escaped form.
	if !strings.Contains(body, `value="&#43;40 refund 21/7 @bbva"`) {
		t.Errorf("edit form should load the canonical income line into the box; got:\n%s", body)
	}
	if !strings.Contains(body, "Save changes") {
		t.Errorf("income edit should label the save control \"Save changes\"; got:\n%s", body)
	}
	if !strings.Contains(body, "Edit income") {
		t.Errorf("income edit heading should name the income kind; got:\n%s", body)
	}
	if !strings.Contains(body, `action="/edit/income/`+ids+`"`) {
		t.Errorf("income edit form should post to /edit/income/%s; got:\n%s", ids, body)
	}
}

// AC: POST /edit/income/{id} updates the income in place and it stays an income —
// the edited fields show blue with no leading '−', and the day total is still
// unmoved by it (ADR-0009, amended #59/#63).
func TestEditIncomeSavePersistsAndStaysBlue(t *testing.T) {
	ts := newTestServer(t)
	postAdd(t, ts, "30 lunch")
	_, added := postAdd(t, ts, "+40 refund")
	id := incomeControlID(t, added)

	resp, body := postForm(t, ts, "/edit/income/"+strconv.FormatInt(id, 10),
		url.Values{"raw": {"+45 tax refund @bbva"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /edit/income status = %d, want 200 after redirect", resp.StatusCode)
	}
	// The edited income shows its new fields, with no leading '−'.
	if !strings.Contains(body, "€45.00") || !strings.Contains(body, "tax refund") {
		t.Errorf("edited income fields should show in the list; got:\n%s", body)
	}
	if strings.Contains(body, "−€45.00") {
		t.Errorf("an edited standalone income must not carry the leading −; got:\n%s", body)
	}
	if !strings.Contains(body, `class="row income"`) {
		t.Errorf("an edited income must stay an income row; got:\n%s", body)
	}
	// The day total still reflects the expense only (€30.00) — the income never
	// moves it, before or after the edit.
	if !strings.Contains(body, "€30.00") {
		t.Errorf("day total should stay €30.00 (income never moves it); got:\n%s", body)
	}
	// The stale amount is gone.
	if strings.Contains(body, "€40.00") {
		t.Errorf("the pre-edit amount should be replaced; got:\n%s", body)
	}
}

// AC: editing a payback (its amount/account) keeps its linked_expense_id — it
// stays nested under its parent expense and re-nets the parent's cost, rather
// than detaching into a standalone credit row (ADR-0009).
func TestEditPaybackKeepsLink(t *testing.T) {
	ts := newTestServer(t)
	_, added := postAdd(t, ts, "90 team lunch @amex")
	parent := paybackParentID(t, added)

	// Log a payback out-of-band, then find its id from the disclosure's controls.
	_, withPb := postForm(t, ts, "/add", url.Values{
		"raw":               {"+30 Bob share @bbva"},
		"linked_expense_id": {strconv.FormatInt(parent, 10)},
	})
	pbID := incomeControlID(t, withPb)

	// Edit the payback: raise its amount. No link field is sent — the store must
	// keep it linked to its parent.
	resp, body := postForm(t, ts, "/edit/income/"+strconv.FormatInt(pbID, 10),
		url.Values{"raw": {"+40 Bob share @bbva"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /edit/income payback status = %d, want 200", resp.StatusCode)
	}

	// Still a payback: nested under the parent (not a standalone income row), and
	// the parent's net cost re-derived as 90 − 40 = €50.00.
	if strings.Contains(body, `class="row income"`) {
		t.Errorf("an edited payback must not detach into a standalone income row; got:\n%s", body)
	}
	if !strings.Contains(body, "from 1 payback") {
		t.Errorf("the edited payback should stay under its parent's disclosure; got:\n%s", body)
	}
	if !strings.Contains(body, "€50.00") {
		t.Errorf("parent net should re-derive to €50.00 after the edit; got:\n%s", body)
	}
	if !strings.Contains(body, "−€40.00") {
		t.Errorf("the edited payback amount −€40.00 should show; got:\n%s", body)
	}
}

// AC: POST /delete/income/{id} removes the income (the credit row is gone) while
// leaving expenses untouched.
func TestDeleteIncomeRemovesRow(t *testing.T) {
	ts := newTestServer(t)
	postAdd(t, ts, "30 lunch")
	_, added := postAdd(t, ts, "+40 refund")
	id := incomeControlID(t, added)

	resp, body := postForm(t, ts, "/delete/income/"+strconv.FormatInt(id, 10), url.Values{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /delete/income status = %d, want 200 after redirect", resp.StatusCode)
	}
	if strings.Contains(body, "refund") || strings.Contains(body, "€40.00") {
		t.Errorf("deleted income should be gone from the list; got:\n%s", body)
	}
	// The expense is untouched.
	if !strings.Contains(body, "lunch") || !strings.Contains(body, "€30.00") {
		t.Errorf("deleting an income must not touch expenses; got:\n%s", body)
	}
}

// AC: the payback disclosure rows expose edit/delete controls wired to the
// kind-qualified income routes (ADR-0009).
func TestPaybackRowWiresIncomeControls(t *testing.T) {
	ts := newTestServer(t)
	_, added := postAdd(t, ts, "90 team lunch @amex")
	parent := paybackParentID(t, added)

	_, body := postForm(t, ts, "/add", url.Values{
		"raw":               {"+30 Bob share @bbva"},
		"linked_expense_id": {strconv.FormatInt(parent, 10)},
	})
	if !strings.Contains(body, "/edit/income/") {
		t.Errorf("payback row should link an edit control to /edit/income/{id}; got:\n%s", body)
	}
	if !strings.Contains(body, "/delete/income/") {
		t.Errorf("payback row should wire a delete control to /delete/income/{id}; got:\n%s", body)
	}
}

// AC: GET /edit/income for an unknown id is a 404; an income too old to edit
// (ADR-0008) is a bare 403 on both the GET form and the POST save, exactly as an
// expense — while its Delete control stays. 404 remains reserved for unknown ids.
func TestEditIncomeGuards404And403(t *testing.T) {
	ts := newTestServer(t)

	// Unknown id → 404.
	if resp, _ := get(t, ts, "/edit/income/999"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /edit/income/999 status = %d, want 404", resp.StatusCode)
	}

	// An income dated 2020 is well over a year old as of testToday (2026-07-21).
	_, added := postAdd(t, ts, "+10 old refund 1/1/2020")
	id := incomeControlID(t, added)
	ids := strconv.FormatInt(id, 10)

	// The list omits its Edit link but keeps the Delete control.
	_, home := get(t, ts, "/")
	if strings.Contains(home, "/edit/income/"+ids) {
		t.Errorf("list should omit the Edit link for a too-old income; got:\n%s", home)
	}
	if !strings.Contains(home, "/delete/income/"+ids) {
		t.Errorf("a too-old income should still be deletable; got:\n%s", home)
	}

	// Both edit endpoints refuse it with a bare 403.
	if resp, _ := get(t, ts, "/edit/income/"+ids); resp.StatusCode != http.StatusForbidden {
		t.Errorf("GET /edit/income for a too-old income status = %d, want 403", resp.StatusCode)
	}
	resp, _ := postForm(t, ts, "/edit/income/"+ids, url.Values{"raw": {"+12 old refund 1/1/2020"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /edit/income for a too-old income status = %d, want 403", resp.StatusCode)
	}
}

// An unrecognised {kind} in an edit or delete URL is a 404 — the routes accept
// only "expense" and "income" (ADR-0009).
func TestUnknownKindIs404(t *testing.T) {
	ts := newTestServer(t)
	if resp, _ := get(t, ts, "/edit/wibble/1"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /edit/wibble/1 status = %d, want 404", resp.StatusCode)
	}
	resp, _ := postForm(t, ts, "/delete/wibble/1", url.Values{})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST /delete/wibble/1 status = %d, want 404", resp.StatusCode)
	}
}

// AC: deleting a fronted expense confirms the paybacks go too — the delete-confirm
// flow (#45) names them, so the owner is told before the cascade (ADR-0009).
func TestDeleteFrontedExpenseConfirmNamesPaybacks(t *testing.T) {
	ts := newTestServer(t)
	_, added := postAdd(t, ts, "90 team lunch @amex")
	parent := paybackParentID(t, added)

	_, body := postForm(t, ts, "/add", url.Values{
		"raw":               {"+30 Bob share @bbva"},
		"linked_expense_id": {strconv.FormatInt(parent, 10)},
	})

	// The parent expense's delete control carries an hx-confirm that names the
	// paybacks going with it.
	marker := `action="/delete/expense/` + strconv.FormatInt(parent, 10) + `"`
	start := strings.Index(body, marker)
	if start == -1 {
		t.Fatalf("no delete control for the fronted expense; got:\n%s", body)
	}
	end := strings.Index(body[start:], "</form>")
	form := body[start : start+end]
	if !strings.Contains(form, "hx-confirm=") {
		t.Fatalf("fronted-expense delete should carry hx-confirm; got:\n%s", form)
	}
	if !strings.Contains(strings.ToLower(form), "payback") {
		t.Errorf("the delete confirm should tell the owner the paybacks go too; got:\n%s", form)
	}
}
