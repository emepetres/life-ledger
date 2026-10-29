package server_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"
)

// The stylesheet is cached by browsers and by Cloudflare's edge, so every page
// must link it under a URL that changes whenever its content does.
func TestPagesLinkStylesheetWithContentVersion(t *testing.T) {
	pages := map[string]*httptest.Server{
		"/":      newTestServer(t),
		"/login": newGuardedServer(t),
	}

	for path, ts := range pages {
		_, css := get(t, ts, "/static/style.css")
		sum := sha256.Sum256([]byte(css))
		want := `href="/static/style.css?v=` + hex.EncodeToString(sum[:])[:12] + `"`

		resp, body := get(t, ts, path)
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s: status %d", path, resp.StatusCode)
		}
		if !strings.Contains(body, want) {
			t.Errorf("GET %s: stylesheet link missing %s", path, want)
		}
	}
}
