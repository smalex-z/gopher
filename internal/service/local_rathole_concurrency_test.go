package service

import (
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/smalex-z/gopher/internal/paths"
)

// Regression for a real gap found in a QA sweep: ReconcileServerConfig had no
// lock, despite being called concurrently and uncoordinated from tunnel/
// machine create, update, delete, bootstrap, and agent-install. Each call
// re-reads the full DB state and rewrites server.toml from scratch via
// writeLocalFileInPlace (O_TRUNC + a single WriteString), so two overlapping
// calls could interleave such that the one invoked first finishes writing
// last with a now-stale DB snapshot, regressing the file to a state that
// matches neither caller's intent. reconcileMu serializes every call.
//
// This test can't deterministically force the exact interleaving that caused
// the original bug (that's the nature of a TOCTOU race), so it asserts what
// the lock guarantees regardless of scheduling: every concurrent call
// succeeds, and the file left behind is always a single, complete, valid
// config — never a torn/mixed write from two racing writers.
func TestReconcileServerConfig_ConcurrentCallsProduceOneValidFile(t *testing.T) {
	initTestDB(t)
	cfgPath := t.TempDir() + "/server.toml"

	orig := paths.RatholeConfig
	paths.RatholeConfig = cfgPath
	t.Cleanup(func() { paths.RatholeConfig = orig })

	svc := &LocalSetupService{}

	const n = 25
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = svc.ReconcileServerConfig()
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent ReconcileServerConfig call %d: %v", i, err)
		}
	}

	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read reconciled config: %v", err)
	}
	content := string(got)
	if !strings.Contains(content, "[server]") {
		t.Errorf("expected a well-formed config with a [server] section, got:\n%s", content)
	}
	if !strings.Contains(content, "# ===== BEGIN CUSTOM CONFIGURATION =====") ||
		!strings.Contains(content, "# ===== END CUSTOM CONFIGURATION =====") {
		t.Errorf("expected exactly one intact custom-section marker pair, got:\n%s", content)
	}
	// A torn write from two racing, unsynchronized writers would typically
	// duplicate or truncate the markers — assert each appears exactly once.
	if c := strings.Count(content, "BEGIN CUSTOM CONFIGURATION"); c != 1 {
		t.Errorf("expected exactly 1 BEGIN marker, got %d — file may be corrupted:\n%s", c, content)
	}
	if c := strings.Count(content, "END CUSTOM CONFIGURATION"); c != 1 {
		t.Errorf("expected exactly 1 END marker, got %d — file may be corrupted:\n%s", c, content)
	}
}
