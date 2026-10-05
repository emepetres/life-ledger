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

// Store is the persistence surface the server needs: list every expense and
// every income (both newest-first, for the interleaved day grouping), create
// either, and load / update / delete either kind by id. It is an interface so
// the HTTP layer depends only on the behaviour it uses and tests can drive it
// against a real temp-file store. The Get/Update/Delete pairs report
// store.ErrNotFound for an unknown id, which the kind-qualified edit/delete
// handlers translate to a 404 (ADR-0009).
type Store interface {
	List(ctx context.Context) ([]expense.Expense, error)
	ListIncomes(ctx context.Context) ([]expense.Income, error)
	Create(ctx context.Context, e *expense.Expense) error
	CreateIncome(ctx context.Context, i *expense.Income) error
	Get(ctx context.Context, id int64) (expense.Expense, error)
	GetIncome(ctx context.Context, id int64) (expense.Income, error)
	Update(ctx context.Context, e *expense.Expense) error
	UpdateIncome(ctx context.Context, i *expense.Income) error
	Delete(ctx context.Context, id int64) error
	DeleteIncome(ctx context.Context, id int64) error
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

	// Edit a record in place, kind-qualified for {expense, income} (ADR-0009):
	// GET loads its canonical entry line back into the quick-add box (edit mode);
	// POST re-validates the save gate and updates it, keeping the identity and
	// refreshing updated_at. Both reuse the add path's parse + preview + save-gate
	// machinery so editing behaves identically to adding, switching only the store
	// method by kind. An unknown kind is a 404.
	mux.HandleFunc("GET /edit/{kind}/{id}", s.handleEditForm)
	mux.HandleFunc("POST /edit/{kind}/{id}", s.handleEditSave)

	// Delete a record, kind-qualified for {expense, income}. Deleting a fronted
	// expense cascade-deletes its paybacks at the schema level (ADR-0009).
	mux.HandleFunc("POST /delete/{kind}/{id}", s.handleDelete)

	// Start a payback: prime the quick-add box in income mode, pre-linked to this
	// expense (the link is set out-of-band, never typed). A 404 for an unknown id.
	mux.HandleFunc("GET /payback/{id}", s.handlePaybackStart)

	// Splittypie export: download the Split expenses from a chosen day through
	// today as splittypie quick-add lines. A plain GET so it works without JS.
	mux.HandleFunc("GET /export/splittypie", s.handleSplittypieExport)

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
	s.renderHome(w, r, http.StatusOK, homeView{Intent: expense.Add()})
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
// submits the line under the same Intent the save would use — the box's edit
// target (kind + id) or payback parent (linked_expense_id) ride along as hidden
// fields of the same form, which htmx includes automatically — so the preview
// shows exactly what would be stored and disables the save control on any
// save-gate violation, kind rules included. Always answers 200 — a "bad" entry
// is a valid preview; an Intent that can no longer be built (a vanished or
// too-old record, a deleted payback parent) previews under the plain add Intent
// and leaves the refusal to the save.
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	raw := r.PostFormValue("raw")
	in, err := s.previewIntent(r)
	if err != nil {
		in = expense.Add()
	}
	sub := expense.Submit(in, raw, s.now())
	s.renderPartial(w, "preview", buildPreview(raw, in, sub, true))
}

// renderHome loads the list, groups it by day, and renders the home page from a
// partly-built homeView — filling in the shared chrome (htmx src, form action,
// inline preview, day groups) around whatever form state the caller set: the
// echoed line and the Intent (plain add, payback, or edit). Every full-page
// render flows through here, so the list/group/render path lives in one place.
// The inline preview is built non-interactively so the save control stays
// enabled for a no-JS submit.
func (s *Server) renderHome(w http.ResponseWriter, r *http.Request, status int, v homeView) {
	expenses, err := s.store.List(r.Context())
	if err != nil {
		log.Printf("listing expenses: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	incomes, err := s.store.ListIncomes(r.Context())
	if err != nil {
		log.Printf("listing incomes: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	v.HTMXSrc = s.htmxSrc
	v.FormAction = formAction(v.Intent)
	if v.Intent.IsEdit() {
		v.EditKind = routeKind(v.Intent)
	}
	if v.ExportFrom == "" {
		v.ExportFrom = firstOfMonth(s.now()).Format(isoDate)
	}
	v.Preview = buildPreview(v.Raw, v.Intent, expense.Submit(v.Intent, v.Raw, s.now()), false)
	v.Groups = groupByDay(expenses, incomes, s.now())
	s.render(w, status, v)
}

// handleAdd submits the line under the add Intent — a payback when the box was
// opened via a row's "+ payback" (the hidden linked_expense_id), else a plain
// add — re-validating the save gate on the server so an entry that skipped the
// client is still refused, and persists whichever record the line became. On
// success it redirects home (Post/Redirect/Get); on a save-gate violation it
// re-renders the page at 422 with the offending text, still under the same
// Intent so a payback keeps its banner and hidden link, storing nothing. A
// payback whose parent no longer exists is a 409 back under the plain add
// Intent, with a note.
func (s *Server) handleAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	raw := r.PostFormValue("raw")
	in, err := s.addIntent(r)
	switch {
	case errors.Is(err, errParentGone):
		s.renderHome(w, r, http.StatusConflict, homeView{
			Raw:    raw,
			Intent: expense.Add(),
			Notice: "That expense no longer exists, so the payback wasn't saved.",
		})
		return
	case err != nil:
		log.Printf("loading payback parent: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	sub := expense.Submit(in, raw, s.now())
	if !sub.OK() {
		s.renderHome(w, r, http.StatusUnprocessableEntity, homeView{Raw: raw, Intent: in})
		return
	}
	if sub.Income != nil {
		err = s.store.CreateIncome(r.Context(), sub.Income)
	} else {
		err = s.store.Create(r.Context(), sub.Expense)
	}
	if err != nil {
		log.Printf("creating record: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// addAction is the quick-add form's action in add mode; edit mode swaps in an
// "/edit/{kind}/{id}" action built by editAction.
const addAction = "/add"

// kindExpense and kindIncome are the two record kinds the edit/delete routes are
// qualified by (ADR-0009); they are the only accepted {kind} path values.
const (
	kindExpense = "expense"
	kindIncome  = "income"
)

// editAction is the quick-add form's action when editing the given record: the
// kind-qualified "/edit/{kind}/{id}" endpoint.
func editAction(kind string, id int64) string {
	return "/edit/" + kind + "/" + strconv.FormatInt(id, 10)
}

// formAction is where the quick-add form posts under the Intent: the record's
// kind-qualified edit endpoint while editing, else /add.
func formAction(in expense.Intent) string {
	if !in.IsEdit() {
		return addAction
	}
	return editAction(routeKind(in), in.ID())
}

// routeKind is the {kind} route value of the record an edit Intent targets.
func routeKind(in expense.Intent) string {
	if in.EditsIncome() {
		return kindIncome
	}
	return kindExpense
}

// errParentGone and errNotEditable are the ways building an Intent from request
// state can fail, besides a missing edit target (store.ErrNotFound).
var (
	// errParentGone: a payback's parent expense no longer exists.
	errParentGone = errors.New("payback parent no longer exists")
	// errNotEditable: the edit target is past the edit window (ADR-0008).
	errNotEditable = errors.New("record too old to edit")
)

// editIntent loads the record an edit targets and builds its edit Intent,
// returning the record's canonical entry line too — what fills the box, never
// the verbatim raw_text, so re-parsing on save can't shift its date (ADR-0008).
// It fails with store.ErrNotFound for an unknown id and errNotEditable past the
// edit window.
func (s *Server) editIntent(ctx context.Context, kind string, id int64) (expense.Intent, string, error) {
	if kind == kindIncome {
		i, err := s.store.GetIncome(ctx, id)
		if err != nil {
			return expense.Intent{}, "", err
		}
		in, ok := expense.EditIncome(i, s.now())
		if !ok {
			return expense.Intent{}, "", errNotEditable
		}
		return in, i.EntryLine(), nil
	}
	e, err := s.store.Get(ctx, id)
	if err != nil {
		return expense.Intent{}, "", err
	}
	in, ok := expense.EditExpense(e, s.now())
	if !ok {
		return expense.Intent{}, "", errNotEditable
	}
	return in, e.EntryLine(), nil
}

// previewIntent rebuilds the quick-add box's Intent for a /preview swap from
// its posted hidden fields: an edit target (kind + id) wins, else the same add
// Intent /add would build. It mirrors exactly what the save would submit under,
// so the preview can't drift from the save.
func (s *Server) previewIntent(r *http.Request) (expense.Intent, error) {
	kind := r.PostFormValue("kind")
	if id, err := strconv.ParseInt(r.PostFormValue("id"), 10, 64); err == nil && id > 0 && (kind == kindExpense || kind == kindIncome) {
		in, _, err := s.editIntent(r.Context(), kind, id)
		return in, err
	}
	return s.addIntent(r)
}

// addIntent builds the Intent of a submission to /add: adding a payback when the
// box carries a hidden linked_expense_id, else a plain add. It never builds an
// edit Intent — /add only ever creates. A payback whose parent is gone fails
// with errParentGone.
func (s *Server) addIntent(r *http.Request) (expense.Intent, error) {
	linked := linkedExpenseID(r)
	if linked == nil {
		return expense.Add(), nil
	}
	parent, err := s.store.Get(r.Context(), *linked)
	if errors.Is(err, store.ErrNotFound) {
		return expense.Intent{}, errParentGone
	}
	if err != nil {
		return expense.Intent{}, err
	}
	return expense.AddPayback(parent), nil
}

// respondEditErr maps an editIntent failure to an HTTP response: an unknown id
// is a 404, a record past the edit window a 403 (ADR-0008), anything else a
// logged 500. It reports whether it wrote one.
func respondEditErr(w http.ResponseWriter, r *http.Request, id int64, err error) bool {
	if errors.Is(err, errNotEditable) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return true
	}
	return respondStoreErr(w, r, "loading record for edit", id, err)
}

// handleEditForm loads a record's canonical entry line back into the quick-add
// box under its edit Intent, for either {kind}. An unknown id or kind is a 404;
// a record too old to edit (ADR-0008) is a 403.
func (s *Server) handleEditForm(w http.ResponseWriter, r *http.Request) {
	kind, ok := parseKind(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	in, line, err := s.editIntent(r.Context(), kind, id)
	if respondEditErr(w, r, id, err) {
		return
	}
	s.renderHome(w, r, http.StatusOK, homeView{Raw: line, Intent: in})
}

// handleEditSave submits the edited line under the record's edit Intent —
// re-validating the save gate on the server, kind rules included, so an edit
// can't turn an expense into an income or back (ADR-0009 amendment) — and
// updates the record in place, keeping its identity (and a payback's parent
// link) and refreshing updated_at. It guards on the stored date, never the
// submitted line, so a record too old to edit is refused (403) even on a direct
// POST. On success it redirects home; on a save-gate violation it re-renders the
// form under the same edit Intent at 422, changing nothing; an unknown id or
// kind is a 404.
func (s *Server) handleEditSave(w http.ResponseWriter, r *http.Request) {
	kind, ok := parseKind(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	in, _, err := s.editIntent(r.Context(), kind, id)
	if respondEditErr(w, r, id, err) {
		return
	}

	raw := r.PostFormValue("raw")
	sub := expense.Submit(in, raw, s.now())
	if !sub.OK() {
		s.renderHome(w, r, http.StatusUnprocessableEntity, homeView{Raw: raw, Intent: in})
		return
	}
	if sub.Income != nil {
		err = s.store.UpdateIncome(r.Context(), sub.Income)
	} else {
		err = s.store.Update(r.Context(), sub.Expense)
	}
	if respondStoreErr(w, r, "updating "+kind, id, err) {
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleDelete removes a record, switching store method by the {kind} path
// value, then redirects home (Post/Redirect/Get). Deleting a fronted expense
// cascade-deletes its paybacks at the schema level (ADR-0009). An unknown id (or
// kind) is a 404.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	kind, ok := parseKind(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	op, del := "deleting expense", s.store.Delete
	if kind == kindIncome {
		op, del = "deleting income", s.store.DeleteIncome
	}
	if respondStoreErr(w, r, op, id, del(r.Context(), id)) {
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handlePaybackStart primes the quick-add box to log a payback against an
// existing expense under the AddPayback Intent: income mode (the '+' sigil
// pre-filled), a non-editable chip naming the parent, and a hidden
// linked_expense_id so the saved income links to it out-of-band — the link is
// never expressible in the entry text (ADR-0009). An unknown id is a 404; the
// primed box still posts to /add, where the hidden link is read back.
func (s *Server) handlePaybackStart(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	e, err := s.store.Get(r.Context(), id)
	if respondStoreErr(w, r, "loading expense for payback", id, err) {
		return
	}
	// Prime the '+' income sigil so the box opens in income mode and the user
	// types only the amount and who paid it back.
	s.renderHome(w, r, http.StatusOK, homeView{Raw: incomePrefix, Intent: expense.AddPayback(e)})
}

// incomePrefix is the leading '+' sigil that marks an entry as an income
// (ADR-0009); it primes the payback box so the line is already in income mode.
const incomePrefix = "+"

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

// parseKind reads the {kind} path segment, accepting only "expense" or "income"
// (ADR-0009) and writing a 404 for anything else, so an unknown kind never
// reaches a store method.
func parseKind(w http.ResponseWriter, r *http.Request) (string, bool) {
	kind := r.PathValue("kind")
	if kind != kindExpense && kind != kindIncome {
		http.NotFound(w, r)
		return "", false
	}
	return kind, true
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

// linkedExpenseID reads the hidden linked_expense_id form field: nil when the
// field is absent, blank, or malformed (a standalone income), else the parsed
// parent id (a payback, see addIntent). The field is set out-of-band by the
// "+ payback" flow and never appears in the entry text (ADR-0009); a bad value
// degrades to a standalone income rather than a 500.
func linkedExpenseID(r *http.Request) *int64 {
	v := strings.TrimSpace(r.PostFormValue("linked_expense_id"))
	if v == "" {
		return nil
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		return nil
	}
	return &id
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
// ErrMustBeIncome is worded per Intent in errorMessages.
var saveGateMessages = map[expense.ParseError]string{
	expense.ErrNoAmount:         "needs an amount",
	expense.ErrEmptyDescription: "needs a description",
	expense.ErrTwoDateTokens:    "two dates",
	expense.ErrTwoAccounts:      "two @accounts",
	expense.ErrSplitOnIncome:    "cannot split an income",
	expense.ErrMustBeIncome:     "an income must keep its + amount — delete it and add the expense instead",
	expense.ErrMustBeExpense:    "an expense can't start with + — delete it and add the income instead",
}

// errorMessages maps the save-gate violations of a line submitted under in to
// the user-facing wording surfaced in the form, naming a payback as such, and
// falling back to the terse message for any unrecognised code.
func errorMessages(errs []expense.ParseError, in expense.Intent) []string {
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		switch m, ok := saveGateMessages[e]; {
		case e == expense.ErrMustBeIncome && in.IsPayback():
			msgs = append(msgs, "a payback must keep its + amount")
		case ok:
			msgs = append(msgs, m)
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
