package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Javi forgiving what Carla owes him, with nothing filled in, forgives the whole
// balance as of today: the balance is square and no money record is created.
func TestForgiveWholeBalanceByDefault(t *testing.T) {
	ts, st := newTwoUserServer(t)
	javi := as(t, ts, javiPassword)
	addShared(t, javi, ts, "100 alquiler") // Carla owes Javi 50

	resp, _ := do(t, javi, http.MethodPost, ts, "/forgive", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/shared" {
		t.Fatalf("POST /forgive = %d -> %q, want 303 -> /shared", resp.StatusCode, resp.Header.Get("Location"))
	}

	if _, body := getAs(t, javi, ts, "/shared"); !strings.Contains(body, "All square") {
		t.Errorf("after forgiving everything the balance should be square; got:\n%s", body)
	}
	fs, err := st.ListForgivenesses(context.Background())
	if err != nil || len(fs) != 1 || fs[0].Amount != 5000 || !fs[0].Date.Equal(testToday) {
		t.Fatalf("forgivenesses = %+v, %v; want one of 5000 dated %v", fs, err, testToday)
	}
}

// Javi may forgive less than the whole balance and date it on another day.
func TestForgivePartialAmountAndDate(t *testing.T) {
	ts, st := newTwoUserServer(t)
	javi := as(t, ts, javiPassword)
	addShared(t, javi, ts, "100 alquiler") // Carla owes Javi 50

	resp, _ := do(t, javi, http.MethodPost, ts, "/forgive", url.Values{"amount": {"20,5"}, "date": {"2026-07-15"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /forgive = %d, want 303", resp.StatusCode)
	}

	if _, body := getAs(t, javi, ts, "/shared"); !strings.Contains(body, "Carla owes Javi €29.50") {
		t.Errorf("50 owed, 20.50 forgiven: want 'Carla owes Javi €29.50'; got:\n%s", body)
	}
	fs, _ := st.ListForgivenesses(context.Background())
	wantDate := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	if len(fs) != 1 || fs[0].Amount != 2050 || !fs[0].Date.Equal(wantDate) {
		t.Fatalf("forgivenesses = %+v, want one of 2050 dated %v", fs, wantDate)
	}
}

// Carla cannot forgive anything: the route answers as if it did not exist, and
// nothing is stored.
func TestCarlaCannotForgive(t *testing.T) {
	ts, st := newTwoUserServer(t)
	javi := as(t, ts, javiPassword)
	carla := as(t, ts, carlaPassword)
	addShared(t, carla, ts, "100 viaje") // Javi owes Carla 50: the tempting direction
	addShared(t, javi, ts, "100 alquiler")
	addShared(t, javi, ts, "100 cena") // now Carla owes Javi 50

	resp, _ := do(t, carla, http.MethodPost, ts, "/forgive", url.Values{})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Carla POST /forgive = %d, want 404", resp.StatusCode)
	}
	if fs, _ := st.ListForgivenesses(context.Background()); len(fs) != 0 {
		t.Errorf("nothing should be stored; got %+v", fs)
	}
}

// Forgiving needs Carla to owe Javi, and a sensible amount and date: anything
// else is refused with a 422 and stores nothing.
func TestForgiveRefusals(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, ts *httptest.Server, javi, carla *http.Client)
		form  url.Values
	}{
		{
			name:  "nothing owed",
			setup: func(*testing.T, *httptest.Server, *http.Client, *http.Client) {},
			form:  url.Values{},
		},
		{
			name: "javi is the one who owes",
			setup: func(t *testing.T, ts *httptest.Server, _, carla *http.Client) {
				addShared(t, carla, ts, "100 viaje")
			},
			form: url.Values{},
		},
		{name: "zero amount", form: url.Values{"amount": {"0"}}},
		{name: "not a number", form: url.Values{"amount": {"abc"}}},
		{name: "negative amount", form: url.Values{"amount": {"-5"}}},
		{name: "more than the balance", form: url.Values{"amount": {"50.01"}}},
		{name: "future date", form: url.Values{"date": {"2026-07-22"}}},
		{name: "malformed date", form: url.Values{"date": {"22/07/2026"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, st := newTwoUserServer(t)
			javi := as(t, ts, javiPassword)
			carla := as(t, ts, carlaPassword)
			if tt.setup != nil {
				tt.setup(t, ts, javi, carla)
			} else {
				addShared(t, javi, ts, "100 alquiler") // Carla owes Javi 50
			}

			resp, _ := do(t, javi, http.MethodPost, ts, "/forgive", tt.form)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("POST /forgive = %d, want 422", resp.StatusCode)
			}
			if fs, _ := st.ListForgivenesses(context.Background()); len(fs) != 0 {
				t.Errorf("nothing should be stored; got %+v", fs)
			}
		})
	}
}

// A Forgiveness is listed in the Shared tab for both users, never in Javi's
// ordinary tab, never moves the day's total, and is neither an expense nor an
// income (it does not count in Javi's totals).
func TestForgivenessIsListedInSharedTabOnly(t *testing.T) {
	ts, st := newTwoUserServer(t)
	javi := as(t, ts, javiPassword)
	carla := as(t, ts, carlaPassword)
	addShared(t, javi, ts, "100 alquiler")
	do(t, javi, http.MethodPost, ts, "/forgive", url.Values{"amount": {"30"}})

	for name, c := range map[string]*http.Client{"javi": javi, "carla": carla} {
		_, body := getAs(t, c, ts, "/shared")
		if !strings.Contains(body, "Javi forgave") || !strings.Contains(body, "€30.00") {
			t.Errorf("%s should see the forgiveness in the Shared tab; got:\n%s", name, body)
		}
		if !strings.Contains(body, `title="Spent this day">€100.00<`) {
			t.Errorf("%s: the day total should stay the 100 spent; got:\n%s", name, body)
		}
	}
	if _, home := getAs(t, javi, ts, "/"); strings.Contains(home, "forgave") {
		t.Errorf("the ordinary tab must not list it; got:\n%s", home)
	}

	if es := listExpenses(t, st); len(es) != 1 {
		t.Errorf("expenses = %d, want only the shared one", len(es))
	}
	if is, _ := st.ListIncomes(context.Background()); len(is) != 0 {
		t.Errorf("incomes = %+v, want none", is)
	}
}

// The Forgive form is offered only to Javi, only while Carla owes him, prefilled
// with the whole balance and today's date.
func TestForgiveFormVisibility(t *testing.T) {
	ts, _ := newTwoUserServer(t)
	javi := as(t, ts, javiPassword)
	carla := as(t, ts, carlaPassword)
	const offered = `action="/forgive"`

	if _, body := getAs(t, javi, ts, "/shared"); strings.Contains(body, offered) {
		t.Errorf("nothing owed: no Forgive form expected; got:\n%s", body)
	}

	addShared(t, javi, ts, "100.50 alquiler") // Carla owes Javi 50.25
	_, body := getAs(t, javi, ts, "/shared")
	for _, want := range []string{offered, `name="amount" value="50.25"`, `name="date" value="2026-07-21"`, `max="2026-07-21"`} {
		if !strings.Contains(body, want) {
			t.Errorf("Carla owes Javi: Forgive form missing %q; got:\n%s", want, body)
		}
	}
	if _, body := getAs(t, carla, ts, "/shared"); strings.Contains(body, offered) {
		t.Errorf("Carla must not be offered Forgive; got:\n%s", body)
	}

	addShared(t, carla, ts, "300 viaje") // Javi now owes Carla 100
	if _, body := getAs(t, javi, ts, "/shared"); strings.Contains(body, offered) {
		t.Errorf("Javi owes: no Forgive form expected; got:\n%s", body)
	}
}

// Javi can undo a Forgiveness by deleting it, which brings the debt back; Carla
// cannot, and nobody can edit one.
func TestDeleteForgiveness(t *testing.T) {
	ts, st := newTwoUserServer(t)
	javi := as(t, ts, javiPassword)
	carla := as(t, ts, carlaPassword)
	addShared(t, javi, ts, "100 alquiler")
	do(t, javi, http.MethodPost, ts, "/forgive", url.Values{})
	fs, _ := st.ListForgivenesses(context.Background())
	if len(fs) != 1 {
		t.Fatalf("setup: forgivenesses = %+v", fs)
	}
	path := "/delete/forgiveness/" + itoa(fs[0].ID)

	if resp, _ := do(t, carla, http.MethodPost, ts, path, url.Values{}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("Carla deleting = %d, want 404", resp.StatusCode)
	}
	if _, body := getAs(t, carla, ts, "/shared"); strings.Contains(body, path) {
		t.Errorf("Carla must not be offered the delete control; got:\n%s", body)
	}
	for _, p := range []string{"/edit/forgiveness/" + itoa(fs[0].ID)} {
		if resp, _ := getAs(t, javi, ts, p); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404 (never editable)", p, resp.StatusCode)
		}
		if resp, _ := do(t, javi, http.MethodPost, ts, p, url.Values{"raw": {"5 x"}}); resp.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404 (never editable)", p, resp.StatusCode)
		}
	}
	if _, body := getAs(t, javi, ts, "/shared"); !strings.Contains(body, path) {
		t.Errorf("Javi should be offered the delete control %s; got:\n%s", path, body)
	}

	resp, _ := do(t, javi, http.MethodPost, ts, path, url.Values{})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/shared" {
		t.Fatalf("Javi deleting = %d -> %q, want 303 -> /shared", resp.StatusCode, resp.Header.Get("Location"))
	}
	if _, body := getAs(t, javi, ts, "/shared"); !strings.Contains(body, "Carla owes Javi €50.00") {
		t.Errorf("deleting should bring the debt back; got:\n%s", body)
	}
	if resp, _ := do(t, javi, http.MethodPost, ts, path, url.Values{}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("deleting again = %d, want 404", resp.StatusCode)
	}
}
