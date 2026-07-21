package server_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// postPreview drives the live-preview fragment endpoint the way the quick-add
// box does as the user types, returning the rendered fragment.
func postPreview(t *testing.T, ts *httptest.Server, raw string) (*http.Response, string) {
	t.Helper()
	resp, err := http.PostForm(ts.URL+"/preview", url.Values{"raw": {raw}})
	if err != nil {
		t.Fatalf("POST /preview %q: %v", raw, err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("reading POST /preview %q body: %v", raw, err)
	}
	return resp, string(body)
}

// AC: the preview echoes every resolved field — the resolved date as a real
// date (never the "-N" token), the formatted amount, the description, the
// account pill, and the ½ split badge — for a fully-marked entry.
func TestPreviewEchoesResolvedFields(t *testing.T) {
	ts := newTestServer(t)

	// testToday is 2026-07-21 (Tue); "-1" resolves to 2026-07-20 (Mon).
	resp, body := postPreview(t, ts, "12.50 lunch @work * -1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /preview status = %d, want 200", resp.StatusCode)
	}
	for _, want := range []string{"€12.50", "Mon 20 Jul", "lunch", "@work", "½ split"} {
		if !strings.Contains(body, want) {
			t.Errorf("preview missing %q; got:\n%s", want, body)
		}
	}
	// The resolved date must be shown, never the raw offset token.
	if strings.Contains(body, "-1") {
		t.Errorf("preview should show the resolved date, not the -1 token; got:\n%s", body)
	}
	// A tagged account is not the personal default.
	if strings.Contains(body, "@personal") {
		t.Errorf("preview with @work should not show @personal; got:\n%s", body)
	}
}

// AC: an omitted date defaults to today and an omitted account surfaces as the
// @personal default (never blank).
func TestPreviewDefaultsDateAndPersonalAccount(t *testing.T) {
	ts := newTestServer(t)

	_, body := postPreview(t, ts, "5 coffee")
	if !strings.Contains(body, "Tue 21 Jul") {
		t.Errorf("omitted date should default to today (Tue 21 Jul); got:\n%s", body)
	}
	if !strings.Contains(body, "@personal") {
		t.Errorf("omitted account should show @personal default; got:\n%s", body)
	}
}

// AC: the ½ split badge appears only when a standalone '*' is present.
func TestPreviewSplitBadgeOnlyWhenStar(t *testing.T) {
	ts := newTestServer(t)

	if _, body := postPreview(t, ts, "5 coffee"); strings.Contains(body, "½") {
		t.Errorf("no '*' should mean no ½ badge; got:\n%s", body)
	}
	if _, body := postPreview(t, ts, "5 coffee *"); !strings.Contains(body, "½ split") {
		t.Errorf("a '*' should surface the ½ split badge; got:\n%s", body)
	}
}

// AC: an invalid entry surfaces the fix message and disables the save control
// (the server-side save gate remains the backstop).
func TestPreviewBlocksInvalidEntry(t *testing.T) {
	ts := newTestServer(t)

	_, body := postPreview(t, ts, "lunch @work")
	if !strings.Contains(body, "needs an amount") {
		t.Errorf("invalid entry should surface the fix message; got:\n%s", body)
	}
	if !strings.Contains(body, "disabled") {
		t.Errorf("invalid entry should disable the save control; got:\n%s", body)
	}
}

// AC: a valid entry enables the save control and shows the ready state.
func TestPreviewValidEntryEnablesSave(t *testing.T) {
	ts := newTestServer(t)

	_, body := postPreview(t, ts, "12 lunch")
	if strings.Contains(body, "disabled") {
		t.Errorf("valid entry should leave the save control enabled; got:\n%s", body)
	}
}

// AC: an empty box shows an unobtrusive placeholder and no gate error.
func TestPreviewEmptyShowsPlaceholder(t *testing.T) {
	ts := newTestServer(t)

	_, body := postPreview(t, ts, "")
	if !strings.Contains(body, "live preview") {
		t.Errorf("empty entry should show a placeholder; got:\n%s", body)
	}
	if strings.Contains(body, "needs an amount") {
		t.Errorf("empty entry should not surface save-gate errors; got:\n%s", body)
	}
}

// AC: the quick-add box is wired to the live-preview fragment endpoint via htmx.
func TestHomeWiresPreviewEndpoint(t *testing.T) {
	ts := newTestServer(t)

	_, body := get(t, ts, "/")
	if !strings.Contains(body, `hx-post="/preview"`) {
		t.Errorf("home page should wire the quick-add box to /preview via htmx; got:\n%s", body)
	}
}
