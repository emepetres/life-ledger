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
	// The hidden linked_expense_id, when present, rides along automatically:
	// htmx includes every named field of the input's closest form in the
	// request, and that hidden field lives in the same form as the quick-add
	// input (ADR-0009 amendment, #62).
	linked := linkedExpenseID(r) != nil
	parsed := expense.Parse(raw, s.now()).GatePayback(linked)
	s.renderPartial(w, "preview", buildPreview(raw, parsed, true, editing, linked))
}

// renderForm renders the home page with the given status and plain quick-add
// form state: the echoed raw line, where the form posts (addAction, or an
// /edit/expense/{id} action in edit mode), and the edit-mode flag. It is the
// entry point for the expense-side full-page renders — plain home, save-gate
// rejection, and the expense edit form — delegating to renderHome for the shared
// list/group/render. The income edit form renders through renderIncomeEditForm
// instead, since it also carries the kind-aware Income flag (ADR-0009).
func (s *Server) renderForm(w http.ResponseWriter, r *http.Request, status int, raw, formAction string, editing bool) {
	s.renderHome(w, r, status, homeView{
		Raw:        raw,
		FormAction: formAction,
		Editing:    editing,
	})
}

// renderIncomeEditForm renders the home page with the quick-add box in income
// edit mode: the canonical line echoed into the box, the kind-qualified income
// edit action, and the Income flag so the edit heading names the right kind
// (ADR-0009). It is renderForm's income twin, sharing the one homeView shape
// across both the GET form load and the save-gate re-render.
func (s *Server) renderIncomeEditForm(w http.ResponseWriter, r *http.Request, status int, raw string, id int64) {
	s.renderHome(w, r, status, homeView{
		Raw:        raw,
		FormAction: editAction(kindIncome, id),
		Editing:    true,
		Income:     true,
	})
}

// renderHome loads the list, groups it by day, and renders the home page from a
// partly-built homeView — filling in the shared chrome (htmx src, inline
// preview, day groups) around whatever form state the caller set (plain add,
// edit, or payback). Every full-page render flows through here, so the
// list/group/render path lives in one place. The inline preview is built
// non-interactively so the save control stays enabled for a no-JS submit.
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
	// v.Payback carries whether this render's box is in payback mode (ADR-0009
	// amendment, #62); the gate is reapplied here so the inline preview matches
	// what /preview would show for the same state.
	parsed := expense.Parse(v.Raw, s.now()).GatePayback(v.Payback)
	v.HTMXSrc = s.htmxSrc
	v.Preview = buildPreview(v.Raw, parsed, false, v.Editing, v.Payback)
	v.Groups = groupByDay(expenses, incomes, s.now())
	s.render(w, status, v)
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
	// A hidden linked_expense_id — present only when the box was opened via a
	// row's "+ payback" — links the saved income to its parent out-of-band,
	// making it a payback; absent, it stores as a standalone income with a nil
	// link. Read once so the gate check and the eventual save agree.
	linkedID := linkedExpenseID(r)

	// A '+' line is an income (ADR-0009). The '*'-on-income case never reaches
	// the store because the save gate above rejects it (ErrSplitOnIncome); a
	// payback link on a non-income line is rejected the same way
	// (ErrPaybackNotIncome, ADR-0009 amendment #62) — a payback can't silently
	// fall through and save as an unlinked expense.
	parsed := expense.Parse(raw, s.now()).GatePayback(linkedID != nil)
	if !parsed.OK() {
		s.renderAddRejected(w, r, raw, linkedID)
		return
	}

	if parsed.IsIncome {
		income := newIncome(parsed, raw)
		income.LinkedExpenseID = linkedID
		if err := s.store.CreateIncome(r.Context(), income); err != nil {
			log.Printf("creating income: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	if err := s.store.Create(r.Context(), newExpense(parsed, raw)); err != nil {
		log.Printf("creating expense: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// renderAddRejected re-renders the quick-add box after a server-side save-gate
// refusal on /add, at 422. When the rejected submission carried a payback link,
// it reloads the parent so the payback banner and hidden linked_expense_id
// survive the re-render — otherwise the box would silently drop back to plain
// add mode and the user's next submit would save an unlinked entry instead of
// fixing the payback (ADR-0009 amendment, #62). A parent that no longer exists
// degrades to a plain rejection rather than a 500: the link is already gone, so
// there is nothing left to re-show.
func (s *Server) renderAddRejected(w http.ResponseWriter, r *http.Request, raw string, linkedID *int64) {
	if linkedID == nil {
		s.renderForm(w, r, http.StatusUnprocessableEntity, raw, addAction, false)
		return
	}
	parent, err := s.store.Get(r.Context(), *linkedID)
	if err != nil {
		s.renderForm(w, r, http.StatusUnprocessableEntity, raw, addAction, false)
		return
	}
	s.renderHome(w, r, http.StatusUnprocessableEntity, homeView{
		Raw:               raw,
		FormAction:        addAction,
		Payback:           true,
		LinkedExpenseID:   *linkedID,
		LinkedExpenseDesc: parent.Description,
	})
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

// handleEditForm loads a record's canonical entry line back into the quick-add
// box in edit mode, switching store method by the {kind} path value. An unknown
// or non-numeric id is a 404, as is an unrecognised kind.
func (s *Server) handleEditForm(w http.ResponseWriter, r *http.Request) {
	kind, ok := parseKind(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if kind == kindIncome {
		s.editFormIncome(w, r, id)
		return
	}
	s.editFormExpense(w, r, id)
}

// editFormExpense loads the expense's canonical entry line into the quick-add box
// in edit mode, reusing the same inline preview + save gate as adding. An unknown
// id is a 404; an expense too old to edit (ADR-0008) is a 403.
func (s *Server) editFormExpense(w http.ResponseWriter, r *http.Request, id int64) {
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
	s.renderForm(w, r, http.StatusOK, e.EntryLine(), editAction(kindExpense, id), true)
}

// editFormIncome is editFormExpense's income twin: it loads the income's
// canonical entry line (the leading '+' sigil and all) into the box, capped by
// the same Editable cutoff (ADR-0008). A payback's parent link is not expressible
// in the line and is preserved out-of-band on save (ADR-0009), so nothing extra
// is threaded through the form. An unknown id is a 404; too-old is a 403.
func (s *Server) editFormIncome(w http.ResponseWriter, r *http.Request, id int64) {
	i, err := s.store.GetIncome(r.Context(), id)
	if respondStoreErr(w, r, "loading income for edit", id, err) {
		return
	}
	if !expense.Editable(i.Date, s.now()) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	s.renderIncomeEditForm(w, r, http.StatusOK, i.EntryLine(), id)
}

// handleEditSave re-parses the edited line, re-validates the save gate on the
// server (so an entry that skipped the client is still refused), and updates the
// record in place, switching store method by the {kind} path value. On success
// it redirects home (Post/Redirect/Get); on a save-gate violation it re-renders
// the form in edit mode at 422, changing nothing; an unknown id (or kind) is a
// 404, and a record too old to edit (ADR-0008) a 403.
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
	if kind == kindIncome {
		s.editSaveIncome(w, r, id)
		return
	}
	s.editSaveExpense(w, r, id)
}

// editSaveExpense updates an expense from the edited line, keeping its identity
// (id, created_at) and refreshing updated_at (ADR-0001). It guards on the stored
// date, never the submitted line, so an expense too old to edit is refused even
// on a direct POST that skipped the list's Edit link.
func (s *Server) editSaveExpense(w http.ResponseWriter, r *http.Request, id int64) {
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
		s.renderForm(w, r, http.StatusUnprocessableEntity, raw, editAction(kindExpense, id), true)
		return
	}

	e := newExpense(parsed, raw)
	e.ID = id
	if respondStoreErr(w, r, "updating expense", id, s.store.Update(r.Context(), e)) {
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// editSaveIncome is editSaveExpense's income twin. It builds the income from the
// parsed line with a nil link and lets the store preserve the stored
// linked_expense_id: an edit never re-parents a payback (ADR-0009), so fixing an
// amount or account can't detach it from its ticket. The kind is fixed by the
// route, so the record stays an income regardless of what the edited line parses
// to.
func (s *Server) editSaveIncome(w http.ResponseWriter, r *http.Request, id int64) {
	existing, err := s.store.GetIncome(r.Context(), id)
	if respondStoreErr(w, r, "loading income for edit", id, err) {
		return
	}
	if !expense.Editable(existing.Date, s.now()) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	raw := r.PostFormValue("raw")

	parsed := expense.Parse(raw, s.now())
	if !parsed.OK() {
		s.renderIncomeEditForm(w, r, http.StatusUnprocessableEntity, raw, id)
		return
	}

	i := newIncome(parsed, raw)
	i.ID = id
	// LinkedExpenseID stays nil here on purpose: UpdateIncome does not write that
	// column, so the stored parent link survives the edit (ADR-0009).
	if respondStoreErr(w, r, "updating income", id, s.store.UpdateIncome(r.Context(), i)) {
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
// existing expense: income mode (the '+' sigil pre-filled), a non-editable chip
// naming the parent, and a hidden linked_expense_id so the saved income links to
// it out-of-band — the link is never expressible in the entry text (ADR-0009).
// It loads the parent to name it in the chip and to 404 an unknown id; the
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
	s.renderHome(w, r, http.StatusOK, homeView{
		// Prime the '+' income sigil so the box opens in income mode and the user
		// types only the amount and who paid it back.
		Raw:               incomePrefix,
		FormAction:        addAction,
		Payback:           true,
		LinkedExpenseID:   e.ID,
		LinkedExpenseDesc: e.Description,
	})
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

// newExpense turns a validated parse result into the record to store. The full
// paid Amount is stored regardless of Split (ADR-0001); a blank account is left
// nil so the store writes SQL NULL, and the verbatim line is retained as
// RawText. The store fills in the identity and timestamps.
func newExpense(p expense.ParsedEntry, raw string) *expense.Expense {
	return expense.NewExpense(p.Date, p.Amount, p.Description, accountPtr(p.Account), p.Split, raw)
}

// newIncome turns a validated income parse result into the record to store,
// with a nil link — the caller sets LinkedExpenseID from the hidden field when
// the income is a payback (ADR-0009), never from the entry line. A blank account
// is left nil so the store writes SQL NULL, and the verbatim line is retained as
// RawText.
func newIncome(p expense.ParsedEntry, raw string) *expense.Income {
	return expense.NewIncome(p.Date, p.Amount, p.Description, accountPtr(p.Account), nil, raw)
}

// linkedExpenseID reads the hidden linked_expense_id form field into the pointer
// the store binds: nil when the field is absent, blank, or malformed (a
// standalone income), else the parsed parent id (a payback). The field is set
// out-of-band by the "+ payback" flow and never appears in the entry text
// (ADR-0009); a bad value degrades to a standalone income rather than a 500, and
// a non-existent id would be caught by the foreign-key constraint on insert.
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

// accountPtr turns the parser's bare account string into the nullable pointer
// the store binds: nil for a blank account (written as SQL NULL), else its
// address. Shared by newExpense and newIncome so the NULL-vs-empty rule can't
// drift between the two.
func accountPtr(account string) *string {
	if account == "" {
		return nil
	}
	return &account
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
	expense.ErrSplitOnIncome:    "cannot split an income",
	expense.ErrPaybackNotIncome: "a payback must keep its + amount",
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
