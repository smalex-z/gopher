package service

import "testing"

// TestLoginRateLimiterStop verifies the cleanup goroutine has a working stop
// path (it previously ran forever with no way to halt it) and that Stop is
// idempotent.
func TestLoginRateLimiterStop(t *testing.T) {
	rl := newLoginRateLimiter()

	// Basic behaviour still works.
	for i := 0; i < loginRateLimit; i++ {
		if !rl.record("1.2.3.4") {
			t.Fatalf("attempt %d should be allowed", i)
		}
	}
	if rl.record("1.2.3.4") {
		t.Fatal("attempt past the limit should be denied")
	}

	// Stop halts the cleanup goroutine and is safe to call twice.
	rl.Stop()
	rl.Stop()
}
