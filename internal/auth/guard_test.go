package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/emepetres/life-ledger/internal/auth"
)

func hash(t *testing.T, pw string) []byte {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func twoUserGuard(t *testing.T) *auth.Guard {
	t.Helper()
	g, err := auth.NewGuard(auth.Config{
		PasswordHash:      hash(t, "javi-pw"),
		CarlaPasswordHash: hash(t, "carla-pw"),
		SessionSecret:     []byte("secret"),
	})
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	return g
}

// login posts a password and returns who got in plus the session cookie.
func login(g *auth.Guard, pw string) (auth.User, *http.Cookie, error) {
	rec := httptest.NewRecorder()
	u, err := g.Login(rec, httptest.NewRequest(http.MethodPost, "/login", nil), pw)
	if cs := rec.Result().Cookies(); len(cs) == 1 {
		return u, cs[0], err
	}
	return u, nil, err
}

// Which password was entered decides who is logged in — there is no selector, so
// Carla can't pick Javi.
func TestLoginIdentifiesUserByPassword(t *testing.T) {
	g := twoUserGuard(t)
	if u, _, err := login(g, "javi-pw"); err != nil || u != auth.Javi {
		t.Errorf("javi-pw: user %q, err %v", u, err)
	}
	if u, _, err := login(g, "carla-pw"); err != nil || u != auth.Carla {
		t.Errorf("carla-pw: user %q, err %v", u, err)
	}
	if _, c, err := login(g, "nope"); err != auth.ErrInvalidCredentials || c != nil {
		t.Errorf("wrong password: err %v, cookie %v; want ErrInvalidCredentials and no cookie", err, c)
	}
}

// The signed cookie carries the identity back to the middleware's handlers.
func TestMiddlewareExposesUserFromCookie(t *testing.T) {
	g := twoUserGuard(t)
	var seen auth.User
	h := g.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = auth.UserFrom(r.Context())
	}), func(*http.Request) bool { return false })

	for _, tc := range []struct {
		pw   string
		want auth.User
	}{{"javi-pw", auth.Javi}, {"carla-pw", auth.Carla}} {
		_, c, _ := login(g, tc.pw)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || seen != tc.want {
			t.Errorf("%s: status %d, user %q, want 200 and %q", tc.pw, rec.Code, seen, tc.want)
		}
		// Sliding expiry must keep the identity.
		if cs := rec.Result().Cookies(); len(cs) != 1 {
			t.Errorf("%s: want a re-issued cookie, got %d", tc.pw, len(cs))
		} else {
			req2 := httptest.NewRequest(http.MethodGet, "/", nil)
			req2.AddCookie(cs[0])
			h.ServeHTTP(httptest.NewRecorder(), req2)
			if seen != tc.want {
				t.Errorf("%s: identity lost after cookie re-issue: %q", tc.pw, seen)
			}
		}
	}
}

// Carla's password is optional: without it only Javi can log in (the app
// behaves as before the Shared tab).
func TestCarlaPasswordIsOptional(t *testing.T) {
	g, err := auth.NewGuard(auth.Config{PasswordHash: hash(t, "javi-pw"), SessionSecret: []byte("s")})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := login(g, "carla-pw"); err != auth.ErrInvalidCredentials {
		t.Errorf("err = %v, want ErrInvalidCredentials", err)
	}
	if u, _, err := login(g, "javi-pw"); err != nil || u != auth.Javi {
		t.Errorf("javi: %q, %v", u, err)
	}
}

func TestNewGuardRejectsBadCarlaHash(t *testing.T) {
	_, err := auth.NewGuard(auth.Config{
		PasswordHash: hash(t, "a"), CarlaPasswordHash: []byte("not-a-hash"), SessionSecret: []byte("s"),
	})
	if err == nil {
		t.Error("want an error for a malformed Carla hash")
	}
}
