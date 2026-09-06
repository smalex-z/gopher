package service

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/smalex-z/gopher/internal/db"
)

type fakeUpgrader struct {
	calls  atomic.Int32
	lastID atomic.Value
}

func (f *fakeUpgrader) UpgradeAgent(m *db.Machine) error {
	f.calls.Add(1)
	f.lastID.Store(m.ID)
	return nil
}

func waitForCalls(t *testing.T, get func() int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if get() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected %d upgrade calls, got %d", want, get())
}

func TestMaybeAutoUpgradeAgent_FiresAndThrottles(t *testing.T) {
	// Deterministic: no jitter so the first attempt fires immediately.
	prev := agentUpgradeJitterFn
	agentUpgradeJitterFn = func() time.Duration { return 0 }
	defer func() { agentUpgradeJitterFn = prev }()

	up := &fakeUpgrader{}
	h := NewHealthService(false)
	h.SetAgentUpgrader(up)

	m := &db.Machine{ID: "m1", Name: "alpha"}
	h.maybeAutoUpgradeAgent(m, "test")
	waitForCalls(t, up.calls.Load, 1)
	if got := up.lastID.Load(); got != "m1" {
		t.Errorf("upgraded wrong machine: %v", got)
	}

	// A second call within the retry interval must NOT fire again.
	h.maybeAutoUpgradeAgent(m, "test again")
	time.Sleep(100 * time.Millisecond)
	if got := up.calls.Load(); got != 1 {
		t.Errorf("retry interval breached: expected 1 call, got %d", got)
	}

	// Past the retry interval, it fires again.
	h.mu.Lock()
	h.agentUpgrades["m1"].last = time.Now().Add(-agentUpgradeMaxRetry - time.Minute)
	h.mu.Unlock()
	h.maybeAutoUpgradeAgent(m, "after backoff")
	waitForCalls(t, up.calls.Load, 2)
}

func TestAgentUpgradeRetryInterval_BacksOff(t *testing.T) {
	// Untouched / first attempt: no wait.
	if got := agentUpgradeRetryInterval(0); got != 0 {
		t.Errorf("attempts=0 should be 0, got %v", got)
	}
	// 90s, 180s, 360s, … doubling each attempt.
	if got := agentUpgradeRetryInterval(1); got != agentUpgradeMinRetry {
		t.Errorf("attempts=1 = %v, want %v", got, agentUpgradeMinRetry)
	}
	if got := agentUpgradeRetryInterval(2); got != 2*agentUpgradeMinRetry {
		t.Errorf("attempts=2 = %v, want %v", got, 2*agentUpgradeMinRetry)
	}
	// Never exceeds the ceiling.
	if got := agentUpgradeRetryInterval(20); got != agentUpgradeMaxRetry {
		t.Errorf("attempts=20 = %v, want ceiling %v", got, agentUpgradeMaxRetry)
	}
}

func TestClearAgentUpgradeState_ResetsBackoff(t *testing.T) {
	prev := agentUpgradeJitterFn
	agentUpgradeJitterFn = func() time.Duration { return 0 }
	defer func() { agentUpgradeJitterFn = prev }()

	up := &fakeUpgrader{}
	h := NewHealthService(false)
	h.SetAgentUpgrader(up)

	m := &db.Machine{ID: "m1", Name: "alpha"}
	h.maybeAutoUpgradeAgent(m, "first")
	waitForCalls(t, up.calls.Load, 1)

	// Agent reached target → state cleared → an immediate later bump fires again
	// without waiting out the backoff.
	h.clearAgentUpgradeState("m1")
	h.maybeAutoUpgradeAgent(m, "new bump")
	waitForCalls(t, up.calls.Load, 2)
}

func TestMaybeAutoUpgradeAgent_NoUpgraderIsNoop(t *testing.T) {
	h := NewHealthService(false) // no upgrader wired
	// Must not panic or block.
	h.maybeAutoUpgradeAgent(&db.Machine{ID: "m1"}, "test")
}

func TestIsAgentProtocolSkew(t *testing.T) {
	skews := []string{
		`rpc error: code = Unavailable desc = connection error: error reading server preface: http2: failed reading the frame payload: http2: frame too large, note that the frame header looked like an HTTP/1.1 header`,
		`http2: frame too large`,
		`error reading server preface`,
	}
	for _, msg := range skews {
		if !isAgentProtocolSkew(errors.New(msg)) {
			t.Errorf("expected skew for: %s", msg)
		}
	}
	notSkews := []string{
		`rpc error: code = Unavailable desc = connection refused`,
		`context deadline exceeded`,
		`agent GetStatus: some other failure`,
	}
	for _, msg := range notSkews {
		if isAgentProtocolSkew(errors.New(msg)) {
			t.Errorf("false positive skew for: %s", msg)
		}
	}
}
