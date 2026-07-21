package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// issuedCookie issues a session cookie into a recorder and returns it, failing
// the test if none was set.
func issuedCookie(t *testing.T, s *Sessions) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Issue(rec)
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("Issue set %d cookies, want 1", len(cookies))
	}
	return cookies[0]
}

// requestWith returns a request carrying the given cookie.
func requestWith(c *http.Cookie) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if c != nil {
		r.AddCookie(c)
	}
	return r
}

func TestSessionIssueSetsSecurityFlags(t *testing.T) {
	c := issuedCookie(t, newSessions([]byte("secret"), true))

	if !c.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("session cookie SameSite = %v, want Lax", c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("session cookie Path = %q, want /", c.Path)
	}
	// 30-day sliding, persistent cookie.
	wantMaxAge := int(sessionMaxAge.Seconds())
	if c.MaxAge != wantMaxAge {
		t.Errorf("session cookie MaxAge = %d, want %d (30 days)", c.MaxAge, wantMaxAge)
	}
}

// Secure is env-conditional: on in prod, off for http://localhost.
func TestSessionSecureFlagIsConditional(t *testing.T) {
	if c := issuedCookie(t, newSessions([]byte("secret"), true)); !c.Secure {
		t.Error("Secure must be set when configured on (production)")
	}
	if c := issuedCookie(t, newSessions([]byte("secret"), false)); c.Secure {
		t.Error("Secure must be off when configured off (local http://localhost)")
	}
}

func TestSessionValidAcceptsIssuedCookie(t *testing.T) {
	s := newSessions([]byte("secret"), false)
	if !s.Valid(requestWith(issuedCookie(t, s))) {
		t.Error("a freshly issued cookie should be valid")
	}
}

func TestSessionValidRejectsMissingCookie(t *testing.T) {
	s := newSessions([]byte("secret"), false)
	if s.Valid(requestWith(nil)) {
		t.Error("a request with no session cookie must not be valid")
	}
}

// A tampered value must fail the HMAC check.
func TestSessionValidRejectsTamperedCookie(t *testing.T) {
	s := newSessions([]byte("secret"), false)
	c := issuedCookie(t, s)
	c.Value += "x"
	if s.Valid(requestWith(c)) {
		t.Error("a tampered session cookie must not be valid")
	}
}

// A cookie signed with a different key must be rejected — this is the basis of
// "log out everywhere = rotate the signing key".
func TestSessionValidRejectsWrongKey(t *testing.T) {
	issuer := newSessions([]byte("old-secret"), false)
	verifier := newSessions([]byte("new-secret"), false)
	if verifier.Valid(requestWith(issuedCookie(t, issuer))) {
		t.Error("a cookie signed with a rotated-away key must not be valid")
	}
}

func TestSessionClearExpiresCookie(t *testing.T) {
	s := newSessions([]byte("secret"), false)
	rec := httptest.NewRecorder()
	s.Clear(rec)
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("Clear set %d cookies, want 1", len(cookies))
	}
	if cookies[0].MaxAge >= 0 {
		t.Errorf("Clear MaxAge = %d, want negative (delete)", cookies[0].MaxAge)
	}
	// The cleared cookie must not validate as a live session.
	if s.Valid(requestWith(cookies[0])) {
		t.Error("a cleared cookie must not be a valid session")
	}
}
