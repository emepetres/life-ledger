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
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/emepetres/life-ledger/internal/auth"
	"github.com/emepetres/life-ledger/internal/expense"
	"github.com/emepetres/life-ledger/internal/store"
	"github.com/emepetres/life-ledger/web"
)

func init() {
	// Pin the JavaScript content type so the vendored htmx asset is always served
	// as JavaScript, independent of the host's registry-based MIME mappings
	// (notably inconsistent on Windows dev machines).
	_ = mime.AddExtensionType(".js", "text/javascript; charset=utf-8")
}

// Store is the persistence surface the server needs: list every expense
// (newest-first, for day grouping), create one, load one back by id to edit,
// update it in place, and delete it. It is an interface so the HTTP layer
// depends only on the behaviour it uses and tests can drive it against a real
// temp-file store. Get, Update, and Delete report store.ErrNotFound for an
// unknown id, which the handlers translate to a 404.
type Store interface {
	List(ctx context.Context) ([]expense.Expense, error)
	Create(ctx context.Context, e *expense.Expense) error
	Get(ctx context.Context, id int64) (expense.Expense, error)
	Update(ctx context.Context, e *expense.Expense) error
	Delete(ctx context.Context, id int64) error
}

// Server holds the wired dependencies shared by all handlers.
type Server struct {
	store   Store
	tmpl    *template.Template
	htmxSrc string
	// now supplies "today" for parsing; injected so date-relative entries and the
	// day grouping are deterministic in tests. Production uses the wall clock.
	now func() time.Time
	// guard enforces authentication when set: it gates the mux with middleware and
	// backs the login/logout routes. Nil leaves the app unguarded, which only the
	// non-auth handler tests do — production always wires a guard (see cmd).
	guard *auth.Guard
}

// Option customises a Server at construction time.
type Option func(*Server)

// WithClock injects the clock used to resolve "today" when parsing entries. It
// exists so tests can freeze time and assert on date grouping; production leaves
// it at the default local wall clock.
func WithClock(now func() time.Time) Option {
	return func(s *Server) { s.now = now }
}

// WithGuard turns on authentication: the returned handler gains a login page and
// logout route, and every other route is gated by the guard's middleware
// (ADR-0004). Production always passes this; it is a functional option so the
// handler tests whose subject is not auth can construct an unguarded server.
func WithGuard(g *auth.Guard) Option {
	return func(s *Server) { s.guard = g }
}

// New builds the application's HTTP handler with all routes registered, backed
// by the given store.
func New(store Store, opts ...Option) (http.Handler, error) {
	tmpl, err := template.ParseFS(web.Templates, "templates/*.html")
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

	// Edit an expense in place: GET loads its raw_text back into the quick-add
	// box (edit mode); POST re-validates the save gate and updates it, keeping the
	// identity and refreshing updated_at. Both reuse the add path's parse +
	// preview + save-gate machinery so editing behaves identically to adding.
	mux.HandleFunc("GET /edit/{id}", s.handleEditForm)
	mux.HandleFunc("POST /edit/{id}", s.handleEditSave)

	// Delete an expense.
	mux.HandleFunc("POST /delete/{id}", s.handleDelete)

	// Home page. Registered last as the catch-all for "/" so unknown paths 404.
	mux.HandleFunc("GET /{$}", s.handleHome)

	if s.guard == nil {
		return mux, nil
	}

	// Auth on: the login page and logout route, plus the guard's middleware gating
	// everything except the login page, static assets, and the health check.
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)

	return s.guard.Middleware(mux, unauthenticatedOK), nil
}

// unauthenticatedOK reports whether a request may bypass the auth middleware: the
// login page (so the user can reach it to authenticate), the static assets, and
// the platform health probe (ADR-0004). Path-based, so it covers GET and POST of
// the login route alike.
func unauthenticatedOK(r *http.Request) bool {
	p := r.URL.Path
	return p == "/login" || p == "/health" || strings.HasPrefix(p, "/static/")
}

// handleHome renders the full page: the quick-add form (in add mode) and the
// day-grouped list.
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	s.renderForm(w, r, http.StatusOK, "", addAction, false)
}

// handleLoginForm renders the login page. An already-authenticated visitor is
// sent home rather than shown the form again.
func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if s.guard.Authenticated(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.renderLogin(w, http.StatusOK, "")
}

// handleLogin verifies the submitted password through the guard (which spends a
// per-IP rate-limit token first, then does the bcrypt compare) and, on success,
// sets the session cookie and redirects home (Post/Redirect/Get). A wrong
// password re-renders the form at 401; too many rapid attempts at 429. The
// password is never echoed back.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch err := s.guard.Login(w, r, r.PostFormValue("password")); {
	case err == nil:
		http.Redirect(w, r, "/", http.StatusSeeOther)
	case errors.Is(err, auth.ErrRateLimited):
		s.renderLogin(w, http.StatusTooManyRequests, "Too many attempts. Wait a minute and try again.")
	default: // auth.ErrInvalidCredentials
		s.renderLogin(w, http.StatusUnauthorized, "Incorrect password.")
	}
}

// handleLogout clears the session cookie and returns to the login page.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.guard.Logout(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// renderLogin renders the login page at the given status, with an optional error
// message shown after a rejected or rate-limited attempt.
func (s *Server) renderLogin(w http.ResponseWriter, status int, errMsg string) {
	s.renderTemplate(w, status, "login.html", loginView{Error: errMsg})
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
	// The quick-add box sends edit=1 while editing (via hx-vals) so a live swap
	// keeps the "Save changes" label and cancel affordance instead of reverting
	// to the add-mode "Add" control on every keystroke.
	editing := r.PostFormValue("edit") != ""
	parsed := expense.Parse(raw, s.now())
	s.renderPartial(w, "preview", buildPreview(raw, parsed, true, editing))
}

// renderForm loads the list, groups it by day, and renders the home page with
// the given status and quick-add form state: the echoed raw line, the inline
// preview, and where the form posts (addAction, or an /edit/{id} action in edit
// mode). Every full-page render — plain home, save-gate rejection, and the edit
// form — flows through here, so the list/group/render path lives in one place.
// The inline preview is built non-interactively so the save control stays
// enabled for a no-JS submit.
func (s *Server) renderForm(w http.ResponseWriter, r *http.Request, status int, raw, formAction string, editing bool) {
	expenses, err := s.store.List(r.Context())
	if err != nil {
		log.Printf("listing expenses: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	parsed := expense.Parse(raw, s.now())
	s.render(w, status, homeView{
		HTMXSrc:    s.htmxSrc,
		Raw:        raw,
		FormAction: formAction,
		Editing:    editing,
		Preview:    buildPreview(raw, parsed, false, editing),
		Groups:     groupByDay(expenses, s.now()),
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
		s.renderForm(w, r, http.StatusUnprocessableEntity, raw, addAction, false)
		return
	}

	if err := s.store.Create(r.Context(), newExpense(parsed, raw)); err != nil {
		log.Printf("creating expense: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// addAction is the quick-add form's action in add mode; edit mode swaps in an
// "/edit/{id}" action built by editAction.
const addAction = "/add"

// editAction is the quick-add form's action when editing the given expense.
func editAction(id int64) string {
	return "/edit/" + strconv.FormatInt(id, 10)
}

// handleEditForm loads the expense's original raw_text back into the quick-add
// box in edit mode, reusing the same inline preview + save gate as adding. An
// unknown or non-numeric id is a 404.
func (s *Server) handleEditForm(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	e, err := s.store.Get(r.Context(), id)
	if respondStoreErr(w, r, "loading expense for edit", id, err) {
		return
	}
	if !expense.Editable(e.Date, s.now()) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// Edit the canonical entry line rendered from the stored fields, not the
	// verbatim raw_text: its date is absolute, so re-parsing on save can't shift
	// it (ADR-0008).
	s.renderForm(w, r, http.StatusOK, e.EntryLine(), editAction(id), true)
}

// handleEditSave re-parses the edited line, re-validates the save gate on the
// server (so an entry that skipped the client is still refused), and updates the
// expense in place — keeping its identity (id, created_at) and refreshing
// updated_at (ADR-0001). On success it redirects home (Post/Redirect/Get); on a
// save-gate violation it re-renders the form in edit mode at 422, changing
// nothing; an unknown id is a 404.
func (s *Server) handleEditSave(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Guard on the stored date, never the submitted line: an expense too old to
	// edit (ADR-0008) is refused even on a direct POST that skipped the list's
	// hidden Edit link.
	existing, err := s.store.Get(r.Context(), id)
	if respondStoreErr(w, r, "loading expense for edit", id, err) {
		return
	}
	if !expense.Editable(existing.Date, s.now()) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	raw := r.PostFormValue("raw")

	parsed := expense.Parse(raw, s.now())
	if !parsed.OK() {
		s.renderForm(w, r, http.StatusUnprocessableEntity, raw, editAction(id), true)
		return
	}

	e := newExpense(parsed, raw)
	e.ID = id
	if respondStoreErr(w, r, "updating expense", id, s.store.Update(r.Context(), e)) {
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleDelete removes the expense, then redirects home (Post/Redirect/Get). An
// unknown id is a 404.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if respondStoreErr(w, r, "deleting expense", id, s.store.Delete(r.Context(), id)) {
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// respondStoreErr maps a store error on the id-scoped operations to an HTTP
// response — store.ErrNotFound to a 404, any other error to a logged 500 — and
// reports whether it wrote one, so the caller returns on true and proceeds on a
// nil error. op names the operation in the 500 log. It gathers the identical
// not-found/500 translation the edit and delete handlers would otherwise repeat.
func respondStoreErr(w http.ResponseWriter, r *http.Request, op string, id int64, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
	default:
		log.Printf("%s %d: %v", op, id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
	return true
}

// parseID reads the {id} path segment as a positive int64, writing a 404 and
// returning ok=false for a missing or non-numeric id so a bad URL never reaches
// the store.
func parseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}

// newExpense turns a validated parse result into the record to store. The full
// paid Amount is stored regardless of Split (ADR-0001); a blank account is left
// nil so the store writes SQL NULL, and the verbatim line is retained as
// RawText. The store fills in the identity and timestamps.
func newExpense(p expense.ParsedEntry, raw string) *expense.Expense {
	var account *string
	if p.Account != "" {
		a := p.Account
		account = &a
	}
	return expense.NewExpense(p.Date, p.Amount, p.Description, account, p.Split, raw)
}

// render executes the home template at the given status.
func (s *Server) render(w http.ResponseWriter, status int, data homeView) {
	s.renderTemplate(w, status, "home.html", data)
}

// renderPartial executes a named template (an htmx fragment) at 200.
func (s *Server) renderPartial(w http.ResponseWriter, name string, data any) {
	s.renderTemplate(w, http.StatusOK, name, data)
}

// renderTemplate executes the named template into a buffer first — so a template
// error yields a clean 500 rather than a partially written body — then writes it
// as HTML with the given status. It is the single write path shared by the full
// page, the login page, and the htmx fragments.
func (s *Server) renderTemplate(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("rendering %s: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// saveGateMessages is the user-facing wording for each save-gate violation,
// kept here in the presentation layer (the parser holds only terse messages).
var saveGateMessages = map[expense.ParseError]string{
	expense.ErrNoAmount:         "needs an amount",
	expense.ErrEmptyDescription: "needs a description",
	expense.ErrTwoDateTokens:    "two dates",
	expense.ErrTwoAccounts:      "two @accounts",
}

// errorMessages maps the parser's save-gate violations to the user-facing
// wording surfaced in the form, falling back to the terse message for any
// unrecognised code.
func errorMessages(errs []expense.ParseError) []string {
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		if m, ok := saveGateMessages[e]; ok {
			msgs = append(msgs, m)
		} else {
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
