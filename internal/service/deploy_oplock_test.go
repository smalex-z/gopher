package service

import (
	"errors"
	"testing"
	"time"

	"github.com/smalex-z/gopher/internal/db"
)

// TestDeployClientRespectsOpLock verifies the fix for the shared-hub bug:
// DeployClient must not stream (or fire its terminating \x00DONE) while another
// op already holds the hub op-lock. It should return ErrOpInProgress instead of
// interleaving with, and prematurely closing, the concurrent op's log stream.
func TestDeployClientRespectsOpLock(t *testing.T) {
	initTestDB(t) // so the background deploy body errors cleanly, not nil-DB panic
	svc := NewDeployService()

	// Simulate a concurrent install/firewall op holding the lock.
	if !svc.Hub.TryAcquireOp() {
		t.Fatal("expected to acquire the op lock on a fresh hub")
	}

	err := svc.DeployClient(&db.Machine{ID: "m1"})
	if !errors.Is(err, ErrOpInProgress) {
		t.Fatalf("expected ErrOpInProgress while lock held, got %v", err)
	}

	// Release and confirm a subsequent deploy can now claim the lock (it runs
	// its body in a goroutine and returns nil immediately).
	svc.Hub.ReleaseOp()
	if err := svc.DeployClient(&db.Machine{ID: "m1"}); err != nil {
		t.Fatalf("expected deploy to start after release, got %v", err)
	}

	// The background goroutine takes the lock; wait for it to release so the
	// test doesn't leak it. The body fails fast (no DB), then ReleaseOp runs.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if svc.Hub.TryAcquireOp() {
			svc.Hub.ReleaseOp()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("deploy goroutine never released the op lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
