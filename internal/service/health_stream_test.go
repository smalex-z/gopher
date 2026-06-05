package service

import (
	"context"
	"testing"

	"github.com/smalex-z/gopher/internal/db"
)

func TestReconcileStreams_CancelsDroppedKeepsLive(t *testing.T) {
	h := NewHealthService(false)
	cancelled := map[string]bool{}
	h.streams["keep"] = func() { cancelled["keep"] = true }
	h.streams["drop"] = func() { cancelled["drop"] = true }

	h.reconcileStreams(map[string]bool{"keep": true})

	if cancelled["keep"] {
		t.Error("live stream should not be cancelled")
	}
	if !cancelled["drop"] {
		t.Error("orphaned stream should be cancelled")
	}
	if _, ok := h.streams["drop"]; ok {
		t.Error("orphaned stream should be removed from the map")
	}
	if _, ok := h.streams["keep"]; !ok {
		t.Error("live stream should remain in the map")
	}
}

func TestEnsureStream_NoopBeforeStart(t *testing.T) {
	h := NewHealthService(false) // streamCtx is nil until Start()
	h.ensureStream(db.Machine{ID: "m1", AgentRemotePort: 5000})
	if len(h.streams) != 0 {
		t.Errorf("ensureStream must no-op before Start(); got %d streams", len(h.streams))
	}
}

func TestEnsureStream_IdempotentAndCancellable(t *testing.T) {
	h := NewHealthService(false)
	h.streamCtx, h.streamCancel = context.WithCancel(context.Background())
	t.Cleanup(h.streamCancel)

	m := db.Machine{ID: "m1", AgentRemotePort: 5000}
	h.ensureStream(m)
	h.ensureStream(m) // second call must not register a duplicate worker

	h.mu.Lock()
	n := len(h.streams)
	h.mu.Unlock()
	if n != 1 {
		t.Fatalf("expected exactly 1 stream registered, got %d", n)
	}

	h.reconcileStreams(map[string]bool{}) // nothing should survive
	h.mu.Lock()
	n = len(h.streams)
	h.mu.Unlock()
	if n != 0 {
		t.Errorf("reconcile with empty keep-set should clear all streams, got %d", n)
	}
}
