package server_test

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// AC (#113, ADR-0009 amendment): an edit keeps the record's kind. Saving a line
// with a leading '+' over an expense is refused at 422 — not converted, and not
// silently stored with the '+' dropped — and the expense is left unchanged.
func TestEditExpenseWithIncomeLineRejected(t *testing.T) {
	ts, _, seeded, _ := newEditFixture(t, "30 lunch")
	path := "/edit/expense/" + strconv.FormatInt(seeded.ID, 10)

	resp, body := postForm(t, ts, path, url.Values{"raw": {"+45 lunch 21/7"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST %s status = %d, want 422", path, resp.StatusCode)
	}
	if !strings.Contains(body, "an expense can&#39;t start with") {
		t.Errorf("rejection should explain the kind rule; got:\n%s", body)
	}

	_, home := get(t, ts, "/")
	if !strings.Contains(home, "€30.00") || strings.Contains(home, "€45.00") {
		t.Errorf("the expense must be unchanged after a refused edit; got:\n%s", home)
	}
	if strings.Contains(home, `class="row income"`) {
		t.Errorf("a refused edit must not turn the expense into an income; got:\n%s", home)
	}
}

// AC (#113): saving a line without the leading '+' over an income is refused at
// 422 and the income is left unchanged.
func TestEditIncomeWithoutPlusRejected(t *testing.T) {
	ts := newTestServer(t)
	_, added := postAdd(t, ts, "+40 refund")
	path := "/edit/income/" + strconv.FormatInt(incomeControlID(t, added), 10)

	resp, body := postForm(t, ts, path, url.Values{"raw": {"45 refund 21/7"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST %s status = %d, want 422", path, resp.StatusCode)
	}
	if !strings.Contains(body, "an income must keep its") {
		t.Errorf("rejection should explain the kind rule; got:\n%s", body)
	}

	_, home := get(t, ts, "/")
	if !strings.Contains(home, "€40.00") || strings.Contains(home, "€45.00") {
		t.Errorf("the income must be unchanged after a refused edit; got:\n%s", home)
	}
}

// AC (#113): the live preview runs the same kind rule as the save — while
// editing an expense, a '+' line disables Save and explains why.
func TestPreviewEditingExpenseBlocksIncomeLine(t *testing.T) {
	ts, _, seeded, _ := newEditFixture(t, "30 lunch")

	_, body := postForm(t, ts, "/preview", url.Values{
		"raw":  {"+30 lunch 21/7"},
		"kind": {"expense"},
		"id":   {strconv.FormatInt(seeded.ID, 10)},
	})
	if !strings.Contains(body, "an expense can&#39;t start with") {
		t.Errorf("edit preview should surface the kind rule; got:\n%s", body)
	}
	if !strings.Contains(body, "disabled") {
		t.Errorf("edit preview should disable Save for a kind change; got:\n%s", body)
	}
	if !strings.Contains(body, "Save changes") {
		t.Errorf("edit preview should keep the edit-mode save label; got:\n%s", body)
	}
}

// AC (#113): editing a payback previews it as a payback — the green credit chip
// with a leading '−' — not as a blue standalone income.
func TestPreviewEditingPaybackShowsCreditChip(t *testing.T) {
	ts := newTestServer(t)
	_, added := postAdd(t, ts, "90 team lunch")
	parent := paybackParentID(t, added)
	_, withPb := postForm(t, ts, "/add", url.Values{
		"raw":               {"+30 Bob share"},
		"linked_expense_id": {strconv.FormatInt(parent, 10)},
	})
	pbID := strconv.FormatInt(incomeControlID(t, withPb), 10)

	// The edit form itself renders the inline preview as a payback...
	_, form := get(t, ts, "/edit/income/"+pbID)
	if !strings.Contains(form, "chip amount credit") {
		t.Errorf("payback edit form should preview the green credit chip; got:\n%s", form)
	}

	// ...and so does every live /preview swap while editing it.
	_, body := postForm(t, ts, "/preview", url.Values{
		"raw":  {"+40 Bob share 21/7"},
		"kind": {"income"},
		"id":   {pbID},
	})
	if !strings.Contains(body, "−€40.00") || !strings.Contains(body, "chip amount credit") {
		t.Errorf("payback edit preview should show the green −€40.00 credit chip; got:\n%s", body)
	}
}

// AC (#113): adding a payback to an expense deleted in the meantime (e.g. in
// another tab) is refused with a 409 — storing nothing, neither a payback nor a
// standalone income — and the box reloads in plain add Intent with a note.
func TestAddPaybackToDeletedParentRefused(t *testing.T) {
	ts := newTestServer(t)
	_, added := postAdd(t, ts, "90 team lunch")
	parent := strconv.FormatInt(paybackParentID(t, added), 10)
	postForm(t, ts, "/delete/expense/"+parent, nil)

	resp, body := postForm(t, ts, "/add", url.Values{
		"raw":               {"+30 Bob share"},
		"linked_expense_id": {parent},
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("POST /add status = %d, want 409", resp.StatusCode)
	}
	if !strings.Contains(body, "no longer exists") {
		t.Errorf("refusal should say the expense no longer exists; got:\n%s", body)
	}
	if strings.Contains(body, `name="linked_expense_id"`) {
		t.Errorf("the box should drop back to plain add Intent; got:\n%s", body)
	}
	if strings.Contains(body, "Bob share") && strings.Contains(body, `class="row income"`) {
		t.Errorf("nothing should be stored; got:\n%s", body)
	}
}

// AC (#113): /add only ever creates. Edit-target fields posted to it (as the
// edit form's hidden kind + id would be) are ignored — the line is added as a
// new record, never applied to an existing one.
func TestAddIgnoresEditFields(t *testing.T) {
	ts, _, seeded, _ := newEditFixture(t, "30 lunch")

	resp, body := postForm(t, ts, "/add", url.Values{
		"raw":  {"50 dinner"},
		"kind": {"expense"},
		"id":   {strconv.FormatInt(seeded.ID, 10)},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /add status = %d, want 200 after redirect", resp.StatusCode)
	}
	if !strings.Contains(body, "€30.00") || !strings.Contains(body, "€50.00") {
		t.Errorf("/add should create a second expense and leave the first alone; got:\n%s", body)
	}
}
