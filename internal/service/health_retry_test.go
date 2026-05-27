package service

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/smalex-z/gopher/internal/db"
)

type fakePusher struct {
	calls    atomic.Int32
	lastID   atomic.Value
	returnFn func() error
}

func (f *fakePusher) RetryPendingConfigPush(m *db.Machine) error {
	f.calls.Add(1)
	f.lastID.Store(m.ID)
	if f.returnFn != nil {
		return f.returnFn()
	}
	return nil
}

func TestMaybeRetryConfigPush_FiresWhenFlagSetAndPusherWired(t *testing.T) {
	pusher := &fakePusher{}
	h := NewHealthService(false)
	h.SetConfigPusher(pusher)

	m := &db.Machine{ID: "m1", Name: "alpha", ConfigPushPending: true}
	h.maybeRetryConfigPush(m)

	// Push runs in a goroutine — give it up to 500ms to fire. If it doesn't,
	// the wiring is broken.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if pusher.calls.Load() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := pusher.calls.Load(); got != 1 {
		t.Fatalf("expected 1 push call, got %d", got)
	}
	if got, _ := pusher.lastID.Load().(string); got != "m1" {
		t.Errorf("pusher called with wrong machine id: %q", got)
	}
}

func TestMaybeRetryConfigPush_NoOpWhenFlagUnset(t *testing.T) {
	// The common case — most machines never had a push failure. The retry
	// must not fire, otherwise every 60s health tick would re-push every
	// machine and we'd churn rathole-client's notify watcher needlessly.
	pusher := &fakePusher{}
	h := NewHealthService(false)
	h.SetConfigPusher(pusher)

	m := &db.Machine{ID: "m2", ConfigPushPending: false}
	h.maybeRetryConfigPush(m)

	time.Sleep(100 * time.Millisecond) // give a goroutine a chance, if one were spawned
	if got := pusher.calls.Load(); got != 0 {
		t.Errorf("expected 0 push calls when flag unset, got %d", got)
	}
}

func TestMaybeRetryConfigPush_NoOpWhenPusherUnset(t *testing.T) {
	// HealthService can run without a configPusher wired (tests, older
	// deployments). maybeRetryConfigPush must be a no-op in that case
	// rather than nil-pointer-dereference on the next reconnect.
	h := NewHealthService(false)
	m := &db.Machine{ID: "m3", ConfigPushPending: true}
	// If this panics the test fails; the assertion is implicit.
	h.maybeRetryConfigPush(m)
}
