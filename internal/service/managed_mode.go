package service

import (
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/smalex-z/gopher/internal/embedbin"
	"github.com/smalex-z/gopher/internal/paths"
)

// managedModeHealMarker records that a startup self-heal restart was already
// attempted, so a host where the patch or restart doesn't stick gets exactly
// one try — never a boot loop. See EnsureManagedModeAtStartup.
var managedModeHealMarker = paths.StateDir + "/.managed-mode-heal-attempted"

// EnsureManagedModeAtStartup is the state-based counterpart to Apply()'s
// patchSystemdManagedEnv call. That patch runs only when the CURRENTLY
// running binary executes Apply() — but that binary is always the OLD one
// (Apply() downloads and installs the new binary; it doesn't re-exec into
// it), so a fix living only inside Apply() can never repair the specific
// transition that ships it. Any install that reaches a fixed release any
// other way — skipping several versions in one Apply(), a manual binary
// swap + restart, anything but an in-place Apply() performed by an
// already-fixed binary — still boots with an unpatched unit and silently
// runs unmanaged (new tunnels/machines never connect; see the update.go
// Apply() comment for the full failure chain).
//
// This runs on every plain startup instead, keyed on the process's own
// environment rather than on which release is doing the updating — so it
// self-heals regardless of update cadence or how the box got here. Call
// site (main.go) skips this entirely in --dev mode, matching --dev's
// no-system-writes contract.
func EnsureManagedModeAtStartup() {
	if !embedbin.Embedded() || os.Getenv("GOPHER_MANAGED") == "1" {
		return
	}
	ensureManagedModeAtStartup()
}

// ensureManagedModeAtStartup is the actual marker/patch/restart logic, split
// out so tests can exercise it without depending on embedbin.Embedded() —
// that's only true when scripts/fetch-deps.sh has staged real binaries into
// internal/embedbin/bin/ first, which plain `go test` (as CI's Go Unit Tests
// job runs it) never does. A test asserting on this function's behavior
// while gated behind Embedded() passes or fails based on whether the
// repo happens to have staged binaries lying around — exactly the gap that
// let TestEnsureManagedModeAtStartup_DoesNotRetryAfterMarkerExists pass in a
// local dev checkout for the wrong reason (short-circuited on Embedded(),
// not on the marker check it claimed to test) while failing in CI.
func ensureManagedModeAtStartup() {
	if _, err := os.Stat(managedModeHealMarker); err == nil {
		// Already tried once and it didn't stick — an unconditional retry
		// would turn a host where the patch can never succeed (no sudo,
		// read-only /etc, whatever) into a restart loop that never finishes
		// booting. Log loudly instead and keep serving in legacy/unmanaged
		// mode; an operator can rerun `gopher install` directly, or delete
		// the marker to allow one more automatic attempt.
		log.Printf("WARN: edge is still running unmanaged (GOPHER_MANAGED unset) after a prior self-heal attempt — new tunnels/machines may silently fail to connect. Run 'sudo gopher install' to fix, or remove %s to allow another automatic attempt.", managedModeHealMarker)
		return
	}
	// Write the marker BEFORE attempting anything: if patchSystemdManagedEnv
	// itself panics or the process is killed mid-attempt, the next boot
	// still sees "already tried" rather than looping.
	if err := os.WriteFile(managedModeHealMarker, []byte(time.Now().UTC().String()+"\n"), 0644); err != nil {
		log.Printf("WARN: could not write managed-mode marker %s, skipping startup self-heal to avoid a possible restart loop: %v", managedModeHealMarker, err)
		return
	}
	if err := patchSystemdManagedEnv(); err != nil {
		log.Printf("WARN: startup managed-mode self-heal failed: %v", err)
		return
	}
	log.Printf("Enabling managed mode (GOPHER_MANAGED=1) on the systemd unit — restarting once to pick it up.")
	go func() {
		time.Sleep(time.Second)
		restartArgs := append(append([]string{}, privilegedCmdPrefix()...), "systemctl", "restart", "gopher")
		_ = exec.Command(restartArgs[0], restartArgs[1:]...).Run() // #nosec G204 — fixed args
	}()
}
