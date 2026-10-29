package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/emepetres/life-ledger/internal/auth"
	"github.com/emepetres/life-ledger/internal/expense"
	"github.com/emepetres/life-ledger/internal/server"
	"github.com/emepetres/life-ledger/internal/store"
)

const (
	javiPassword  = "javi-secret"
	carlaPassword = "carla-secret"
)

// newTwoUserServer is the real handler stack with both users' passwords wired, over
// a store the test can seed and inspect.
func newTwoUserServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	hash := func(pw string) []byte {
		h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	guard, err := auth.NewGuard(auth.Config{
		PasswordHash:      hash(javiPassword),
		CarlaPasswordHash: hash(carlaPassword),
		SessionSecret:     []byte("test-signing-secret"),
	})
	if err != nil {
		t.Fatal(err)
	}
	st := openTempStore(t)
	h, err := server.New(st, server.WithClock(func() time.Time { return testToday }), server.WithGuard(guard))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, st
}

// as logs a fresh client in with password and returns it, with redirects NOT
// followed so tests can assert on 303s.
func as(t *testing.T, ts *httptest.Server, password string) *http.Client {
	t.Helper()
	c := jarClient(t)
	if resp := login(t, c, ts, password); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status = %d, want 303", resp.StatusCode)
	}
	return c
}

func do(t *testing.T, c *http.Client, method string, ts *httptest.Server, path string, form url.Values) (*http.Response, string) {
	t.Helper()
	var resp *http.Response
	var err error
	if method == http.MethodGet {
		resp, err = c.Get(ts.URL + path)
	} else {
		resp, err = c.PostForm(ts.URL+path, form)
	}
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func getAs(t *testing.T, c *http.Client, ts *httptest.Server, path string) (*http.Response, string) {
	return do(t, c, http.MethodGet, ts, path, nil)
}

// addShared posts a line from the Shared tab's form.
func addShared(t *testing.T, c *http.Client, ts *httptest.Server, raw string) *http.Response {
	t.Helper()
	resp, _ := do(t, c, http.MethodPost, ts, "/add", url.Values{"raw": {raw}, "tab": {"shared"}})
	return resp
}

func listExpenses(t *testing.T, st *store.Store) []expense.Expense {
	t.Helper()
	es, err := st.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return es
}

// Which password was typed decides where you land: Javi on his ledger, Carla on
// the Shared tab.
func TestLoginLandsOnEachUsersTab(t *testing.T) {
	ts, _ := newTwoUserServer(t)
	for _, tc := range []struct{ pw, want string }{{javiPassword, "/"}, {carlaPassword, "/shared"}} {
		resp := login(t, jarClient(t), ts, tc.pw)
		if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || loc != tc.want {
			t.Errorf("login with %q: %d %q, want 303 %q", tc.pw, resp.StatusCode, loc, tc.want)
		}
	}
}

// Shared expenses live only in /shared and ordinary ones only in /.
func TestTabsListOnlyTheirOwnExpenses(t *testing.T) {
	ts, st := newTwoUserServer(t)
	javi := as(t, ts, javiPassword)

	do(t, javi, http.MethodPost, ts, "/add", url.Values{"raw": {"9 desayuno"}})
	addShared(t, javi, ts, "30 cena")

	_, home := getAs(t, javi, ts, "/")
	if !strings.Contains(home, "desayuno") || strings.Contains(home, "cena") {
		t.Errorf("ordinary tab should list only desayuno; got:\n%s", home)
	}
	_, shared := getAs(t, javi, ts, "/shared")
	if !strings.Contains(shared, "cena") || strings.Contains(shared, "desayuno") {
		t.Errorf("shared tab should list only cena; got:\n%s", shared)
	}

	es := listExpenses(t, st)
	if len(es) != 2 {
		t.Fatalf("want 2 expenses, got %d", len(es))
	}
	if es[0].Description != "cena" || !es[0].Shared || es[0].PaidBy != expense.Javi {
		t.Errorf("shared add: %+v", es[0])
	}
	if es[1].Shared {
		t.Errorf("ordinary add came out shared: %+v", es[1])
	}
}

// A line Carla enters is always a Shared expense paid by Carla, whatever the form
// claims.
func TestCarlaAddsAreSharedAndPaidByCarla(t *testing.T) {
	ts, st := newTwoUserServer(t)
	carla := as(t, ts, carlaPassword)

	// No tab field at all, and then a forged "ordinary" one.
	do(t, carla, http.MethodPost, ts, "/add", url.Values{"raw": {"20 compra"}})
	do(t, carla, http.MethodPost, ts, "/add", url.Values{"raw": {"10 cine"}, "tab": {"ordinary"}})

	es := listExpenses(t, st)
	if len(es) != 2 {
		t.Fatalf("want 2 expenses, got %d", len(es))
	}
	for _, e := range es {
		if !e.Shared || e.PaidBy != expense.Carla {
			t.Errorf("%q: Shared=%v PaidBy=%q, want shared, carla", e.Description, e.Shared, e.PaidBy)
		}
	}
}

// Carla only ever sees the Shared tab.
func TestCarlaIsRedirectedToSharedTab(t *testing.T) {
	ts, st := newTwoUserServer(t)
	_ = st.Create(context.Background(), expense.NewExpense(testToday, 900, "desayuno", nil, false, "9 desayuno"))
	carla := as(t, ts, carlaPassword)

	resp, _ := getAs(t, carla, ts, "/")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/shared" {
		t.Errorf("GET / as Carla: %d %q, want 303 /shared", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, body := getAs(t, carla, ts, "/shared")
	if resp.StatusCode != http.StatusOK || strings.Contains(body, "desayuno") {
		t.Errorf("GET /shared as Carla: %d; must not leak ordinary expenses", resp.StatusCode)
	}
	if strings.Contains(body, `href="/"`) {
		t.Errorf("Carla's page must not link to the ordinary tab")
	}
}

// Carla can't reach Javi's ordinary records by id: edit, delete and payback all
// 404, as if they did not exist.
func TestCarlaCannotTouchOrdinaryRecords(t *testing.T) {
	ts, st := newTwoUserServer(t)
	e := expense.NewExpense(testToday, 900, "desayuno", nil, false, "9 desayuno")
	_ = st.Create(context.Background(), e)
	inc := expense.NewIncome(testToday, 500, "nómina", nil, nil, "+5 nómina")
	_ = st.CreateIncome(context.Background(), inc)
	carla := as(t, ts, carlaPassword)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/edit/expense/" + itoa(e.ID)},
		{http.MethodPost, "/edit/expense/" + itoa(e.ID)},
		{http.MethodPost, "/delete/expense/" + itoa(e.ID)},
		{http.MethodGet, "/payback/" + itoa(e.ID)},
		{http.MethodGet, "/edit/income/" + itoa(inc.ID)},
		{http.MethodPost, "/delete/income/" + itoa(inc.ID)},
	} {
		resp, _ := do(t, carla, tc.method, ts, tc.path, url.Values{"raw": {"1 hack"}})
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s as Carla: status %d, want 404", tc.method, tc.path, resp.StatusCode)
		}
	}
	if es := listExpenses(t, st); len(es) != 1 || es[0].Description != "desayuno" {
		t.Errorf("ordinary expense was changed: %+v", es)
	}
}

// Either user can edit and delete a Shared expense the other registered; it stays
// Shared and keeps its payer.
func TestEitherUserEditsAndDeletesTheOthersSharedExpense(t *testing.T) {
	ts, st := newTwoUserServer(t)
	javi := as(t, ts, javiPassword)
	carla := as(t, ts, carlaPassword)
	addShared(t, javi, ts, "30 cena")
	e := listExpenses(t, st)[0]

	editPath := "/edit/expense/" + itoa(e.ID)
	if resp, body := getAs(t, carla, ts, editPath); resp.StatusCode != http.StatusOK || !strings.Contains(body, "30 cena") {
		t.Fatalf("Carla edit form: %d", resp.StatusCode)
	}
	do(t, carla, http.MethodPost, ts, editPath, url.Values{"raw": {"35 cena"}})
	got := listExpenses(t, st)[0]
	if got.Amount != 3500 || !got.Shared || got.PaidBy != expense.Javi {
		t.Errorf("after Carla's edit: %+v, want 35 € still shared and paid by javi", got)
	}

	do(t, carla, http.MethodPost, ts, "/delete/expense/"+itoa(e.ID), nil)
	if n := len(listExpenses(t, st)); n != 0 {
		t.Errorf("Carla's delete left %d expenses", n)
	}
}

// The Shared tab opens with who owes whom.
func TestSharedTabShowsBalance(t *testing.T) {
	ts, _ := newTwoUserServer(t)
	javi := as(t, ts, javiPassword)
	carla := as(t, ts, carlaPassword)

	if _, body := getAs(t, javi, ts, "/shared"); !strings.Contains(body, "Estáis a mano") {
		t.Errorf("empty ledger should be even; got:\n%s", body)
	}

	addShared(t, javi, ts, "100 alquiler")
	if _, body := getAs(t, javi, ts, "/shared"); !strings.Contains(body, "Carla debe €50.00 a Javi") {
		t.Errorf("Javi paid 100: want 'Carla debe €50.00 a Javi'; got:\n%s", body)
	}

	addShared(t, carla, ts, "160 viaje")
	if _, body := getAs(t, carla, ts, "/shared"); !strings.Contains(body, "Javi debe €30.00 a Carla") {
		t.Errorf("Carla paid 160 on top: want 'Javi debe €30.00 a Carla'; got:\n%s", body)
	}
}

// Settlements: '=' in the Shared tab moves the balance, is paid by the typist, and
// shows only in the Shared tab (Javi's as an expense, Carla's as an income).
func TestSettlements(t *testing.T) {
	ts, st := newTwoUserServer(t)
	javi := as(t, ts, javiPassword)
	carla := as(t, ts, carlaPassword)

	addShared(t, carla, ts, "100 viaje") // Javi owes Carla 50
	addShared(t, javi, ts, "=30 bizum")  // Javi pays Carla 30 -> owes 20
	if _, body := getAs(t, javi, ts, "/shared"); !strings.Contains(body, "Javi debe €20.00 a Carla") {
		t.Errorf("after Javi's settlement: want 'Javi debe €20.00 a Carla'; got:\n%s", body)
	}

	es := listExpenses(t, st)
	var settle *expense.Expense
	for i := range es {
		if es[i].Settlement {
			settle = &es[i]
		}
	}
	if settle == nil || settle.Shared || settle.PaidBy != expense.Javi || settle.Amount != 3000 {
		t.Fatalf("Javi's settlement: %+v", settle)
	}

	addShared(t, carla, ts, "=25 bizum") // Carla pays Javi 25 -> Javi owes 45
	if _, body := getAs(t, carla, ts, "/shared"); !strings.Contains(body, "Javi debe €45.00 a Carla") {
		t.Errorf("after Carla's settlement: want 'Javi debe €45.00 a Carla'; got:\n%s", body)
	}
	incomes, _ := st.ListIncomes(context.Background())
	if len(incomes) != 1 || !incomes[0].Settlement {
		t.Fatalf("Carla's settlement should be a settlement income: %+v", incomes)
	}

	// Neither shows in Javi's ordinary tab.
	_, home := getAs(t, javi, ts, "/")
	if strings.Contains(home, "bizum") {
		t.Errorf("settlements must not appear in the ordinary tab; got:\n%s", home)
	}
	// Both are listed in the Shared tab.
	_, shared := getAs(t, javi, ts, "/shared")
	if strings.Count(shared, "bizum") < 2 {
		t.Errorf("both settlements should be listed in the shared tab; got:\n%s", shared)
	}
}

// Each tab refuses the other's sigil with a 422 and stores nothing.
func TestTabsRefuseTheOtherTabsSigils(t *testing.T) {
	ts, st := newTwoUserServer(t)
	javi := as(t, ts, javiPassword)

	if resp, _ := do(t, javi, http.MethodPost, ts, "/add", url.Values{"raw": {"=30 devolución"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("= in ordinary: status %d, want 422", resp.StatusCode)
	}
	if resp, _ := do(t, javi, http.MethodPost, ts, "/add", url.Values{"raw": {"+30 reembolso"}, "tab": {"shared"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("+ in shared: status %d, want 422", resp.StatusCode)
	}
	if n := len(listExpenses(t, st)); n != 0 {
		t.Errorf("refused lines stored %d expenses", n)
	}
	// The ordinary tab still takes standalone incomes.
	if resp, _ := do(t, javi, http.MethodPost, ts, "/add", url.Values{"raw": {"+30 reembolso"}}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("+ in ordinary: status %d, want 303 (saved)", resp.StatusCode)
	}
}

// Paybacks work on a shared expense: added from its own button (by either user),
// shown under it in the Shared tab, netting what the other owes, and never listed
// in the ordinary tab.
func TestPaybackOnSharedExpense(t *testing.T) {
	ts, st := newTwoUserServer(t)
	javi := as(t, ts, javiPassword)
	carla := as(t, ts, carlaPassword)
	addShared(t, javi, ts, "100 cena")
	e := listExpenses(t, st)[0]

	if resp, body := getAs(t, carla, ts, "/payback/"+itoa(e.ID)); resp.StatusCode != http.StatusOK || !strings.Contains(body, "payback → cena") {
		t.Fatalf("Carla payback form: %d", resp.StatusCode)
	}
	resp, _ := do(t, carla, http.MethodPost, ts, "/add", url.Values{"raw": {"+40 reembolso"}, "linked_expense_id": {itoa(e.ID)}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/shared" {
		t.Fatalf("add payback: %d %q, want 303 /shared", resp.StatusCode, resp.Header.Get("Location"))
	}

	_, shared := getAs(t, javi, ts, "/shared")
	// 100 paid by Javi, 40 back => net 60, Carla owes 30.
	if !strings.Contains(shared, "Carla debe €30.00 a Javi") || !strings.Contains(shared, "reembolso") {
		t.Errorf("shared tab after payback; got:\n%s", shared)
	}
	if _, home := getAs(t, javi, ts, "/"); strings.Contains(home, "reembolso") {
		t.Errorf("a payback of a shared expense must not show in the ordinary tab")
	}
}

// The splittypie export is gone.
func TestSplittypieExportIsGone(t *testing.T) {
	ts, _ := newTwoUserServer(t)
	javi := as(t, ts, javiPassword)
	if resp, _ := getAs(t, javi, ts, "/export/splittypie?from=2026-07-01"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("export status %d, want 404", resp.StatusCode)
	}
	if _, home := getAs(t, javi, ts, "/"); strings.Contains(strings.ToLower(home), "splittypie") {
		t.Errorf("ordinary page still mentions splittypie")
	}
}

// Javi gets both tabs in his navigation.
func TestJaviSeesBothTabs(t *testing.T) {
	ts, _ := newTwoUserServer(t)
	javi := as(t, ts, javiPassword)
	for _, path := range []string{"/", "/shared"} {
		_, body := getAs(t, javi, ts, path)
		if !strings.Contains(body, `href="/shared"`) || !strings.Contains(body, `href="/"`) {
			t.Errorf("%s: want links to both tabs; got:\n%s", path, body)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// A shared line is stored with its full amount: nothing is halved on save, the
// half is derived for the balance.
func TestSharedExpenseStoresFullAmount(t *testing.T) {
	ts, st := newTwoUserServer(t)
	addShared(t, as(t, ts, javiPassword), ts, "10 dinner")
	if es := listExpenses(t, st); len(es) != 1 || es[0].Amount != 1000 {
		t.Errorf("stored %+v, want one expense of 1000 cents", es)
	}
}

// The live preview knows which tab a line is typed in: '=' previews as a
// settlement in the Shared tab, and the other tab's sigil blocks the save.
func TestPreviewKnowsTheTab(t *testing.T) {
	ts := newTestServer(t)
	post := func(raw, tab string) string {
		resp, err := http.PostForm(ts.URL+"/preview", url.Values{"raw": {raw}, "tab": {tab}})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(b)
	}
	if body := post("=40 bizum", "shared"); !strings.Contains(body, "settlement") || strings.Contains(body, "disabled") {
		t.Errorf("= in shared should preview as a settlement and be saveable; got:\n%s", body)
	}
	if body := post("=40 bizum", ""); !strings.Contains(body, "settlement belongs in the Shared tab") || !strings.Contains(body, "disabled") {
		t.Errorf("= in ordinary should be blocked; got:\n%s", body)
	}
	if body := post("+30 x", "shared"); !strings.Contains(body, "incomes in the Shared tab") || !strings.Contains(body, "disabled") {
		t.Errorf("+ in shared should be blocked; got:\n%s", body)
	}
}
