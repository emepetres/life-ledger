package server_test

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/emepetres/life-ledger/internal/auth"
	"github.com/emepetres/life-ledger/internal/server"
	"golang.org/x/crypto/bcrypt"
)

// testPassword is the plaintext the guarded test server authenticates against;
// its bcrypt hash is generated at the cheapest cost so the tests stay fast.
const testPassword = "correct horse"

// newGuardedServer builds the real handler stack with authentication turned on
// (Secure off, matching local http:// QA so Go's cookie jar sends the cookie),
// backed by a fresh temp store and the frozen test clock.
func newGuardedServer(t *testing.T) *httptest.Server {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hashing test password: %v", err)
	}
	guard, err := auth.NewGuard(auth.Config{
		PasswordHash:  hash,
		SessionSecret: []byte("test-signing-secret"),
		Secure:        false,
	})
	if err != nil {
		t.Fatalf("auth.NewGuard: %v", err)
	}
	h, err := server.New(openTempStore(t),
		server.WithClock(func() time.Time { return testToday }),
		server.WithGuard(guard))
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

// noRedirectClient is an unauthenticated client that surfaces the raw redirect
// rather than following it, so a test can assert on the 303 and its Location.
func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// jarClient is a client with a cookie jar, so a session cookie set by a login
// persists across subsequent requests the way a browser would keep it.
func jarClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	return &http.Client{Jar: jar}
}

// login posts the given password with client and returns the response (redirects
// not followed, so the caller can inspect the outcome and any Set-Cookie).
func login(t *testing.T, client *http.Client, ts *httptest.Server, password string) *http.Response {
	t.Helper()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.PostForm(ts.URL+"/login", url.Values{"password": {password}})
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	resp.Body.Close()
	return resp
}

// hasSessionCookie reports whether the response sets a non-empty session cookie.
func hasSessionCookie(resp *http.Response) bool {
	for _, c := range resp.Cookies() {
		if c.Name == "ll_session" && c.Value != "" && c.MaxAge >= 0 {
			return true
		}
	}
	return false
}

// AC: an unauthenticated request to a protected route is refused — redirected to
// the login page, not served the ledger.
func TestUnauthenticatedProtectedRouteRedirectsToLogin(t *testing.T) {
	ts := newGuardedServer(t)
	resp, err := noRedirectClient().Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("unauth GET / status = %d, want %d (redirect)", resp.StatusCode, http.StatusSeeOther)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("unauth GET / Location = %q, want /login", loc)
	}
}

// AC: /health and static assets are reachable without a session (platform probes
// and the login page's stylesheet must load pre-auth).
func TestHealthAndStaticReachableWithoutSession(t *testing.T) {
	ts := newGuardedServer(t)
	client := noRedirectClient()

	for _, path := range []string{"/health", "/static/style.css", "/static/favicon.svg"} {
		resp, err := client.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("unauth GET %s status = %d, want 200", path, resp.StatusCode)
		}
	}
}

// AC: the login page itself is reachable without a session.
func TestLoginPageReachableWithoutSession(t *testing.T) {
	ts := newGuardedServer(t)
	resp, err := noRedirectClient().Get(ts.URL + "/login")
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /login status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Log in") {
		t.Errorf("login page missing the login form; got:\n%s", body)
	}
}

// AC: login with the correct password sets the session cookie, and that session
// then reaches a protected route.
func TestLoginCorrectPasswordSetsSessionAndGrantsAccess(t *testing.T) {
	ts := newGuardedServer(t)
	client := jarClient(t)

	resp := login(t, client, ts, testPassword)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("login Location = %q, want /", loc)
	}
	if !hasSessionCookie(resp) {
		t.Fatal("login with correct password must set a session cookie")
	}

	// The session now reaches the ledger.
	client.CheckRedirect = nil
	home, err := client.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET / after login: %v", err)
	}
	body, _ := io.ReadAll(home.Body)
	home.Body.Close()
	if home.StatusCode != http.StatusOK {
		t.Fatalf("GET / after login status = %d, want 200", home.StatusCode)
	}
	if !strings.Contains(string(body), "Life Ledger") {
		t.Errorf("authenticated home should render the ledger; got:\n%s", body)
	}
}

// AC: the session cookie carries the mandated security flags — HttpOnly,
// SameSite=Lax, and a 30-day persistent Max-Age.
func TestLoginCookieHasSecurityFlags(t *testing.T) {
	ts := newGuardedServer(t)
	resp := login(t, jarClient(t), ts, testPassword)

	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "ll_session" {
			session = c
		}
	}
	if session == nil {
		t.Fatal("no session cookie set on login")
	}
	if !session.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if session.SameSite != http.SameSiteLaxMode {
		t.Errorf("session cookie SameSite = %v, want Lax", session.SameSite)
	}
	if wantMaxAge := int((30 * 24 * time.Hour).Seconds()); session.MaxAge != wantMaxAge {
		t.Errorf("session cookie MaxAge = %d, want %d (30-day persistent)", session.MaxAge, wantMaxAge)
	}
}

// AC: the 30-day expiry slides — an authenticated request re-issues the session
// cookie so continued use keeps extending the window.
func TestSessionSlidesOnAuthenticatedRequest(t *testing.T) {
	ts := newGuardedServer(t)
	client := jarClient(t)
	login(t, client, ts, testPassword)

	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET / after login: %v", err)
	}
	resp.Body.Close()
	if !hasSessionCookie(resp) {
		t.Error("an authenticated request should re-issue the session cookie (sliding expiry)")
	}
}

// AC: a wrong password fails — no session cookie, and protected routes stay out
// of reach.
func TestLoginWrongPasswordFails(t *testing.T) {
	ts := newGuardedServer(t)
	client := jarClient(t)

	resp := login(t, client, ts, "wrong password")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong-password login status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	if hasSessionCookie(resp) {
		t.Error("a wrong password must not set a session cookie")
	}

	// Still locked out.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	home, err := client.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	home.Body.Close()
	if home.StatusCode != http.StatusSeeOther {
		t.Errorf("after a failed login, GET / status = %d, want redirect to login", home.StatusCode)
	}
}

// AC: the logout route clears the session cookie and ends access.
func TestLogoutClearsCookie(t *testing.T) {
	ts := newGuardedServer(t)
	client := jarClient(t)
	login(t, client, ts, testPassword)

	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.PostForm(ts.URL+"/logout", url.Values{})
	if err != nil {
		t.Fatalf("POST /logout: %v", err)
	}
	resp.Body.Close()

	// The logout response expires the cookie.
	var cleared bool
	for _, c := range resp.Cookies() {
		if c.Name == "ll_session" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout must clear the session cookie (negative Max-Age)")
	}

	// Access is gone: the jar has dropped the cookie, so home redirects to login.
	home, err := client.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET / after logout: %v", err)
	}
	home.Body.Close()
	if home.StatusCode != http.StatusSeeOther {
		t.Errorf("after logout, GET / status = %d, want redirect to login", home.StatusCode)
	}
}

// AC: a per-IP rate limit kicks in after ~5 rapid login attempts — the 6th is
// refused with 429 rather than another password check.
func TestLoginRateLimitedAfterRapidAttempts(t *testing.T) {
	ts := newGuardedServer(t)
	client := jarClient(t)

	// Five wrong attempts are all still processed as bad-password (401)...
	for i := 1; i <= 5; i++ {
		if resp := login(t, client, ts, "wrong password"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", i, resp.StatusCode)
		}
	}
	// ...the sixth is turned away by the rate limit.
	if resp := login(t, client, ts, "wrong password"); resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("6th rapid attempt status = %d, want %d (rate limited)", resp.StatusCode, http.StatusTooManyRequests)
	}
}
