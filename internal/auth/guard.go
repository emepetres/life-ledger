package auth

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Login-rate-limit budget: ~5 attempts per minute per client, then a cooldown
// until the window rolls over (ADR-0004).
const (
	loginMaxAttempts = 5
	loginWindow      = time.Minute
)

// Sentinel errors returned by Login so the caller can map each to the right HTTP
// response without inspecting strings.
var (
	// ErrInvalidCredentials means the submitted password did not match the hash.
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	// ErrRateLimited means too many login attempts came from this client too
	// quickly; the password was not even checked.
	ErrRateLimited = errors.New("auth: too many login attempts")
)

// Config holds the deployment-supplied auth settings.
type Config struct {
	// PasswordHash is the bcrypt hash of the single shared password (never the
	// plaintext), supplied via config/env.
	PasswordHash []byte
	// SessionSecret signs the session cookie. Any length — it is hashed to a
	// fixed-size HMAC key internally. Rotating it logs everyone out.
	SessionSecret []byte
	// Secure sets the session cookie's Secure flag: true in production (HTTPS),
	// false for local http://localhost QA.
	Secure bool
}

// Guard is the auth mechanism: it decides whether a request is authenticated,
// runs the login (rate limit + bcrypt verify + issue cookie) and logout flows,
// and provides middleware that gates the protected routes. It renders no HTML.
type Guard struct {
	hash     []byte
	sessions *Sessions
	limiter  *rateLimiter
}

// NewGuard validates the config and builds a Guard. It fails fast on a missing
// or malformed bcrypt hash (bcrypt.Cost rejects a non-hash) and a missing
// signing secret, so a misconfigured deployment never boots into an unprotected
// or un-loginable state.
func NewGuard(cfg Config) (*Guard, error) {
	if len(cfg.PasswordHash) == 0 {
		return nil, errors.New("auth: password hash is required")
	}
	if _, err := bcrypt.Cost(cfg.PasswordHash); err != nil {
		return nil, fmt.Errorf("auth: password hash is not a valid bcrypt hash: %w", err)
	}
	if len(cfg.SessionSecret) == 0 {
		return nil, errors.New("auth: session secret is required")
	}
	return &Guard{
		hash:     cfg.PasswordHash,
		sessions: newSessions(cfg.SessionSecret, cfg.Secure),
		limiter:  newRateLimiter(loginMaxAttempts, loginWindow),
	}, nil
}

// Authenticated reports whether the request carries a valid session.
func (g *Guard) Authenticated(r *http.Request) bool {
	return g.sessions.Valid(r)
}

// Login runs the login flow for a submitted password. It first spends a per-IP
// rate-limit token (so a flood is turned away before the expensive bcrypt
// compare); an exhausted budget returns ErrRateLimited. A mismatching password
// returns ErrInvalidCredentials. On success it issues the session cookie and
// returns nil. The constant-time compare comes from bcrypt.
func (g *Guard) Login(w http.ResponseWriter, r *http.Request, password string) error {
	if !g.limiter.Allow(clientIP(r)) {
		return ErrRateLimited
	}
	if err := bcrypt.CompareHashAndPassword(g.hash, []byte(password)); err != nil {
		return ErrInvalidCredentials
	}
	g.sessions.Issue(w)
	return nil
}

// Logout clears the session cookie, ending the session.
func (g *Guard) Logout(w http.ResponseWriter) {
	g.sessions.Clear(w)
}

// Middleware gates next: requests for which skip reports true (the login page,
// static assets, the health check) pass straight through; every other request
// needs a valid session or is refused with a redirect to the login page. On a
// valid session it re-issues the cookie so the 30-day expiry slides with use.
func (g *Guard) Middleware(next http.Handler, skip func(*http.Request) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if skip(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !g.sessions.Valid(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		g.sessions.Issue(w) // slide the expiry forward on active use.
		next.ServeHTTP(w, r)
	})
}

// clientIP is the rate-limit key: the request's source address. Behind the
// platform ingress the leftmost X-Forwarded-For entry is the real client, so it
// is preferred when present; otherwise the connection's RemoteAddr host is used.
// X-Forwarded-For is client-spoofable, but the limit is only a light speed bump
// on top of bcrypt (ADR-0004), so that is an accepted trade-off.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(xff)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
