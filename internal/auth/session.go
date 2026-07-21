// Package auth guards the ledger with real, in-app authentication (ADR-0004):
// a single shared bcrypt-hashed password behind a login form, a signed stateless
// session cookie (HMAC, no session table), protective middleware, and a light
// per-IP rate limit on the login POST. It is a self-contained mechanism — it
// makes the security decisions and manages the cookie, but renders no HTML; the
// server package owns the login page and wires the pieces together.
package auth

import (
	"crypto/sha256"
	"net/http"
	"time"

	"github.com/gorilla/securecookie"
)

// cookieName is the session cookie's name. The "ll_" prefix namespaces it to
// Life Ledger so it can't collide with anything else on the host.
const cookieName = "ll_session"

// sessionMaxAge caps a stolen-cookie window without forcing constant re-login
// (ADR-0004). It is applied both to the signed value (rejected on decode once
// older) and to the cookie's own Max-Age, and slides: every authenticated
// request re-issues a fresh cookie, so active use never expires.
const sessionMaxAge = 30 * 24 * time.Hour

// sessionValue is the constant payload carried in the signed cookie. The single
// shared password means the session needs no per-user identity; the value exists
// only so there is something to sign and version. The HMAC and the embedded
// timestamp — not this string — are what make the cookie unforgeable.
const sessionValue = "1"

// Sessions issues and validates the signed, stateless session cookie. It wraps a
// gorilla/securecookie codec (a vetted HMAC implementation) keyed by a signing
// secret; rotating that secret invalidates every outstanding cookie, which is
// how "log out everywhere" is achieved for a single user.
type Sessions struct {
	codec  *securecookie.SecureCookie
	secure bool
}

// newSessions builds a Sessions from a signing secret of any length. The secret
// is hashed to a fixed 32-byte HMAC key with SHA-256, so any passphrase-style
// secret works and the key fed to the codec is always the right size. secure
// sets the cookie's Secure flag — on in production, off for http://localhost so
// local QA over plain HTTP still receives the cookie (ADR-0004).
func newSessions(signingSecret []byte, secure bool) *Sessions {
	key := sha256.Sum256(signingSecret)
	codec := securecookie.New(key[:], nil) // HMAC-sign only; no payload to encrypt.
	codec.MaxAge(int(sessionMaxAge / time.Second))
	return &Sessions{codec: codec, secure: secure}
}

// Issue writes a fresh session cookie with a sliding 30-day expiry. Called on a
// successful login and again on every authenticated request, so continued use
// keeps extending the window. HttpOnly and SameSite=Lax are always set; Secure
// follows the configured environment.
func (s *Sessions) Issue(w http.ResponseWriter) {
	encoded, err := s.codec.Encode(cookieName, sessionValue)
	if err != nil {
		// Encoding a constant with a valid key does not fail in practice; if it
		// somehow did, simply issue no cookie rather than a malformed one.
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    encoded,
		Path:     "/",
		MaxAge:   int(sessionMaxAge / time.Second), // >0 => persistent cookie.
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// Valid reports whether the request carries a well-signed, unexpired session
// cookie. A missing, tampered, wrong-key, or too-old cookie all read as false.
func (s *Sessions) Valid(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	var v string
	return s.codec.Decode(cookieName, c.Value, &v) == nil
}

// Clear expires the session cookie, ending the session on the client. The flags
// mirror Issue so the browser matches and replaces the existing cookie.
func (s *Sessions) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1, // delete now.
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
}
