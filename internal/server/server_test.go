package server_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/emepetres/life-ledger/internal/server"
)

// newTestServer builds the real handler stack the way main does, then wraps it
// in an httptest server so tests drive it as a black box over HTTP.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	h, err := server.New()
	if err != nil {
		t.Fatalf("server.New(): %v", err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, ts *httptest.Server, path string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("reading %s body: %v", path, err)
	}
	return resp, string(body)
}

// AC: GET /health returns 200 without any auth, suitable for a platform probe.
func TestHealthReturns200Unauthenticated(t *testing.T) {
	ts := newTestServer(t)
	resp, _ := get(t, ts, "/health")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /health status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

// AC: Home page renders through html/template.
func TestHomePageRenders(t *testing.T) {
	ts := newTestServer(t)
	resp, body := get(t, ts, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(body, "Life Ledger") {
		t.Errorf("GET / body missing app name; got:\n%s", body)
	}
	// The template must have executed — no unrendered actions left behind.
	if strings.Contains(body, "{{") {
		t.Errorf("GET / body contains unrendered template action:\n%s", body)
	}
}

// AC: htmx is served from a vendored, version-pinned local asset (no CDN).
func TestHomeReferencesVendoredHtmx(t *testing.T) {
	ts := newTestServer(t)
	_, body := get(t, ts, "/")

	// The referenced script must be a local, version-pinned static path.
	re := regexp.MustCompile(`src="(/static/vendor/htmx-\d+\.\d+\.\d+\.min\.js)"`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("home page does not reference a local version-pinned htmx asset; got:\n%s", body)
	}

	// No CDN references anywhere in the page.
	for _, cdn := range []string{"unpkg.com", "cdn.jsdelivr.net", "cdnjs", "//cdn", "https://"} {
		if strings.Contains(body, cdn) {
			t.Errorf("home page references external/CDN resource %q (must be vendored):\n%s", cdn, body)
		}
	}

	// The referenced asset must actually be served, as JavaScript, and be real htmx.
	resp, js := get(t, ts, m[1])
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", m[1], resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("htmx asset Content-Type = %q, want javascript", ct)
	}
	if !strings.Contains(js, "htmx") {
		t.Errorf("served asset at %s does not look like htmx", m[1])
	}
}
