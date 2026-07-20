// Package server wires the HTTP surface of Life Ledger: the rendered home page,
// the vendored static assets, and the unauthenticated health check. It builds a
// plain net/http handler so it can be exercised as a black box in tests and
// booted unchanged by cmd/life-ledger.
package server

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"mime"
	"net/http"

	"github.com/emepetres/life-ledger/web"
)

func init() {
	// Pin the JavaScript content type so the vendored htmx asset is always served
	// as JavaScript, independent of the host's registry-based MIME mappings
	// (notably inconsistent on Windows dev machines).
	_ = mime.AddExtensionType(".js", "text/javascript; charset=utf-8")
}

// homeData is the view model for the home page template.
type homeData struct {
	// HTMXSrc is the local URL of the vendored, version-pinned htmx script.
	HTMXSrc string
}

// New builds the application's HTTP handler with all routes registered.
func New() (http.Handler, error) {
	tmpl, err := template.ParseFS(web.Templates, "templates/home.html")
	if err != nil {
		return nil, fmt.Errorf("parsing templates: %w", err)
	}

	// The vendored htmx filename is the single source of the pinned version;
	// discover it rather than duplicating the version string.
	htmxSrc, err := vendoredHTMXSrc()
	if err != nil {
		return nil, err
	}

	staticFS, err := fs.Sub(web.Static, "static")
	if err != nil {
		return nil, fmt.Errorf("mounting static assets: %w", err)
	}

	mux := http.NewServeMux()

	// Static assets (including the vendored htmx script). Unauthenticated.
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))

	// Unauthenticated health check for platform probes.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	// Home page. Registered last as the catch-all for "/" so unknown paths 404.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		// Render into a buffer first so a template error yields a clean 500
		// rather than a 200 with a half-written body.
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "home.html", homeData{HTMXSrc: htmxSrc}); err != nil {
			log.Printf("rendering home page: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = buf.WriteTo(w)
	})

	return mux, nil
}

// vendoredHTMXSrc finds the single vendored htmx asset and returns the URL path
// it is served under. It errors if zero or more than one is present, so a
// mis-vendored asset fails fast at startup rather than silently at request time.
func vendoredHTMXSrc() (string, error) {
	matches, err := fs.Glob(web.Static, "static/vendor/htmx-*.min.js")
	if err != nil {
		return "", fmt.Errorf("locating vendored htmx: %w", err)
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("expected exactly one vendored htmx asset, found %d: %v", len(matches), matches)
	}
	// matches[0] is "static/vendor/htmx-x.y.z.min.js"; served under "/static/...".
	return "/" + matches[0], nil
}
