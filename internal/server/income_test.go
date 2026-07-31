package server_test

import (
	"net/http"
	"strings"
	"testing"
)

// AC: POST /add with a "+40 …" line and no active link creates a standalone
// income; it renders blue with no leading '−' (ADR-0009 amendment, #59/#63),
// interleaved by date, and the day total is left unchanged by it (ADR-0009).
func TestAddStandaloneIncomeRendersBlueWithoutMovingTotal(t *testing.T) {
	ts := newTestServer(t)

	// An expense first, then a standalone income on the same day (today).
	postAdd(t, ts, "30 lunch")
	resp, body := postAdd(t, ts, "+40 refund")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /add income status = %d, want 200 after redirect", resp.StatusCode)
	}

	// No leading '−': a standalone income is blue, not the payback/net credit
	// green.
	if !strings.Contains(body, "€40.00") {
		t.Errorf("standalone income should render €40.00; got:\n%s", body)
	}
	if strings.Contains(body, "−€40.00") {
		t.Errorf("standalone income must not carry the leading −; got:\n%s", body)
	}
	if !strings.Contains(body, "refund") {
		t.Errorf("income description missing; got:\n%s", body)
	}
	// The row is marked as an income row, styled blue.
	if !strings.Contains(body, `class="row income"`) {
		t.Errorf("income should render an income row; got:\n%s", body)
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

// AC: a standalone-income row exposes edit and delete controls wired to the
// kind-qualified income routes (ADR-0009), and never to the expense routes.
func TestStandaloneIncomeRowWiresKindQualifiedControls(t *testing.T) {
	ts := newTestServer(t)
	_, body := postAdd(t, ts, "+40 refund")

	// The credit row's controls target the income endpoints.
	if !strings.Contains(body, "/edit/income/") {
		t.Errorf("income row should link an edit control to /edit/income/{id}; got:\n%s", body)
	}
	if !strings.Contains(body, "/delete/income/") {
		t.Errorf("income row should wire a delete control to /delete/income/{id}; got:\n%s", body)
	}
	// It is an income, not an expense: no expense-scoped controls on this row.
	if strings.Contains(body, "/edit/expense/") || strings.Contains(body, "/delete/expense/") {
		t.Errorf("a standalone income must not wire expense-scoped controls; got:\n%s", body)
	}
	// A standalone income is not a fronted expense, so it offers no "+ payback".
	if strings.Contains(body, "/payback/") {
		t.Errorf("a standalone income row should offer no + payback control; got:\n%s", body)
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

// AC: the live preview of a "+…" line with no active payback link shows a blue
// "€X.XX" chip with no leading '−', alongside the resolved date/description/
// account chips (ADR-0009 amendment, #59/#63).
func TestPreviewStandaloneIncomeShowsBlueChip(t *testing.T) {
	ts := newTestServer(t)

	// testToday is 2026-07-21 (Tue).
	_, body := postPreview(t, ts, "+30 Bob share @bbva")
	if !strings.Contains(body, "€30.00") {
		t.Errorf("income preview should show €30.00; got:\n%s", body)
	}
	if strings.Contains(body, "−€30.00") {
		t.Errorf("standalone income preview must not carry the leading −; got:\n%s", body)
	}
	if !strings.Contains(body, "chip amount income") {
		t.Errorf("standalone income preview amount chip should carry the income class; got:\n%s", body)
	}
	if strings.Contains(body, "chip amount credit") {
		t.Errorf("standalone income preview must not carry the payback credit class; got:\n%s", body)
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
