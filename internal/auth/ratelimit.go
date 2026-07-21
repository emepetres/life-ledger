package auth

import (
	"sync"
	"time"
)

// rateLimiter is a light per-key fixed-window counter guarding the login POST
// against password guessing (ADR-0004): up to max attempts per window, then a
// cooldown until the window rolls over. It is deliberately not a hard lockout —
// bcrypt is the real brute-force defence and a lockout would let anyone lock the
// single user out. In-memory is fine for the single-instance deployment.
type rateLimiter struct {
	mu     sync.Mutex
	hits   map[string]*hitWindow
	max    int
	window time.Duration
	// now is injected so tests can advance time across the window boundary;
	// production uses the wall clock.
	now func() time.Time
}

// hitWindow tracks one key's attempt count within the current window.
type hitWindow struct {
	count int
	// resetAt is when the window rolls over and the count returns to zero.
	resetAt time.Time
}

// sweepThreshold bounds the attempt map: once it holds this many keys, the next
// Allow first drops every key whose window has elapsed. Without this a flood of
// distinct (and, via a spoofable X-Forwarded-For, forgeable) source addresses
// would grow the map without limit. Each live window is tiny and short, so the
// map settles back to the count of genuinely-active clients.
const sweepThreshold = 1024

// newRateLimiter builds a limiter allowing max attempts per window per key.
func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		hits:   make(map[string]*hitWindow),
		max:    max,
		window: window,
		now:    time.Now,
	}
}

// Allow records an attempt for key and reports whether it is permitted. The
// first attempt in a window and every attempt up to max return true; once max is
// reached within the window the rest return false until the window rolls over.
func (rl *rateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	if len(rl.hits) >= sweepThreshold {
		for k, hw := range rl.hits {
			if now.After(hw.resetAt) {
				delete(rl.hits, k)
			}
		}
	}
	w := rl.hits[key]
	if w == nil || now.After(w.resetAt) {
		// Fresh window (first-ever attempt or the previous window has elapsed).
		rl.hits[key] = &hitWindow{count: 1, resetAt: now.Add(rl.window)}
		return true
	}
	if w.count >= rl.max {
		return false
	}
	w.count++
	return true
}
