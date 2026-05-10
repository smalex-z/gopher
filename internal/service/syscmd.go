package service

import (
	"fmt"
	"log"
	"os/exec"
	"runtime/debug"
	"strings"
)

// goSafe wraps a goroutine body with panic recovery so a panic in a
// long-running async worker (bootstrap polls, install streams, monitor
// probes) doesn't take down the whole daemon. label is used in the log
// message so operators can match panics to the failing goroutine.
//
// Usage:
//   go goSafe("awaitSSHHealth", func() { ... })
func goSafe(label string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic in %s: %v\n%s", label, r, debug.Stack())
		}
	}()
	fn()
}

// runSudoCommand runs name+args under sudo, captures combined stdout+stderr,
// logs a structured warning on non-zero exit, and returns the wrapped error.
// Replaces the prior `_ = exec.Command("sudo", ...).Run()` pattern that
// silently dropped systemctl/caddy/iptables failures — when a Caddy reload
// fails because of a syntax error in the user's custom block, the API was
// returning success while the subdomain stayed 502'd until somebody read
// `journalctl -u caddy`.
//
// Callers that have an io.Writer hooked to the LogHub should prefer
// runSudoCommandToLog so the failure also reaches the dashboard's live log.
func runSudoCommand(name string, args ...string) error {
	full := append([]string{name}, args...)
	cmd := exec.Command("sudo", full...) // #nosec G204 — args are caller-controlled hardcoded strings
	out, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(out))
		log.Printf("sudo %s %s: %v (output: %s)", name, strings.Join(args, " "), err, trimmed)
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// systemctlReload runs `sudo systemctl reload <unit>`. Convenience wrapper.
func systemctlReload(unit string) error {
	return runSudoCommand("systemctl", "reload", unit)
}

// systemctlStart runs `sudo systemctl start <unit>`. systemctl start is a
// no-op on an already-active unit, so this is the right call when a config
// change has been written and rathole/caddy's notify watcher will reload —
// we just want to cover the "service was stopped" case.
func systemctlStart(unit string) error {
	return runSudoCommand("systemctl", "start", unit)
}

// systemctlReloadOrRestart runs `sudo systemctl reload-or-restart <unit>`.
func systemctlReloadOrRestart(unit string) error {
	return runSudoCommand("systemctl", "reload-or-restart", unit)
}
