package service

import "testing"

// The agent dial-home recovery limiter must be independent of the operator
// Register/Migrate limiter, and keyed per token, so a single noisy agent (e.g.
// a stale client behind a shared NAT, dial-home recovering every few seconds)
// cannot 429 a sibling machine's migrate. Regression for the observed lockout.
func TestRecoverLimiter_IsolatedFromMigrateAndPerToken(t *testing.T) {
	s := NewBootstrapService(nil)

	// Saturate one agent's recovery bucket (loginRateLimit allowed, next denied).
	var lastAllowed bool
	for i := 0; i < loginRateLimit+1; i++ {
		lastAllowed = s.AllowRecoverAttempt("agent-token-A")
	}
	if lastAllowed {
		t.Fatalf("recovery limiter never tripped after %d attempts", loginRateLimit+1)
	}

	// The operator migrate/bootstrap limiter (per IP) must be untouched — even
	// for the same source IP the noisy agent used.
	if !s.AllowAttempt("128.97.82.111") {
		t.Fatal("migrate/bootstrap limiter starved by recovery traffic — buckets not decoupled")
	}

	// A different agent's recovery must not be throttled by the first's.
	if !s.AllowRecoverAttempt("agent-token-B") {
		t.Fatal("recovery limiter not keyed per token — one agent throttled another")
	}
}
