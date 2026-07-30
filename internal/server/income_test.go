package server_test

import (
	"net/http"
	"strings"
	"testing"
)

// AC: POST /add with a "+40 …" line and no active link creates a standalone
// income; it renders as a green credit row (a "−€40.00" amount) interleaved by
// date, and the day total is left unchanged by it (ADR-0009).
func TestAddStandaloneIncomeRendersCreditWithoutMovingTotal(t *testing.T) {
	ts := newTestServer(t)

	// An expense first, then a standalone income on the same day (today).
	postAdd(t, ts, "30 lunch")
	resp, body := postAdd(t, ts, "+40 refund")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /add income status = %d, want 200 after redirect", resp.StatusCode)
	}

	// The credit renders green with a minus sign, and its description shows.
	if !strings.Contains(body, "−€40.00") {
		t.Errorf("standalone income should render a green −€40.00 credit; got:\n%s", body)
	}
	if !strings.Contains(body, "refund") {
		t.Errorf("income description missing; got:\n%s", body)
	}
	// The row is marked as a credit row.
	if !strings.Contains(body, `class="row credit"`) {
		t.Errorf("income should render a credit row; got:\n%s", body)
	}
	// The day total reflects the expense only (€30.00), unmoved by the income; it
	// is not flipped negative or reduced.
	if !strings.Contains(body, "€30.00") {
		t.Errorf("day total should stay €30.00 (expenses only); got:\n%s", body)
	}
	if strings.Contains(body, "−€10.00") || strings.Contains(body, "€70.00") {
		t.Errorf("standalone income must not move the day total; got:\n%s", body)
	}

	// It is genuinely persisted as an income.
	_, home := get(t, ts, "/")
	if !strings.Contains(home, "refund") {
		t.Errorf("GET / after add missing the income; got:\n%s", home)
	}
}

// AC: an income row is display-only in this slice — no edit or delete controls
// (its kind-qualified routes arrive later, ADR-0009).
func TestStandaloneIncomeRowIsDisplayOnly(t *testing.T) {
	ts := newTestServer(t)
	_, body := postAdd(t, ts, "+40 refund")

	// The credit row carries neither an /edit link nor a /delete form. (Expense
	// rows would; there are none here.)
	if strings.Contains(body, "/edit/") {
		t.Errorf("income row should offer no edit control; got:\n%s", body)
	}
	if strings.Contains(body, "/delete/") {
		t.Errorf("income row should offer no delete control; got:\n%s", body)
	}
}

// AC: POST /add with "+… *" is rejected by the server-side save gate (an income
// can't be split), storing nothing.
func TestAddIncomeWithSplitRejected(t *testing.T) {
	ts := newTestServer(t)

	resp, body := postAdd(t, ts, "+40 refund *")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("POST /add %q status = %d, want 422", "+40 refund *", resp.StatusCode)
	}
	if !strings.Contains(body, "cannot split an income") {
		t.Errorf("rejection should surface the split-on-income message; got:\n%s", body)
	}
	// The offending line is echoed back into the box (the leading '+' is HTML
	// attribute-escaped to &#43;, so match on the rest of the verbatim line).
	if !strings.Contains(body, "40 refund *") {
		t.Errorf("rejected income should be echoed back into the box; got:\n%s", body)
	}
	// Nothing stored: the ledger is still empty.
	_, home := get(t, ts, "/")
	if !strings.Contains(home, "No expenses yet") {
		t.Errorf("a rejected income must persist nothing; got:\n%s", home)
	}
}

// AC: the live preview of a "+…" line shows a green "−€X.XX" credit chip
// alongside the resolved date/description/account chips (ADR-0009).
func TestPreviewIncomeShowsGreenCreditChip(t *testing.T) {
	ts := newTestServer(t)

	// testToday is 2026-07-21 (Tue).
	_, body := postPreview(t, ts, "+30 Bob share @bbva")
	if !strings.Contains(body, "−€30.00") {
		t.Errorf("income preview should show a −€30.00 credit chip; got:\n%s", body)
	}
	if !strings.Contains(body, "credit") {
		t.Errorf("income preview amount chip should carry the credit class; got:\n%s", body)
	}
	// The resolved fields still show, exactly like an expense.
	for _, want := range []string{"Tue 21 Jul", "Bob share", "@bbva"} {
		if !strings.Contains(body, want) {
			t.Errorf("income preview missing %q; got:\n%s", want, body)
		}
	}
	// An income is never split, so no ½ badge even in the preview.
	if strings.Contains(body, "½") {
		t.Errorf("income preview should never show a split badge; got:\n%s", body)
	}
}

// AC: a stray '*' on an income preview surfaces the fix message and suppresses
// the split badge (the badge is an expense concept).
func TestPreviewIncomeWithSplitBlocks(t *testing.T) {
	ts := newTestServer(t)

	_, body := postPreview(t, ts, "+40 refund *")
	if !strings.Contains(body, "cannot split an income") {
		t.Errorf("income-with-'*' preview should surface the fix message; got:\n%s", body)
	}
	if strings.Contains(body, "½") {
		t.Errorf("income preview must not show a split badge; got:\n%s", body)
	}
	if !strings.Contains(body, "disabled") {
		t.Errorf("invalid income should disable the save control; got:\n%s", body)
	}
}
