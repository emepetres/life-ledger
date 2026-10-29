package server_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"
)

// Static assets are cached by browsers and by Cloudflare's edge, so every page
// must link them under a URL that changes whenever their content does.
func TestPagesLinkStaticAssetsWithContentVersion(t *testing.T) {
	pages := map[string]*httptest.Server{
		"/":      newTestServer(t),
		"/login": newGuardedServer(t),
	}

	for path, ts := range pages {
		resp, body := get(t, ts, path)
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s: status %d", path, resp.StatusCode)
		}
		for _, asset := range []string{"style.css", "favicon.svg"} {
			_, content := get(t, ts, "/static/"+asset)
			sum := sha256.Sum256([]byte(content))
			want := `href="/static/` + asset + `?v=` + hex.EncodeToString(sum[:])[:12] + `"`
			if !strings.Contains(body, want) {
				t.Errorf("GET %s: link missing %s", path, want)
			}
		}
	}
}
