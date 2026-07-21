package auth

import (
	"strconv"
	"testing"
	"time"
)

// fixedClock is a manually advanced clock so rate-limit windows are exercised
// deterministically without sleeping.
type fixedClock struct{ t time.Time }

func (c *fixedClock) now() time.Time { return c.t }

func TestRateLimiterAllowsUpToMaxThenBlocks(t *testing.T) {
	rl := newRateLimiter(5, time.Minute)

	for i := 1; i <= 5; i++ {
		if !rl.Allow("1.2.3.4") {
			t.Fatalf("attempt %d should be allowed (max 5)", i)
		}
	}
	if rl.Allow("1.2.3.4") {
		t.Error("the 6th attempt within the window should be blocked")
	}
}

// The window rolls over: after it elapses the count resets and attempts are
// allowed again (a cooldown, not a hard lockout).
func TestRateLimiterResetsAfterWindow(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
	rl := newRateLimiter(5, time.Minute)
	rl.now = clk.now

	for i := 0; i < 5; i++ {
		rl.Allow("1.2.3.4")
	}
	if rl.Allow("1.2.3.4") {
		t.Fatal("should be blocked before the window elapses")
	}

	clk.t = clk.t.Add(time.Minute + time.Second)
	if !rl.Allow("1.2.3.4") {
		t.Error("should be allowed again once the window has rolled over")
	}
}

// Once the map grows past the sweep threshold, elapsed windows are evicted so
// memory does not grow without bound under a flood of distinct keys.
func TestRateLimiterSweepsElapsedWindows(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
	rl := newRateLimiter(5, time.Minute)
	rl.now = clk.now

	// Fill the map to the sweep threshold with distinct keys, all in one window.
	for i := 0; i < sweepThreshold; i++ {
		rl.Allow(strconv.Itoa(i))
	}
	if len(rl.hits) != sweepThreshold {
		t.Fatalf("map holds %d keys, want %d before any window elapses", len(rl.hits), sweepThreshold)
	}

	// Advance past the window so every existing entry is now elapsed, then a
	// further attempt triggers the sweep and reclaims them.
	clk.t = clk.t.Add(time.Minute + time.Second)
	rl.Allow("new-client")
	if len(rl.hits) != 1 {
		t.Errorf("after sweep the map holds %d keys, want 1 (only the live client)", len(rl.hits))
	}
}

// Each client's budget is independent — one noisy IP does not spend another's.
func TestRateLimiterIsPerKey(t *testing.T) {
	rl := newRateLimiter(5, time.Minute)

	for i := 0; i < 6; i++ {
		rl.Allow("1.1.1.1")
	}
	if !rl.Allow("2.2.2.2") {
		t.Error("a different client should have its own fresh budget")
	}
}
