package main

import (
	"io"
	"net/http"
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

	st := &watchdogState{}
	recoverRatholeOnce(config{UnitName: "rathole-client"}, st)

	log, _ := os.ReadFile(logPath)
	if !strings.Contains(string(log), "start rathole-client") {
		t.Errorf("expected a start command for a down unit, log:\n%s", log)
	}
	if st.silent != 0 {
		t.Errorf("silentTicks should reset when the unit is down, got %d", st.silent)
	}
}

func TestRecoverRatholeOnce_ActiveWithConnectionIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "systemctl.log")
	systemctlStub(t, dir, "active", "4242", logPath)
	sudoStub(t, dir, `ESTAB 0 0 10.0.0.5:51000 1.2.3.4:2333 users:(("rathole",pid=4242,fd=7))`)
	setStubPath(t, dir)

	st := &watchdogState{}
	for i := 0; i < 5; i++ {
		recoverRatholeOnce(config{UnitName: "rathole-client"}, st)
	}

	if _, err := os.Stat(logPath); err == nil {
		log, _ := os.ReadFile(logPath)
		t.Errorf("a healthy, connected unit must never be restarted, log:\n%s", log)
	}
	if st.silent != 0 {
		t.Errorf("silentTicks should stay 0 while connected, got %d", st.silent)
	}
}

// The core regression: active, but zero established connections, repeatedly.
func TestRecoverRatholeOnce_WedgedUnitGetsRestartedAfterThreshold(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "systemctl.log")
	systemctlStub(t, dir, "active", "4242", logPath)
	sudoStub(t, dir, "") // no established connections at all
	setStubPath(t, dir)

	st := &watchdogState{}
	for i := 1; i < wedgedSilentTicks; i++ {
		recoverRatholeOnce(config{UnitName: "rathole-client"}, st)
		if _, err := os.Stat(logPath); err == nil {
			t.Fatalf("must not restart before the %d-tick threshold (tick %d)", wedgedSilentTicks, i)
		}
		if st.silent != i {
			t.Errorf("tick %d: silentTicks = %d, want %d", i, st.silent, i)
		}
	}

	// The threshold-th tick should trigger the restart.
	recoverRatholeOnce(config{UnitName: "rathole-client"}, st)
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
	if st.silent != 0 {
		t.Errorf("silentTicks should reset after acting, got %d", st.silent)
	}
}

func TestRecoverRatholeOnce_ConnectionReturningResetsTheCounter(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "systemctl.log")
	setStubPath(t, dir)

	st := &watchdogState{}
	// One silent tick...
	systemctlStub(t, dir, "active", "4242", logPath)
	sudoStub(t, dir, "")
	recoverRatholeOnce(config{UnitName: "rathole-client"}, st)
	if st.silent != 1 {
		t.Fatalf("expected silentTicks=1, got %d", st.silent)
	}
	// ...then a real reconnect happens...
	sudoStub(t, dir, `ESTAB 0 0 10.0.0.5:51000 1.2.3.4:2333 users:(("rathole",pid=4242,fd=7))`)
	recoverRatholeOnce(config{UnitName: "rathole-client"}, st)
	if st.silent != 0 {
		t.Fatalf("a live connection must reset the counter, got %d", st.silent)
	}
	// ...so a subsequent lone silent tick must not restart anything.
	sudoStub(t, dir, "")
	recoverRatholeOnce(config{UnitName: "rathole-client"}, st)
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

	st := &watchdogState{silent: 1}
	recoverRatholeOnce(config{UnitName: "rathole-client"}, st)

	if _, err := os.Stat(logPath); err == nil {
		log, _ := os.ReadFile(logPath)
		t.Errorf("indeterminate state must not trigger start/restart/reset-failed, log:\n%s", log)
	}
	if st.silent != 1 {
		t.Errorf("indeterminate state must leave silentTicks untouched, got %d", st.silent)
	}
}

// pointConfigAt overrides the watchdog's config path to a temp file seeded
// with content, restoring the real resolver on cleanup.
func pointConfigAt(t *testing.T, dir, content string) string {
	t.Helper()
	cfgPath := filepath.Join(dir, "client.toml")
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	orig := activeClientTomlPath
	activeClientTomlPath = func() string { return cfgPath }
	t.Cleanup(func() { activeClientTomlPath = orig })
	return cfgPath
}

// A config rathole itself keeps rejecting — truncation, garbage TOML the
// de-dup can't fix — shows up as a unit that won't stay up: every tick finds
// it down and start doesn't stick. After failedStartRefetchTicks of that, the
// file is the suspect and must be refetched from the edge (with the suspect
// content sent along so custom sections survive, and a .bak left behind).
func TestRecoverRatholeOnce_RepeatedFailedStartsRefetchFromEdge(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "systemctl.log")
	systemctlStub(t, dir, "inactive", "0", logPath)
	sudoStub(t, dir, "")
	setStubPath(t, dir)

	const garbage = "this is not toml [client\nremote_addr = broken"
	cfgPath := pointConfigAt(t, dir, garbage)

	const fresh = "[client]\nremote_addr = \"router.example.com:2333\"\n"
	var gotBody string
	withTestEdge(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(fresh))
	})

	st := &watchdogState{}
	cfg := config{UnitName: "rathole-client", Token: "tok"}
	recoverRatholeOnce(cfg, st) // down → start, failedStarts=1
	recoverRatholeOnce(cfg, st) // still down → failedStarts=2
	if got, _ := os.ReadFile(cfgPath); string(got) != garbage {
		t.Fatal("config must not be refetched before the failed-start threshold")
	}
	recoverRatholeOnce(cfg, st) // threshold reached → refetch fires

	if got, _ := os.ReadFile(cfgPath); string(got) != fresh {
		t.Errorf("config after refetch = %q, want the edge's copy", got)
	}
	if gotBody != garbage {
		t.Errorf("the suspect config must be sent to the edge for custom-section salvage, got %q", gotBody)
	}
	if bak, err := os.ReadFile(cfgPath + ".bak"); err != nil || string(bak) != garbage {
		t.Errorf(".bak must hold the pre-refetch content, got %q (err %v)", bak, err)
	}
}

// A config rathole runs with but can never connect through — corrupted
// remote_addr, deleted transport block, snapshot-stale tokens — shows up as
// wedge restarts that never help. After wedgeRefetchCycles of those, refetch.
func TestRecoverRatholeOnce_RepeatedWedgeRestartsRefetchFromEdge(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "systemctl.log")
	systemctlStub(t, dir, "active", "4242", logPath)
	sudoStub(t, dir, "") // never any established connection
	setStubPath(t, dir)

	const stale = "[client]\nremote_addr = \"old-edge.example.com:2333\"\n"
	cfgPath := pointConfigAt(t, dir, stale)

	const fresh = "[client]\nremote_addr = \"router.example.com:2333\"\n"
	withTestEdge(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(fresh))
	})

	st := &watchdogState{}
	cfg := config{UnitName: "rathole-client", Token: "tok"}
	// Two full wedge cycles (silent ticks → restart), config untouched so far…
	for i := 0; i < 2*wedgedSilentTicks; i++ {
		recoverRatholeOnce(cfg, st)
	}
	if got, _ := os.ReadFile(cfgPath); string(got) != stale {
		t.Fatal("config must not be refetched before the wedge-cycle threshold")
	}
	// …the next tick sees wedgeCycles at the threshold and refetches.
	recoverRatholeOnce(cfg, st)
	if got, _ := os.ReadFile(cfgPath); string(got) != fresh {
		t.Errorf("config after wedge refetch = %q, want the edge's copy", got)
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
