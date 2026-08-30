package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Regression for a real incident: rathole-client's own reconnect loop wedged
// after a heartbeat timeout / data-channel failure burst. The process never
// exited, so systemd kept reporting "active" and Restart=always never fired
// — it just sat there, silently disconnected, for 11+ hours, until someone
// noticed on the dashboard and restarted it by hand. The old recoverRatholeOnce
// only ever checked unitActive(), which is exactly the signal that stayed
// "active" throughout. These tests cover the added wedge detection.

// writeStub drops an executable shell script named `name` into dir and
// prepends dir to PATH for the duration of the test, so the code under test
// (which shells out via plain exec.Command) resolves it instead of the
// real binary.
func writeStub(t *testing.T, dir, name, script string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0755); err != nil {
		t.Fatalf("write %s stub: %v", name, err)
	}
}

func setStubPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

// systemctlStub answers `show -p <Prop> --value <unit>` for ActiveState,
// SubState, and MainPID — everything unitActive/runProp/hasEstablishedConnection
// need — and records any start/restart/reset-failed invocation to logPath.
func systemctlStub(t *testing.T, dir, activeState, mainPID, logPath string) {
	t.Helper()
	writeStub(t, dir, "systemctl", `
if [ "$1" = "show" ]; then
  case "$3" in
    ActiveState) echo "`+activeState+`" ;;
    SubState) echo "running" ;;
    MainPID) echo "`+mainPID+`" ;;
  esac
  exit 0
fi
echo "$@" >> `+logPath+`
exit 0
`)
}

// sudoStub forwards `-n systemctl ...` to the systemctl stub already on PATH,
// and answers `-n ss -tnp state established` with ssOutput.
func sudoStub(t *testing.T, dir, ssOutput string) {
	t.Helper()
	writeStub(t, dir, "sudo", `
shift # drop -n
if [ "$1" = "ss" ]; then
  printf '%s\n' "`+ssOutput+`"
  exit 0
fi
exec "$@"
`)
}

func TestRecoverRatholeOnce_DownUnitGetsStarted(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "systemctl.log")
	systemctlStub(t, dir, "inactive", "0", logPath)
	sudoStub(t, dir, "")
	setStubPath(t, dir)

	ticks := 0
	recoverRatholeOnce(config{UnitName: "rathole-client"}, &ticks)

	log, _ := os.ReadFile(logPath)
	if !strings.Contains(string(log), "start rathole-client") {
		t.Errorf("expected a start command for a down unit, log:\n%s", log)
	}
	if ticks != 0 {
		t.Errorf("silentTicks should reset when the unit is down, got %d", ticks)
	}
}

func TestRecoverRatholeOnce_ActiveWithConnectionIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "systemctl.log")
	systemctlStub(t, dir, "active", "4242", logPath)
	sudoStub(t, dir, `ESTAB 0 0 10.0.0.5:51000 1.2.3.4:2333 users:(("rathole",pid=4242,fd=7))`)
	setStubPath(t, dir)

	ticks := 0
	for i := 0; i < 5; i++ {
		recoverRatholeOnce(config{UnitName: "rathole-client"}, &ticks)
	}

	if _, err := os.Stat(logPath); err == nil {
		log, _ := os.ReadFile(logPath)
		t.Errorf("a healthy, connected unit must never be restarted, log:\n%s", log)
	}
	if ticks != 0 {
		t.Errorf("silentTicks should stay 0 while connected, got %d", ticks)
	}
}

// The core regression: active, but zero established connections, repeatedly.
func TestRecoverRatholeOnce_WedgedUnitGetsRestartedAfterThreshold(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "systemctl.log")
	systemctlStub(t, dir, "active", "4242", logPath)
	sudoStub(t, dir, "") // no established connections at all
	setStubPath(t, dir)

	ticks := 0
	for i := 1; i < wedgedSilentTicks; i++ {
		recoverRatholeOnce(config{UnitName: "rathole-client"}, &ticks)
		if _, err := os.Stat(logPath); err == nil {
			t.Fatalf("must not restart before the %d-tick threshold (tick %d)", wedgedSilentTicks, i)
		}
		if ticks != i {
			t.Errorf("tick %d: silentTicks = %d, want %d", i, ticks, i)
		}
	}

	// The threshold-th tick should trigger the restart.
	recoverRatholeOnce(config{UnitName: "rathole-client"}, &ticks)
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("expected a restart to have been issued at tick %d: %v", wedgedSilentTicks, err)
	}
	if !strings.Contains(string(log), "restart rathole-client") {
		t.Errorf("expected a restart command, log:\n%s", log)
	}
	// Exact-line check, not a substring one: "restart rathole-client"
	// itself contains "start rathole-client" as a substring (re+start).
	for _, line := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		if line == "start rathole-client" {
			t.Errorf("wedged-but-active must use restart, not start (start no-ops on an active unit), log:\n%s", log)
		}
	}
	if ticks != 0 {
		t.Errorf("silentTicks should reset after acting, got %d", ticks)
	}
}

func TestRecoverRatholeOnce_ConnectionReturningResetsTheCounter(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "systemctl.log")
	setStubPath(t, dir)

	ticks := 0
	// One silent tick...
	systemctlStub(t, dir, "active", "4242", logPath)
	sudoStub(t, dir, "")
	recoverRatholeOnce(config{UnitName: "rathole-client"}, &ticks)
	if ticks != 1 {
		t.Fatalf("expected silentTicks=1, got %d", ticks)
	}
	// ...then a real reconnect happens...
	sudoStub(t, dir, `ESTAB 0 0 10.0.0.5:51000 1.2.3.4:2333 users:(("rathole",pid=4242,fd=7))`)
	recoverRatholeOnce(config{UnitName: "rathole-client"}, &ticks)
	if ticks != 0 {
		t.Fatalf("a live connection must reset the counter, got %d", ticks)
	}
	// ...so a subsequent lone silent tick must not restart anything.
	sudoStub(t, dir, "")
	recoverRatholeOnce(config{UnitName: "rathole-client"}, &ticks)
	if _, err := os.Stat(logPath); err == nil {
		t.Error("counter reset should mean a single follow-up silent tick doesn't restart")
	}
}

// Regression for a second real incident: a systemd daemon re-exec (nightly
// unattended-upgrades patching libpam) made systemctl unanswerable for ~a
// second. The watchdog's tick landed inside that window, read the failed
// query as "unit down", and issued reset-failed + start — a no-op, but it
// logged a phantom recovery and the same misread flipped the machine to
// degraded on the dashboard. An unanswerable systemctl must be treated as
// indeterminate: no start, no counter change, wait for the next tick.
func TestRecoverRatholeOnce_UnanswerableSystemctlDoesNothing(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "systemctl.log")
	// `show` fails outright (manager unreachable); anything else is recorded.
	writeStub(t, dir, "systemctl", `
if [ "$1" = "show" ]; then exit 1; fi
echo "$@" >> `+logPath+`
exit 0
`)
	sudoStub(t, dir, "")
	setStubPath(t, dir)

	oldDelay := stateQueryRetryDelay
	stateQueryRetryDelay = time.Millisecond
	defer func() { stateQueryRetryDelay = oldDelay }()

	ticks := 1
	recoverRatholeOnce(config{UnitName: "rathole-client"}, &ticks)

	if _, err := os.Stat(logPath); err == nil {
		log, _ := os.ReadFile(logPath)
		t.Errorf("indeterminate state must not trigger start/restart/reset-failed, log:\n%s", log)
	}
	if ticks != 1 {
		t.Errorf("indeterminate state must leave silentTicks untouched, got %d", ticks)
	}
}

func TestHasEstablishedConnection_MeasurementFailureDefaultsTrue(t *testing.T) {
	// Fully isolated PATH — no systemctl binary resolvable at all, real or
	// stub — so the MainPID lookup fails outright rather than depending on
	// how the host's actual systemd answers a query for a made-up unit.
	t.Setenv("PATH", t.TempDir())
	if !hasEstablishedConnection("rathole-client") {
		t.Error("a measurement failure must default to true (don't restart on an inconclusive check)")
	}
}
