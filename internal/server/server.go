// Package server wires the HTTP surface of Life Ledger: the rendered home page
// with its quick-add form and day-grouped expense list, the add endpoint (with
// server-side save-gate re-validation so a no-JS POST is refused too), the
// vendored static assets, and the unauthenticated health check. It builds a
// plain net/http handler so it can be exercised as a black box in tests and
// booted unchanged by cmd/life-ledger.
package server

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"time"

	"github.com/emepetres/life-ledger/internal/expense"
	"github.com/emepetres/life-ledger/web"
)

func init() {
	// Pin the JavaScript content type so the vendored htmx asset is always served
	// as JavaScript, independent of the host's registry-based MIME mappings
	// (notably inconsistent on Windows dev machines).
	_ = mime.AddExtensionType(".js", "text/javascript; charset=utf-8")
}

// Store is the persistence surface the server needs: list every expense
// (newest-first, for day grouping) and create a new one. It is an interface so
// the HTTP layer depends only on the behaviour it uses and tests can drive it
// against a real temp-file store.
type Store interface {
	List(ctx context.Context) ([]expense.Expense, error)
	Create(ctx context.Context, e *expense.Expense) error
}

// Server holds the wired dependencies shared by all handlers.
type Server struct {
	store   Store
	tmpl    *template.Template
	htmxSrc string
	// now supplies "today" for parsing; injected so date-relative entries and the
	// day grouping are deterministic in tests. Production uses the wall clock.
	now func() time.Time
}

// Option customises a Server at construction time.
type Option func(*Server)

// WithClock injects the clock used to resolve "today" when parsing entries. It
// exists so tests can freeze time and assert on date grouping; production leaves
// it at the default local wall clock.
func WithClock(now func() time.Time) Option {
	return func(s *Server) { s.now = now }
}

// New builds the application's HTTP handler with all routes registered, backed
// by the given store.
func New(store Store, opts ...Option) (http.Handler, error) {
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

	s := &Server{
		store:   store,
		tmpl:    tmpl,
		htmxSrc: htmxSrc,
		now:     time.Now,
	}
	for _, opt := range opts {
		opt(s)
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

	// Live preview: parse the in-progress line and render the fragment htmx swaps
	// into the quick-add box as the user types. Reuses the same parser as /add.
	mux.HandleFunc("POST /preview", s.handlePreview)

	// Add an expense: parse, re-validate the save gate server-side, persist.
	mux.HandleFunc("POST /add", s.handleAdd)

	// Home page. Registered last as the catch-all for "/" so unknown paths 404.
	mux.HandleFunc("GET /{$}", s.handleHome)

	return mux, nil
}

// handleHome renders the full page: the quick-add form and the day-grouped list.
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	s.renderHome(w, r, http.StatusOK, "")
}

// handlePreview renders the live-preview fragment for the in-progress line. It
// runs the same parser as the add path (the single source of parse truth), so
// the preview shows exactly what would be stored, and disables the save control
// on an invalid entry. Always answers 200 — a "bad" entry is a valid preview.
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	raw := r.PostFormValue("raw")
	parsed := expense.Parse(raw, s.now())
	s.renderPartial(w, "preview", buildPreview(raw, parsed, true))
}

// renderHome loads the list, groups it by day, and renders the home page with
// the given status and quick-add form state (echoed raw line + inline preview).
// Both the plain home view and the save-gate rejection render through here, so
// the list/group/render path lives in one place. The inline preview is built
// non-interactively so the save control stays enabled for a no-JS submit.
func (s *Server) renderHome(w http.ResponseWriter, r *http.Request, status int, raw string) {
	expenses, err := s.store.List(r.Context())
	if err != nil {
		log.Printf("listing expenses: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	parsed := expense.Parse(raw, s.now())
	s.render(w, status, homeView{
		HTMXSrc: s.htmxSrc,
		Raw:     raw,
		Preview: buildPreview(raw, parsed, false),
		Groups:  groupByDay(expenses),
	})
}

// handleAdd parses the submitted line, re-validates the save gate on the server
// (so an entry that skipped the client is still refused), and persists a valid
// expense. On success it redirects back to the home page (Post/Redirect/Get) so
// the new row shows without a resubmittable POST in history; on a save-gate
// violation it re-renders the page with the offending text and error messages at
// 422, storing nothing.
func (s *Server) handleAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	raw := r.PostFormValue("raw")

	parsed := expense.Parse(raw, s.now())
	if !parsed.OK() {
		s.renderHome(w, r, http.StatusUnprocessableEntity, raw)
		return
	}

	if err := s.store.Create(r.Context(), newExpense(parsed, raw)); err != nil {
		log.Printf("creating expense: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// newExpense turns a validated parse result into the record to store. The full
// paid Amount is stored regardless of Split (ADR-0001); a blank account is left
// nil so the store writes SQL NULL, and the verbatim line is retained as
// RawText. The store fills in the identity and timestamps.
func newExpense(p expense.ParsedExpense, raw string) *expense.Expense {
	var account *string
	if p.Account != "" {
		a := p.Account
		account = &a
	}
	return &expense.Expense{
		Date:        p.Date,
		Amount:      p.Amount,
		Description: p.Description,
		Split:       p.Split,
		Account:     account,
		RawText:     raw,
	}
}

// render executes the home template into a buffer first so a template error
// yields a clean 500 rather than a partially written body, then writes it with
// the given status.
func (s *Server) render(w http.ResponseWriter, status int, data homeView) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "home.html", data); err != nil {
		log.Printf("rendering home page: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// renderPartial executes a named template (an htmx fragment) into a buffer first
// so a template error yields a clean 500 rather than a partially written body,
// then writes it at 200.
func (s *Server) renderPartial(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("rendering %s fragment: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = buf.WriteTo(w)
}

// errorMessages maps the parser's save-gate violations to the user-facing
// wording surfaced in the form.
func errorMessages(errs []expense.ParseError) []string {
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		switch e {
		case expense.ErrNoAmount:
			msgs = append(msgs, "needs an amount")
		case expense.ErrEmptyDescription:
			msgs = append(msgs, "needs a description")
		case expense.ErrTwoDateTokens:
			msgs = append(msgs, "two dates")
		case expense.ErrTwoAccounts:
			msgs = append(msgs, "two @accounts")
		default:
			msgs = append(msgs, e.Error())
		}
	}
	return msgs
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
