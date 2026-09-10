package service

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/smalex-z/gopher/internal/db"
)

type fakeNoiseMigrator struct{ calls atomic.Int32 }

func (f *fakeNoiseMigrator) run() error { f.calls.Add(1); return nil }

// The deferred noise cutover must complete without a server restart: once every
// machine that blocked it is back connected on a current agent, a health poll
// re-runs the migration. It must NOT fire while any blocker is still not ready,
// nor after the install is already on noise.
func TestMaybeRetryNoiseMigration_FiresWhenFleetReady(t *testing.T) {
	initTestDB(t)
	if err := db.MutateSettings(func(a *db.AppSettings) error {
		a.IsSetup = true
		a.RatholeNoisePrivKey = ""
		a.RatholeNoiseBlockedMachines = `["m1","m2"]`
		return nil
	}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	mk := func(id, name string, outdated bool) {
		if err := db.CreateMachine(&db.Machine{ID: id, Name: name, Status: "connected", AgentInstalled: true, AgentOutdated: outdated}); err != nil {
			t.Fatalf("seed machine %s: %v", name, err)
		}
	}
	mk("id1", "m1", false) // current
	mk("id2", "m2", true)  // still outdated → fleet not ready

	fm := &fakeNoiseMigrator{}
	h := NewHealthService(false)
	h.SetNoiseMigrator(fm.run)

	// A blocker went healthy but the other is still outdated → must not fire.
	h.maybeRetryNoiseMigration(&db.Machine{ID: "id1", Name: "m1"})
	time.Sleep(100 * time.Millisecond)
	if got := fm.calls.Load(); got != 0 {
		t.Fatalf("fired while m2 still outdated: %d calls", got)
	}

	// A non-blocking machine's poll must not trigger anything either.
	h.maybeRetryNoiseMigration(&db.Machine{ID: "idX", Name: "unrelated"})
	time.Sleep(50 * time.Millisecond)
	if got := fm.calls.Load(); got != 0 {
		t.Fatalf("fired for a non-blocking machine: %d calls", got)
	}

	// Last blocker reaches current → whole fleet ready → fires once.
	if err := db.SetMachineAgentOutdated("id2", false); err != nil {
		t.Fatalf("mark m2 current: %v", err)
	}
	h.maybeRetryNoiseMigration(&db.Machine{ID: "id2", Name: "m2"})
	waitForCalls(t, fm.calls.Load, 1)

	// Once the install is on noise (priv key present), a later poll must not
	// re-fire even with the cooldown cleared.
	if err := db.MutateSettings(func(a *db.AppSettings) error { a.RatholeNoisePrivKey = "present"; return nil }); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}
	h.mu.Lock()
	h.noiseRetryLast = time.Time{}
	h.noiseRetryInFlight = false
	h.mu.Unlock()
	h.maybeRetryNoiseMigration(&db.Machine{ID: "id2", Name: "m2"})
	time.Sleep(100 * time.Millisecond)
	if got := fm.calls.Load(); got != 1 {
		t.Fatalf("fired after already migrated: %d calls", got)
	}
}
