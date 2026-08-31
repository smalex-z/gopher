package main

import (
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/paths"
)

// Origin systemd units the migration rewrites in place.
const (
	ratholeClientUnit = "/etc/systemd/system/rathole-client.service"
	gopherAgentUnit   = "/etc/systemd/system/gopher-agent.service"
)

// migrateOriginLayout moves an origin off the legacy pre-consolidation layout
// (/etc/rathole/client.toml, /etc/gopher-agent/config.env) onto the /etc/gopher
// tree, matching the edge. It runs once on first boot after the 0.2.1 upgrade
// and is a no-op on already-migrated origins (and on fresh 0.2.1 bootstraps,
// which write the new paths directly).
//
// The agent runs as the gopher user with NOPASSWD: ALL (see bootstrap.sh), so
// every privileged step shells out through `sudo -n`. The whole routine is
// best-effort: a failure logs and returns rather than crashing the agent, so a
// half-migrated box still serves on whatever paths currently work and the next
// boot retries. Steps are ordered copy → repoint-unit → reload → restart →
// remove-legacy so there is never a window where a unit points at a file that
// does not exist.
func migrateOriginLayout() {
	legacyClient := fileExists(paths.LegacyRatholeClientConfig)
	legacyEnv := fileExists(paths.LegacyAgentConfigEnv)
	unitsReferenceLegacy := unitReferences(ratholeClientUnit, paths.LegacyRatholeClientConfig) ||
		unitReferences(gopherAgentUnit, paths.LegacyAgentConfigEnv)

	if !legacyClient && !legacyEnv && !unitsReferenceLegacy {
		return // already on the consolidated layout (or a fresh 0.2.1 bootstrap)
	}

	// Before touching anything, repair the rathole config we'd relocate.
	// Relocating restarts rathole-client, and a restart re-reads the file from
	// disk — so a latent on-disk error the running rathole survived (it rejected
	// the inotify reload and kept its good in-memory config) would turn fatal on
	// restart and drop every tunnel. Recovery is the agent's whole job, so heal
	// it: de-duplicate, write the clean version back, and only then proceed. If
	// it still won't validate after repair, leave the box untouched (up, on its
	// current config) rather than detonating it.
	srcConfig := paths.LegacyRatholeClientConfig
	if !legacyClient {
		srcConfig = paths.RatholeClientConfig
	}
	if !repairRatholeConfig(srcConfig) {
		log.Printf("origin layout migration: %s still won't validate after repair — leaving the machine as-is, will retry next boot", srcConfig)
		return
	}

	log.Printf("origin layout migration: consolidating config under %s", paths.ConfigDir)

	if err := sudo("mkdir", "-p", paths.RatholeDir, paths.AgentDir); err != nil {
		log.Printf("origin layout migration: mkdir failed, aborting (will retry next boot): %v", err)
		return
	}

	restartRathole := false

	// rathole client.toml: copy to the new path, repoint the unit, then restart
	// rathole-client once so it reads the relocated config (rathole binds the
	// inode named on its argv, so a path change needs a process bounce — a
	// one-time tunnel blip that reconnects on its own).
	if legacyClient && !fileExists(paths.RatholeClientConfig) {
		if err := sudo("cp", "-p", paths.LegacyRatholeClientConfig, paths.RatholeClientConfig); err != nil {
			log.Printf("origin layout migration: copy client.toml failed, aborting: %v", err)
			return
		}
		_ = sudo("chown", "gopher:gopher", paths.RatholeClientConfig)
		_ = sudo("chmod", "0644", paths.RatholeClientConfig)
	}
	if fileExists(paths.LegacyRatholeVPSKey) && !fileExists(paths.RatholeVPSKey) {
		_ = sudo("cp", "-p", paths.LegacyRatholeVPSKey, paths.RatholeVPSKey)
	}
	if unitReferences(ratholeClientUnit, paths.LegacyRatholeClientConfig) {
		if err := sudoSed(ratholeClientUnit, paths.LegacyRatholeClientConfig, paths.RatholeClientConfig); err != nil {
			log.Printf("origin layout migration: repoint rathole-client unit failed, leaving legacy config in place: %v", err)
		} else {
			restartRathole = true
		}
	}

	// gopher-agent config.env: copy to the new path and repoint EnvironmentFile.
	// No restart — the running agent already holds its token; systemd reads the
	// new EnvironmentFile on the next natural restart.
	if legacyEnv && !fileExists(paths.AgentConfigEnv) {
		if err := sudo("cp", "-p", paths.LegacyAgentConfigEnv, paths.AgentConfigEnv); err == nil {
			_ = sudo("chown", "root:gopher", paths.AgentConfigEnv)
			_ = sudo("chmod", "0640", paths.AgentConfigEnv)
		} else {
			log.Printf("origin layout migration: copy config.env failed: %v", err)
		}
	}
	_ = sudoSed(gopherAgentUnit, paths.LegacyAgentConfigEnv, paths.AgentConfigEnv)

	_ = sudo("systemctl", "daemon-reload")

	// ratholeRelocated stays false if we still need rathole to bounce onto the
	// new path but the restart didn't succeed — in that case the legacy config
	// must stay so the running (old-path) rathole keeps serving until a later
	// restart picks up the repointed unit.
	ratholeRelocated := !restartRathole // nothing to relocate ⇒ already correct
	if restartRathole && fileExists(paths.RatholeClientConfig) {
		if err := sudo("systemctl", "restart", "rathole-client.service"); err != nil {
			log.Printf("origin layout migration: restart rathole-client failed (keeping legacy config; retry on next boot): %v", err)
		} else {
			ratholeRelocated = true
			log.Printf("origin layout migration: rathole-client restarted on %s", paths.RatholeClientConfig)
		}
	}

	// Remove legacy files only once the new layout is confirmed live, so a
	// failure above never strands the box with a unit pointing at a deleted file.
	if ratholeRelocated && fileExists(paths.RatholeClientConfig) {
		_ = sudo("rm", "-f", paths.LegacyRatholeClientConfig, paths.LegacyRatholeVPSKey)
		_ = sudo("rmdir", "--ignore-fail-on-non-empty", paths.LegacyRatholeClientDir)
	}
	if fileExists(paths.AgentConfigEnv) && !unitReferences(gopherAgentUnit, paths.LegacyAgentConfigEnv) {
		_ = sudo("rm", "-f", paths.LegacyAgentConfigEnv)
		_ = sudo("rmdir", "--ignore-fail-on-non-empty", paths.LegacyAgentDir)
	}

	log.Printf("origin layout migration: complete")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// activeClientTomlPath returns the rathole client.toml the box is actually
// using: the consolidated path if present, else the legacy one.
func activeClientTomlPath() string {
	if fileExists(paths.RatholeClientConfig) {
		return paths.RatholeClientConfig
	}
	if fileExists(paths.LegacyRatholeClientConfig) {
		return paths.LegacyRatholeClientConfig
	}
	return paths.RatholeClientConfig
}

// repairRatholeConfig de-duplicates the rathole client config at path in place
// and reports whether it ends up loadable (no duplicate tables). A clean config
// is a no-op that returns true. The agent owns this file (chowned at bootstrap),
// so the in-place truncate-write preserves the inode and rathole hot-reloads it
// without a flap when it is running. Returns false only if the file can't be
// read or can't be repaired into a loadable state.
func repairRatholeConfig(path string) bool {
	data, err := os.ReadFile(path) // #nosec G304 — fixed config paths
	if err != nil {
		// Nothing here to relocate/repair (e.g. user-level config); not our
		// failure — let the caller proceed.
		return true
	}
	if _, dup := duplicateTomlTable(string(data)); !dup {
		return true
	}

	fixed := dedupeTomlTables(string(data))
	if _, stillDup := duplicateTomlTable(fixed); stillDup || fixed == string(data) {
		return false // couldn't actually resolve it — don't claim success
	}
	if err := writeFilePreservingMode(path, []byte(fixed)); err != nil {
		log.Printf("rathole config repair: failed writing de-duplicated %s: %v", path, err)
		return false
	}
	log.Printf("rathole config repair: removed duplicate table(s) from %s", path)
	return true
}

// ratholeRecoveryLoop is the agent's local self-healing watchdog. systemd keeps
// rathole-client alive across crashes (Restart=always), but a config it can't
// parse lands the unit in "failed" once the restart-burst limit trips, and the
// server can't reach in to fix it because the back-channel rides that very
// tunnel. So the agent — which stays up independently — periodically repairs the
// config and resurrects a down unit. This is exactly the failure that took a
// machine offline: a duplicate table rathole choked on at startup.
//
// It also covers a second, sneakier failure: rathole's own reconnect loop can
// wedge after a heartbeat timeout / data-channel failure burst — the process
// never exits, so systemd still reports "active" and Restart=always never
// fires, but it stops making any progress and never reconnects on its own
// even once the network is fine again. That happened for real: heartbeat
// timeout → control channel shutdown → a burst of failed reconnect attempts
// → total silence for 11+ hours while `systemctl status` looked healthy the
// whole time. wedgedSilentTicks tracks consecutive misses of the one signal
// a genuinely healthy client always has — a live established connection to
// the edge — and force-restarts once that's been absent long enough to rule
// out a normal in-flight reconnect.
//
// A third failure — client.toml missing, or unloadable beyond de-duplication —
// is repaired by dialing out to the edge for the authoritative copy (see
// recover.go); starting the unit without a config would only crash-loop it.
func ratholeRecoveryLoop(cfg config) {
	silentTicks := 0
	recoverRatholeOnce(cfg, &silentTicks)
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for range t.C {
		recoverRatholeOnce(cfg, &silentTicks)
	}
}

// wedgedSilentTicks is how many consecutive recoverRatholeOnce calls (at the
// loop's 60s cadence) may find zero established connections before the unit
// is treated as wedged and force-restarted. A real reconnect after a dropped
// control channel lands well inside one tick; requiring several consecutive
// misses avoids restarting a client that's mid-retry on a slow network.
const wedgedSilentTicks = 3

func recoverRatholeOnce(cfg config, silentTicks *int) {
	unit := cfg.UnitName
	path := activeClientTomlPath()
	missing := !fileExists(path)
	broken := false
	if !missing {
		broken = !repairRatholeConfig(path)
	}
	if missing || broken {
		// The one failure class local repair can't fix: the config is gone, or
		// unloadable beyond de-duplication. Fetch the authoritative copy from
		// the edge (see recover.go) — the DB there is the source of truth, and
		// every inbound repair channel rides the tunnel this file is the
		// credentials for, so dialing out is the only route.
		if err := recoverConfigFromEdge(cfg.Token, path); err != nil {
			log.Printf("rathole recovery: %s missing/unrepairable and dial-home failed: %v", path, err)
		} else {
			log.Printf("rathole recovery: restored %s from edge", path)
		}
	}

	active, state := unitActive(unit)
	if !active {
		// systemctl stayed unanswerable through runPropRetry's whole window —
		// the unit's real state is unknowable, not "down". Starting blind
		// would log a phantom recovery; leave everything alone and let the
		// next tick decide.
		if strings.HasPrefix(state, "unknown") {
			log.Printf("rathole recovery: skipped tick — systemctl unanswerable, unit state %s", state)
			return
		}
		*silentTicks = 0
		// Unit is down. Clear any failed-state burst counter, then start (not
		// restart — start is a no-op on a healthy unit and never flaps live tunnels).
		_ = sudo("systemctl", "reset-failed", unit)
		if err := sudo("systemctl", "start", unit); err != nil {
			log.Printf("rathole recovery: start %s failed: %v", unit, err)
			return
		}
		log.Printf("rathole recovery: started %s (was down)", unit)
		return
	}

	if hasEstablishedConnection(unit) {
		*silentTicks = 0
		return
	}
	*silentTicks++
	if *silentTicks < wedgedSilentTicks {
		return
	}
	*silentTicks = 0
	// Active per systemd but no established connection for several minutes
	// straight — wedged, not merely reconnecting. `start` would be a no-op
	// here (systemd already thinks it's running); only `restart` kills and
	// relaunches the stuck process.
	if err := sudo("systemctl", "restart", unit); err != nil {
		log.Printf("rathole recovery: restart %s failed (wedged, no established connection for %d checks): %v", unit, wedgedSilentTicks, err)
		return
	}
	log.Printf("rathole recovery: restarted %s — active but had no established connection for %d consecutive checks (wedged)", unit, wedgedSilentTicks)
}

// hasEstablishedConnection reports whether unit's main process currently
// holds at least one ESTABLISHED TCP connection. A healthy rathole-client
// keeps its control channel open continuously; the instant that drops it
// should be actively reconnecting. A measurement failure (ss unavailable,
// PID lookup failure) returns true — don't restart a possibly-healthy
// process just because this specific check couldn't run.
func hasEstablishedConnection(unit string) bool {
	pid := runProp(unit, "MainPID")
	if pid == "" || pid == "0" || pid == "unknown" {
		return true
	}
	out, err := runCommand("sudo", "-n", "ss", "-tnp", "state", "established") // #nosec G204 — fixed argv
	if err != nil {
		return true
	}
	return strings.Contains(out, "pid="+pid+",")
}

// dedupeTomlTables removes duplicate `[table]` definitions, keeping the first
// occurrence of each (along with its surrounding `# gopher-*` marker block, when
// present) and dropping later ones. This is the keep-first complement of the
// server's strip-and-rebuild reconcile, done purely on text because the agent
// has no DB to rebuild from. `[[array.of.tables]]` is left alone — those may
// legally repeat.
func dedupeTomlTables(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	seen := make(map[string]struct{})
	n := len(lines)

	keep := func(block []string, name string) {
		if name != "" {
			if _, dup := seen[name]; dup {
				return // drop the duplicate block entirely
			}
			seen[name] = struct{}{}
		}
		out = append(out, block...)
	}

	for i := 0; i < n; {
		t := strings.TrimSpace(lines[i])

		switch {
		case isMarkerStart(t):
			j := i + 1
			for j < n {
				tj := strings.TrimSpace(lines[j])
				if isMarkerEnd(tj) {
					j++ // include the end marker
					break
				}
				if isMarkerStart(tj) {
					break // malformed: next block starts before this one ended
				}
				j++
			}
			keep(lines[i:j], firstTableName(lines[i:j]))
			i = j
		case isTableHeader(t):
			j := i + 1
			for j < n {
				tj := strings.TrimSpace(lines[j])
				if isTableHeader(tj) || isMarkerStart(tj) {
					break
				}
				j++
			}
			keep(lines[i:j], tableNameFromHeader(t))
			i = j
		default:
			out = append(out, lines[i])
			i++
		}
	}
	return strings.Join(out, "\n")
}

func isMarkerStart(t string) bool {
	return strings.HasPrefix(t, "# gopher-") && strings.Contains(t, "-start:")
}

func isMarkerEnd(t string) bool {
	return strings.HasPrefix(t, "# gopher-") && strings.Contains(t, "-end:")
}

func isTableHeader(t string) bool {
	return strings.HasPrefix(t, "[") && !strings.HasPrefix(t, "[[") && strings.Contains(t, "]")
}

func tableNameFromHeader(t string) string {
	end := strings.Index(t, "]")
	if end <= 1 {
		return ""
	}
	return strings.TrimSpace(t[1:end])
}

func firstTableName(block []string) string {
	for _, line := range block {
		if t := strings.TrimSpace(line); isTableHeader(t) {
			return tableNameFromHeader(t)
		}
	}
	return ""
}

// duplicateTomlTable scans a TOML document for a `[table]` header declared more
// than once and returns the first offender. This is the exact error rathole
// rejects with "redefinition of table" — the dominant way a client.toml ends up
// latently broken (a service block appended without de-duping). It deliberately
// only looks at single-bracket table headers; `[[array.of.tables]]` may legally
// repeat. Comments and the values inside a table are ignored. Not a full TOML
// parser — just enough to catch the one thing that detonates on a rathole
// restart, with no external dependency in the agent.
func duplicateTomlTable(content string) (string, bool) {
	seen := make(map[string]struct{})
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "[[") {
			continue
		}
		end := strings.Index(trimmed, "]")
		if end <= 1 {
			continue
		}
		name := strings.TrimSpace(trimmed[1:end])
		if _, ok := seen[name]; ok {
			return name, true
		}
		seen[name] = struct{}{}
	}
	return "", false
}

// unitReferences reports whether a systemd unit file mentions a given path
// (e.g. an ExecStart or EnvironmentFile still pointing at a legacy location).
func unitReferences(unitPath, needle string) bool {
	data, err := os.ReadFile(unitPath) // #nosec G304 — fixed unit paths
	if err != nil {
		return false
	}
	return strings.Contains(string(data), needle)
}

func sudo(args ...string) error {
	full := append([]string{"-n"}, args...)
	out, err := exec.Command("sudo", full...).CombinedOutput() // #nosec G204 — fixed argv from callers
	if err != nil {
		return wrapCmdErr(err, out)
	}
	return nil
}

// sudoSed rewrites every occurrence of from→to in a file in place, using `#` as
// the sed delimiter so the slashes in paths don't need escaping.
func sudoSed(file, from, to string) error {
	expr := "s#" + from + "#" + to + "#g"
	return sudo("sed", "-i", expr, file)
}

func wrapCmdErr(err error, out []byte) error {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return err
	}
	return &cmdError{err: err, out: trimmed}
}

type cmdError struct {
	err error
	out string
}

func (e *cmdError) Error() string { return e.err.Error() + ": " + e.out }
