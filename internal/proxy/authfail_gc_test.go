package proxy

import (
	"testing"
	"time"
)

// recordAuthFail must sweep expired windows: entries used to be deleted only
// on a successful login, so distributed failures (each IP failing once)
// grew the map without bound.
func TestRecordAuthFail_SweepsExpiredEntries(t *testing.T) {
	m, err := NewMiddleware()
	if err != nil {
		t.Fatalf("NewMiddleware: %v", err)
	}

	past := time.Now().Add(-time.Minute)
	m.authMu.Lock()
	for _, ip := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"} {
		m.authFail[ip] = &authFailState{count: 1, reset: past}
	}
	live := "198.51.100.9"
	m.authFail[live] = &authFailState{count: 2, reset: time.Now().Add(time.Hour)}
	m.authMu.Unlock()

	m.recordAuthFail("203.0.113.1")

	m.authMu.Lock()
	defer m.authMu.Unlock()
	if len(m.authFail) != 2 {
		t.Fatalf("map has %d entries after sweep, want 2 (live + new)", len(m.authFail))
	}
	if st := m.authFail[live]; st == nil || st.count != 2 {
		t.Error("unexpired entry must survive the sweep")
	}
	if m.authFail["203.0.113.1"] == nil {
		t.Error("the just-recorded failure must be present")
	}
}
