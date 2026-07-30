package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emepetres/life-ledger/internal/expense"
	"github.com/emepetres/life-ledger/internal/server"
	"github.com/emepetres/life-ledger/internal/store"
)

// editClock is a controllable clock so an edit test can seed an expense at one
// instant and prove the update advances updated_at to a later one.
type editClock struct{ t time.Time }

func (c *editClock) now() time.Time { return c.t }

// newEditFixture builds the real handler over a store with a controllable clock,
// seeds one expense, and returns the server, the store (to inspect identity and
// timestamps), the seeded expense, and the clock (to advance before an edit).
func newEditFixture(t *testing.T, raw string) (*httptest.Server, *store.Store, expense.Expense, *editClock) {
	t.Helper()
	clk := &editClock{t: time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "data", "expenses.db")
	st, err := store.Open(path, store.WithClock(clk.now))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// Seed through the same parse path the add flow uses, so raw_text and the
	// derived fields are consistent with what an add would have stored.
	parsed := expense.Parse(raw, testToday)
	var account *string
	if parsed.Account != "" {
		a := parsed.Account
		account = &a
	}
	seed := expense.NewExpense(parsed.Date, parsed.Amount, parsed.Description, account, parsed.Split, raw)
	if err := st.Create(context.Background(), seed); err != nil {
		t.Fatalf("seeding expense: %v", err)
	}

	ts := newTestServerWithStore(t, st)
	return ts, st, *seed, clk
}

// postForm posts to path following redirects, returning the final response and
// body — the re-rendered home page after a Post/Redirect/Get.
func postForm(t *testing.T, ts *httptest.Server, path string, form url.Values) (*http.Response, string) {
	t.Helper()
	resp, err := http.PostForm(ts.URL+path, form)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("reading POST %s body: %v", path, err)
	}
	return resp, string(body)
}

// AC: per-row Edit loads the canonical entry line (not the verbatim raw_text)
// back into the quick-add box — its date rendered absolute as "DD/MM" so a later
// re-parse can't shift it (ADR-0008) — with the live preview + save gate active,
// and edit mode signalled (a "Save changes" control and a cancel affordance).
func TestEditFormLoadsCanonicalLineAndShowsEditMode(t *testing.T) {
	ts, _, seed, _ := newEditFixture(t, "12.50 lunch @work")

	resp, body := get(t, ts, "/edit/"+strconv.FormatInt(seed.ID, 10))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /edit/%d status = %d, want 200", seed.ID, resp.StatusCode)
	}

	// The canonical entry line is loaded into the box: the date is now the
	// absolute "21/7" (testToday), where the verbatim line named none.
	if !strings.Contains(body, `value="12.50 lunch 21/7 @work"`) {
		t.Errorf("edit form should load the canonical entry line into the box; got:\n%s", body)
	}
	// The same live-preview path is active (chips reflect the parsed line).
	for _, want := range []string{"€12.50", "lunch", "@work"} {
		if !strings.Contains(body, want) {
			t.Errorf("edit form preview missing %q; got:\n%s", want, body)
		}
	}
	// Edit mode is visible: the save control reads "Save changes" and a cancel
	// affordance backs out to the plain home page.
	if !strings.Contains(body, "Save changes") {
		t.Errorf("edit mode should label the save control \"Save changes\"; got:\n%s", body)
	}
	if !strings.Contains(body, `href="/"`) {
		t.Errorf("edit mode should offer a cancel affordance back to /; got:\n%s", body)
	}
	// The form targets the edit endpoint for this id.
	if !strings.Contains(body, `action="/edit/`+strconv.FormatInt(seed.ID, 10)+`"`) {
		t.Errorf("edit form should post to /edit/%d; got:\n%s", seed.ID, body)
	}
}

// AC: saving an edit keeps the expense's identity (same id, same created_at) but
// refreshes updated_at, and the changed fields persist and show in the list.
func TestEditSaveKeepsIdentityRefreshesUpdatedAt(t *testing.T) {
	ts, st, seed, clk := newEditFixture(t, "10 lunch @work")

	// Advance the store clock so a refreshed updated_at is distinguishable.
	clk.t = seed.UpdatedAt.Add(48 * time.Hour)

	resp, body := postForm(t, ts, "/edit/"+strconv.FormatInt(seed.ID, 10),
		url.Values{"raw": {"12.50 dinner @home"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /edit status = %d, want 200 after redirect", resp.StatusCode)
	}
	// The edited fields now show in the list.
	for _, want := range []string{"€12.50", "dinner", "@home"} {
		if !strings.Contains(body, want) {
			t.Errorf("list after edit missing %q; got:\n%s", want, body)
		}
	}

	got, err := st.Get(context.Background(), seed.ID)
	if err != nil {
		t.Fatalf("Get after edit: %v", err)
	}
	if got.ID != seed.ID {
		t.Errorf("edit changed the id: got %d, want %d", got.ID, seed.ID)
	}
	if !got.CreatedAt.Equal(seed.CreatedAt) {
		t.Errorf("edit changed created_at: got %v, want %v", got.CreatedAt, seed.CreatedAt)
	}
	if !got.UpdatedAt.After(seed.UpdatedAt) {
		t.Errorf("edit did not refresh updated_at: got %v, want after %v", got.UpdatedAt, seed.UpdatedAt)
	}
	if got.Amount != 1250 || got.Description != "dinner" || got.Account == nil || *got.Account != "home" {
		t.Errorf("edited fields not persisted: %+v", got)
	}
}

// AC: an edit that violates the save gate is refused server-side (422), stores
// nothing, and re-renders in edit mode with the offending text echoed back.
func TestEditSaveGateRejects(t *testing.T) {
	ts, st, seed, _ := newEditFixture(t, "10 lunch @work")

	resp, body := postForm(t, ts, "/edit/"+strconv.FormatInt(seed.ID, 10),
		url.Values{"raw": {"lunch @work"}}) // no amount
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("rejected edit status = %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(body, "needs an amount") {
		t.Errorf("rejected edit should surface the fix message; got:\n%s", body)
	}
	// Still in edit mode, offending text echoed back.
	if !strings.Contains(body, "Save changes") || !strings.Contains(body, `value="lunch @work"`) {
		t.Errorf("rejected edit should stay in edit mode with the text echoed; got:\n%s", body)
	}

	// Nothing changed in the store.
	got, err := st.Get(context.Background(), seed.ID)
	if err != nil {
		t.Fatalf("Get after rejected edit: %v", err)
	}
	if got.Amount != seed.Amount || got.Description != seed.Description {
		t.Errorf("a rejected edit must persist nothing; got %+v", got)
	}
}

// AC: per-row Delete removes the expense from the list.
func TestDeleteRemovesRow(t *testing.T) {
	// A description absent from the input placeholder, so finding it in the body
	// means the row itself is still listed, not the placeholder echoing "lunch".
	ts, st, seed, _ := newEditFixture(t, "7 popcorn")

	resp, body := postForm(t, ts, "/delete/"+strconv.FormatInt(seed.ID, 10), url.Values{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /delete status = %d, want 200 after redirect", resp.StatusCode)
	}
	if strings.Contains(body, "popcorn") {
		t.Errorf("deleted expense should be gone from the list; got:\n%s", body)
	}
	if !strings.Contains(body, "No expenses yet") {
		t.Errorf("list should be empty after deleting the only row; got:\n%s", body)
	}

	if _, err := st.Get(context.Background(), seed.ID); err != store.ErrNotFound {
		t.Errorf("delete should remove the row: Get err = %v, want ErrNotFound", err)
	}
}

// AC: the rendered list wires per-row edit and delete controls to their id-scoped
// endpoints (so the buttons are live, not the disabled placeholders).
func TestListWiresPerRowEditAndDelete(t *testing.T) {
	ts, _, seed, _ := newEditFixture(t, "10 lunch @work")

	_, body := get(t, ts, "/")
	id := strconv.FormatInt(seed.ID, 10)
	if !strings.Contains(body, `/edit/`+id) {
		t.Errorf("list should link a per-row edit control to /edit/%s; got:\n%s", id, body)
	}
	if !strings.Contains(body, `/delete/`+id) {
		t.Errorf("list should wire a per-row delete control to /delete/%s; got:\n%s", id, body)
	}
	if strings.Contains(body, "disabled>edit") || strings.Contains(body, "disabled>delete") {
		t.Errorf("edit/delete controls should no longer be disabled placeholders; got:\n%s", body)
	}
}

// AC: the delete control carries a client-side confirmation gate (htmx's
// hx-confirm) naming the action, so a click can't fire the delete request
// without the user confirming first.
func TestDeleteControlHasConfirmation(t *testing.T) {
	ts, _, seed, _ := newEditFixture(t, "10 lunch @work")

	_, body := get(t, ts, "/")
	id := strconv.FormatInt(seed.ID, 10)

	deleteFormStart := strings.Index(body, `action="/delete/`+id+`"`)
	if deleteFormStart == -1 {
		t.Fatalf("delete form for id %s not found; got:\n%s", id, body)
	}
	formEnd := strings.Index(body[deleteFormStart:], "</form>")
	if formEnd == -1 {
		t.Fatalf("unterminated delete form; got:\n%s", body)
	}
	deleteForm := body[deleteFormStart : deleteFormStart+formEnd]

	// hx-confirm only gates a request htmx itself issues; on a plain form that
	// requires hx-boost (or hx-post), so without it hx-confirm would be inert
	// and the browser would submit the delete unconditionally.
	if !strings.Contains(deleteForm, `hx-boost="true"`) {
		t.Errorf("delete form should be hx-boosted, or its hx-confirm never fires; got:\n%s", deleteForm)
	}

	confirmMatch := regexp.MustCompile(`hx-confirm="([^"]*)"`).FindStringSubmatch(deleteForm)
	if confirmMatch == nil {
		t.Fatalf("delete form should carry hx-confirm; got:\n%s", deleteForm)
	}
	confirmMsg := strings.ToLower(confirmMatch[1])
	if !strings.Contains(confirmMsg, "delete") || !strings.Contains(confirmMsg, "expense") {
		t.Errorf("hx-confirm message should unambiguously name deleting an expense; got %q", confirmMatch[1])
	}
}

// A GET /edit for an unknown id is a 404, not a server error or a blank form.
func TestEditFormUnknownID404(t *testing.T) {
	ts := newTestServer(t)
	resp, _ := get(t, ts, "/edit/999")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /edit/999 status = %d, want 404", resp.StatusCode)
	}
}

// The bug this fixes (issue #47): editing an expense whose entry line carried a
// relative date, days after it was entered, must not shift the stored date. The
// edit box now holds the canonical line with an absolute date, so re-parsing on
// save keeps the date put no matter how much later the edit happens (ADR-0008).
func TestEditDoesNotShiftDateAfterTimePasses(t *testing.T) {
	// Enter "12 lunch -3" on 2026-07-21, so it resolves to 2026-07-18.
	clk := &editClock{t: time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "data", "expenses.db")
	st, err := store.Open(path, store.WithClock(clk.now))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	parsed := expense.Parse("12 lunch -3", clk.now())
	seed := expense.NewExpense(parsed.Date, parsed.Amount, parsed.Description, nil, parsed.Split, "12 lunch -3")
	if err := st.Create(context.Background(), seed); err != nil {
		t.Fatalf("seeding expense: %v", err)
	}

	// The server shares the clock, so it can be advanced to "edit day".
	h, err := server.New(st, server.WithClock(clk.now))
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	clk.t = time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC) // edit five days later
	id := strconv.FormatInt(seed.ID, 10)

	// The edit box holds the canonical line: the date is the absolute "18/7" the
	// entry originally resolved to, not the stale "-3".
	_, body := get(t, ts, "/edit/"+id)
	if !strings.Contains(body, `value="12 lunch 18/7"`) {
		t.Fatalf("edit box should hold the canonical line with the original absolute date; got:\n%s", body)
	}

	// Change only the amount and save the canonical line back.
	resp, _ := postForm(t, ts, "/edit/"+id, url.Values{"raw": {"15 lunch 18/7"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /edit status = %d, want 200 after redirect", resp.StatusCode)
	}

	got, err := st.Get(context.Background(), seed.ID)
	if err != nil {
		t.Fatalf("Get after edit: %v", err)
	}
	want := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	if !got.Date.Equal(want) {
		t.Errorf("edit shifted the date: got %s, want %s", got.Date.Format("2006-01-02"), want.Format("2006-01-02"))
	}
	if got.Amount != 1500 {
		t.Errorf("edit did not update the amount: got %d, want 1500", got.Amount)
	}
}

// An expense older than a year can't be edited (ADR-0008): the list omits its
// Edit link and both edit endpoints refuse it with a bare 403, while its Delete
// control stays. 404 remains reserved for an unknown id.
func TestEditGuardsExpensesOlderThanAYear(t *testing.T) {
	// Seeded (against testToday, 2026-07-21) with an absolute 2020 date, so it is
	// well over a year old as of the server's frozen now.
	ts, _, seed, _ := newEditFixture(t, "10 old 1/1/2020")
	id := strconv.FormatInt(seed.ID, 10)

	// The list shows the row but offers no Edit link for it (Delete stays).
	_, body := get(t, ts, "/")
	if strings.Contains(body, `/edit/`+id) {
		t.Errorf("list should omit the Edit link for a too-old expense; got:\n%s", body)
	}
	if !strings.Contains(body, `/delete/`+id) {
		t.Errorf("a too-old expense should still be deletable; got:\n%s", body)
	}

	// The edit endpoints refuse it directly, with a bare 403.
	if resp, _ := get(t, ts, "/edit/"+id); resp.StatusCode != http.StatusForbidden {
		t.Errorf("GET /edit for a too-old expense status = %d, want 403", resp.StatusCode)
	}
	resp, _ := postForm(t, ts, "/edit/"+id, url.Values{"raw": {"12 old 1/1/2020"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /edit for a too-old expense status = %d, want 403", resp.StatusCode)
	}
}
