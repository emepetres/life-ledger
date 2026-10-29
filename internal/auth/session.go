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

// User is who is logged in: the owner (Javi) or the partner (Carla). Which
// password was entered decides it, and the signed cookie carries it as its
// payload; the HMAC and the embedded timestamp are what make it unforgeable.
type User string

const (
	Javi  User = "javi"
	Carla User = "carla"
)

// legacySessionValue is the payload of cookies issued before users existed, when
// the one shared password was Javi's: they stay valid, and read as Javi.
const legacySessionValue = "1"

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

// Issue writes a fresh session cookie for user with a sliding 30-day expiry.
// Called on a successful login and again on every authenticated request, so
// continued use keeps extending the window. HttpOnly and SameSite=Lax are always
// set; Secure follows the configured environment.
func (s *Sessions) Issue(w http.ResponseWriter, user User) {
	encoded, err := s.codec.Encode(cookieName, string(user))
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

// User reports who the request's session cookie belongs to, and whether it is a
// well-signed, unexpired cookie at all. A missing, tampered, wrong-key, too-old,
// or unrecognised-payload cookie all read as not ok.
func (s *Sessions) User(r *http.Request) (User, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return "", false
	}
	var v string
	if s.codec.Decode(cookieName, c.Value, &v) != nil {
		return "", false
	}
	switch User(v) {
	case Javi, Carla:
		return User(v), true
	}
	if v == legacySessionValue {
		return Javi, true
	}
	return "", false
}

// Valid reports whether the request carries a valid session cookie.
func (s *Sessions) Valid(r *http.Request) bool {
	_, ok := s.User(r)
	return ok
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
