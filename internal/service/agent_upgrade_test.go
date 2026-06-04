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
	up := &fakeUpgrader{}
	h := NewHealthService(false)
	h.SetAgentUpgrader(up)

	m := &db.Machine{ID: "m1", Name: "alpha"}
	h.maybeAutoUpgradeAgent(m, "test")
	waitForCalls(t, up.calls.Load, 1)
	if got := up.lastID.Load(); got != "m1" {
		t.Errorf("upgraded wrong machine: %v", got)
	}

	// A second call within the cooldown must NOT fire again.
	h.maybeAutoUpgradeAgent(m, "test again")
	time.Sleep(100 * time.Millisecond)
	if got := up.calls.Load(); got != 1 {
		t.Errorf("cooldown breached: expected 1 call, got %d", got)
	}

	// Past the cooldown, it fires again.
	h.mu.Lock()
	h.lastAgentUpgrade["m1"] = time.Now().Add(-agentUpgradeCooldown - time.Minute)
	h.mu.Unlock()
	h.maybeAutoUpgradeAgent(m, "after cooldown")
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
