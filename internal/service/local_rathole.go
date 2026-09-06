package service

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/config"
	"github.com/smalex-z/gopher/internal/db"
	"github.com/smalex-z/gopher/internal/paths"
	sshpkg "github.com/smalex-z/gopher/internal/ssh"
)

// AddMachineSSHTunnel adds a new [server.services.*-ssh] entry to
// /etc/rathole/server.toml above the custom section, then reloads the service.
func (s *LocalSetupService) AddMachineSSHTunnel(machine *db.Machine) error {
	return s.ReconcileServerConfig()
}

// ReconcileServerConfig rebuilds /etc/rathole/server.toml from the database.
// Gopher-managed entries (machine SSH tunnels, service tunnels) are placed
// ABOVE the custom section. The custom section is user-owned and never
// overwritten — it is the right place for pre-existing or user-added services.
//
// Serialized via reconcileMu: this is called concurrently from tunnel/machine
// create, update, delete, bootstrap, and agent-install with no other
// coordination between callers. Each call re-reads the full DB state and
// rewrites the whole file from scratch, so two overlapping calls can
// interleave such that the one that started first finishes writing last —
// with a DB snapshot that's now stale relative to the other call's changes.
// The lock makes every call fully sequential: whichever call runs last
// always re-reads current state, so the file on disk converges to the truth
// no matter how many calls raced to get here.
//
// MigrateRatholeNoise needs a WIDER hold on this same lock — see its comment
// — so the actual work lives in reconcileServerConfigLocked, callable by a
// caller that already holds reconcileMu, without deadlocking on a second Lock.
func (s *LocalSetupService) ReconcileServerConfig() error {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()
	return s.reconcileServerConfigLocked()
}

// reconcileServerConfigLocked is ReconcileServerConfig's body, assuming the
// caller already holds reconcileMu. Do not call this directly unless you do.
func (s *LocalSetupService) reconcileServerConfigLocked() error {
	configPath := paths.RatholeConfig
	const beginMarker = "# ===== BEGIN CUSTOM CONFIGURATION ====="
	const endMarker = "# ===== END CUSTOM CONFIGURATION ====="

	existing, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read %s: %w", configPath, err)
	}

	content := string(existing)
	userBody := extractCustomBody(content, beginMarker, endMarker)

	machines, err := db.GetMachines()
	if err != nil {
		return fmt.Errorf("failed to load machines: %w", err)
	}
	tunnels, err := db.GetTunnels()
	if err != nil {
		return fmt.Errorf("failed to load tunnels: %w", err)
	}

	// Rebuild gopher-managed config from DB using the canonical generator.
	settings, _ := db.GetSettings()
	bindIP := ""
	noisePriv := ""
	if settings != nil {
		bindIP = settings.BindIP
		noisePriv = settings.RatholeNoisePrivKey
	}
	managedConfig := config.GenerateRatholeServerConfig(machines, tunnels, bindIP, noisePriv)

	// Guardrail: never write a generated config that fails self-validation.
	validation := config.ValidateRatholeConfig(managedConfig, machines, tunnels)
	if !validation.Valid {
		return fmt.Errorf("generated rathole config failed validation: %s", strings.Join(validation.Errors, "; "))
	}

	// If the user has custom services, the placeholder is unnecessary noise.
	if userBody != "" {
		managedConfig = removeTomlSection(managedConfig, "server.services.placeholder")
	}

	// Assemble final file: managed config + user-owned custom section.
	customBlock := beginMarker + "\n"
	if userBody != "" {
		customBlock += userBody + "\n"
	}
	customBlock += endMarker + "\n"
	newContent := strings.TrimRight(managedConfig, "\n") + "\n\n" + customBlock

	// In-place write (truncate + rewrite, preserving the inode). Critical:
	// rathole 0.5's notify watcher loses its IN_MODIFY subscription if the
	// inode is replaced via rename(2) — and a `systemctl reload` would
	// drop every active tunnel as the listeners restart. With the inode
	// preserved, inotify fires, rathole's notify watcher hot-reloads the
	// new config, and existing connections are kept untouched.
	if err := writeLocalFileInPlace(configPath, newContent); err != nil {
		return fmt.Errorf("failed to write %s: %w", configPath, err)
	}

	// No restart/start kick: rathole hot-reloads the rewritten server.toml in
	// place via its inotify notify watcher, and liveness is owned by gopher's
	// supervisor (which restarts it if it ever exits). We never restart rathole —
	// that would drop every live tunnel.
	return nil
}

// legacyCustomMarkers pairs older wordings of the custom-section banner with
// the current one. The wrapper text has changed at least once across
// released versions; a reconcile that only recognises the CURRENT literal
// string treats a file using older wording as having NO custom section at
// all — silently dropping any user-added entries (e.g. a hand-added
// `[server.services.x]` block) on the next rewrite. extractCustomBody falls
// back through this list so old installs keep their custom content; the
// caller then re-writes the file under the current marker, which
// self-heals the convention on this same reconcile pass.
var legacyCustomMarkers = []struct{ begin, end string }{
	{"# Add your own rathole service entries here. Gopher will not modify this section.", ""},
}

// extractCustomBody pulls the user-owned custom section out of an existing
// server.toml. Tries the current marker first, then falls back through
// legacyCustomMarkers so a config file predating a marker-wording change
// doesn't lose its custom content — see legacyCustomMarkers for why this
// matters. Returns "" (nothing to preserve) only when no marker, current or
// legacy, is found at all.
func extractCustomBody(content, beginMarker, endMarker string) string {
	if bIdx := strings.Index(content, beginMarker); bIdx != -1 {
		below := content[bIdx+len(beginMarker):]
		body := below
		if eIdx := strings.Index(below, endMarker); eIdx != -1 {
			body = below[:eIdx]
		}
		return strings.TrimSpace(stripGopherServiceSections(body))
	}
	for _, m := range legacyCustomMarkers {
		bIdx := strings.Index(content, m.begin)
		if bIdx == -1 {
			continue
		}
		below := content[bIdx+len(m.begin):]
		body := below
		if m.end != "" {
			if eIdx := strings.Index(below, m.end); eIdx != -1 {
				body = below[:eIdx]
			}
		}
		return strings.TrimSpace(stripGopherServiceSections(body))
	}
	return ""
}

// stripGopherServiceSections removes only marker-delimited Gopher-managed
// blocks (between # gopher-*-start: / # gopher-*-end: lines) from a TOML
// string. Used to clean legacy entries that were incorrectly placed inside the
// custom section by older versions. Does NOT strip sections by name prefix, so
// user entries that happen to share naming patterns are never deleted.
func stripGopherServiceSections(content string) string {
	var out []string
	skip := false
	for _, line := range strings.Split(content, "\n") {
		stripped := strings.TrimSpace(line)
		if strings.HasPrefix(stripped, "# gopher-machine-start:") ||
			strings.HasPrefix(stripped, "# gopher-machine-agent-start:") ||
			strings.HasPrefix(stripped, "# gopher-tunnel-start:") {
			skip = true
			continue
		}
		if strings.HasPrefix(stripped, "# gopher-machine-end:") ||
			strings.HasPrefix(stripped, "# gopher-machine-agent-end:") ||
			strings.HasPrefix(stripped, "# gopher-tunnel-end:") {
			skip = false
			continue
		}
		if !skip {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// AddServiceTunnel adds a user-defined service tunnel to the server's
// /etc/rathole/server.toml and pushes the regenerated client.toml to the
// machine. The agent back-channel is preferred; SSH/SFTP is the fallback
// for machines that don't yet have the agent installed.
//
// Order of operations is client → server → caddy. Pushing client.toml first
// is the cheapest step to fail (network issue, agent down, missing SSH key)
// and failing early means we don't leave an orphan rathole listener bound
// on the VPS waiting for a client that never connects. Each later step's
// failure is also recoverable: the next reconcile rebuilds server.toml from
// the DB, and Caddy reload is idempotent.
func (s *LocalSetupService) AddServiceTunnel(tunnel *db.Tunnel, machine *db.Machine) error {
	settings, err := db.GetSettings()
	if err != nil {
		return err
	}

	// --- 1. Push client.toml first ---
	// Cheapest step to fail. If the agent is unreachable / SSH key is missing
	// / the machine is offline, we want that error before the VPS is told to
	// listen on a fresh port that nothing will ever connect to.
	machineTunnels, err := db.GetTunnelsByMachine(machine.ID)
	if err != nil {
		return fmt.Errorf("failed to load machine tunnels: %w", err)
	}
	ratholeHost := ratholeHostFromSettings(settings)
	noisePub := settings.RatholeNoisePubKey
	transformer := func(existing string) (string, error) {
		return mergeClientManagedConfig(existing, machine, machineTunnels, ratholeHost, noisePub)
	}
	if err := s.updateClientToml(machine, transformer); err != nil {
		return fmt.Errorf("failed to write client.toml on machine: %w", err)
	}

	// --- 2. Update server.toml (full reconcile ensures consistency) ---
	if err := s.ReconcileServerConfig(); err != nil {
		return fmt.Errorf("failed to update server.toml: %w", err)
	}

	// --- 3. Update managed Caddy entry if subdomain is set. Private tunnels
	// still get a Caddy block — they're reverse-proxy-only (Caddy reaches their
	// 127.0.0.1-bound rathole port), they just have no raw public port. Only
	// UDP has no HTTP routing. ---
	if tunnel.Subdomain != "" && settings.Domain != "" && tunnel.Transport != "udp" {
		if err := ensureManagedCaddyLayout(); err != nil {
			return fmt.Errorf("failed to prepare Caddy managed layout: %w", err)
		}
		if err := writeLocalFile(managedRouterCaddyPath(), buildRouterCaddyBlock(settings.Domain, settings.BindIP)); err != nil {
			return fmt.Errorf("failed to write router Caddy file: %w", err)
		}
		managedPath := managedTunnelCaddyPath(tunnel.ID)
		block := buildTunnelCaddyBlock(tunnel.Subdomain, settings.Domain, tunnel.RatholePort, tunnel.NoTLS, tunnel.BotProtectionEnabled || tunnel.AuthEnabled, settings.BindIP, tunnel.TLSSkipVerify, tunnel.Private)
		if err := writeLocalFile(managedPath, block); err != nil {
			return fmt.Errorf("failed to write tunnel Caddy file %s: %w", managedPath, err)
		}
		// Caddy reload is the user-visible step: a syntax error in the
		// custom-config block fails here and the new tunnel never routes.
		// Propagate so the API returns the failure rather than reporting
		// success while the subdomain 502s.
		if err := caddyReload(); err != nil {
			return fmt.Errorf("caddy reload failed: %w", err)
		}
	}
	return nil
}

// RemoveServiceTunnelClient removes only the tunnel's section from the
// client machine's /etc/rathole/client.toml (or user-level fallback).
func (s *LocalSetupService) RemoveServiceTunnelClient(tunnel *db.Tunnel, machine *db.Machine) error {
	if tunnel == nil || machine == nil {
		return nil
	}
	transformer := func(existing string) (string, error) {
		return removeClientManagedSection(existing, "tunnel", tunnel.ID), nil
	}
	return s.updateClientToml(machine, transformer)
}

// updateClientToml is the read-transform-write loop for a machine's
// /etc/rathole/client.toml. It prefers the gopher-agent back-channel and
// falls back to SSH/SFTP for legacy machines that don't have the agent yet.
//
// The fallback is deliberate: until every machine is migrated, both transports
// must work. Once the migration UI flips every machine, this fallback can be
// dropped and the SSH path retired.
func (s *LocalSetupService) updateClientToml(machine *db.Machine, transform func(existing string) (string, error)) error {
	if machine == nil {
		return fmt.Errorf("nil machine")
	}

	var pushErr error
	if machine.AgentInstalled && machine.AgentRemotePort > 0 {
		if pushErr = s.updateClientTomlViaAgent(machine, transform); pushErr == nil {
			clearConfigPushPending(machine)
			return nil
		}
		// Agent failed (network, timeout, permission). Fall back to SSH so
		// the operation still completes; log so we can spot persistent
		// agent issues that should be debugged.
		log.Printf("agent client.toml push failed for machine %s (%s): %v — falling back to SSH", machine.ID, machine.Name, pushErr)
	}

	// SSH fallback only when a usable private key is stored. When the operator
	// has deleted the private key (public-only), there's no SSH transport — the
	// agent is the sole path. Flag ConfigPushPending here (not the caller — no
	// caller does) so HealthService.maybeRetryConfigPush re-pushes via the agent
	// once it reconnects; otherwise a transient agent outage during a tunnel
	// change would leave the origin's client.toml stale forever.
	if !machineHasSSHPrivateKey(machine) {
		if err := db.SetMachineConfigPushPending(machine.ID, true); err != nil {
			log.Printf("mark config_push_pending for %s (%s): %v", machine.ID, machine.Name, err)
		}
		if pushErr == nil {
			pushErr = fmt.Errorf("no agent and no stored SSH private key for %s — config push deferred", machine.Name)
		}
		return pushErr
	}

	if pushErr = s.updateClientTomlViaSSH(machine, transform); pushErr == nil {
		clearConfigPushPending(machine)
		return nil
	}
	// Both transports failed (agent unreachable or absent, SSH failed too) —
	// flag the machine so HealthService.maybeRetryConfigPush replays this push
	// once the machine reports reachable again. Without this, a transient
	// outage during a tunnel change left client.toml stale forever: the flag
	// was only ever set in the no-private-key branch above, so machines WITH a
	// stored key had no retry path at all.
	if err := db.SetMachineConfigPushPending(machine.ID, true); err != nil {
		log.Printf("mark config_push_pending for %s (%s): %v", machine.ID, machine.Name, err)
	}
	return pushErr
}

// machineHasSSHPrivateKey reports whether the server holds a usable SSH private
// key for this machine — i.e. it can still act as an SSH client into the origin.
// False when no key is assigned or the operator deleted the private half
// (public-only). Server→origin control runs over the agent; SSH is an optional
// fallback, so callers gate on this rather than attempting a doomed SSH dial.
func machineHasSSHPrivateKey(machine *db.Machine) bool {
	if machine == nil {
		return false
	}
	key, err := db.GetSSHKeyForMachine(machine)
	return err == nil && key != nil && key.PrivateKey != ""
}

// clearConfigPushPending is the success-hook for any client.toml push path.
// Centralised here (not in each push variant) so the agent and SSH transports
// share the same flag-clear semantics: a config push that lands successfully
// means the machine is current, regardless of how it got there.
func clearConfigPushPending(machine *db.Machine) {
	if machine == nil || !machine.ConfigPushPending {
		return // fast-path: nothing to clear
	}
	if err := db.SetMachineConfigPushPending(machine.ID, false); err != nil {
		log.Printf("clear config_push_pending for %s (%s): %v", machine.ID, machine.Name, err)
		return
	}
	machine.ConfigPushPending = false
}

func (s *LocalSetupService) updateClientTomlViaAgent(machine *db.Machine, transform func(existing string) (string, error)) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := NewAgentClient(machine)

	existing, err := client.GetRatholeConfig(ctx)
	if err != nil {
		return fmt.Errorf("agent get config: %w", err)
	}
	updated, err := transform(existing)
	if err != nil {
		return err
	}
	if updated == existing {
		// No-op write would still bump mtime and cause notify reload churn.
		return nil
	}
	if err := client.PutRatholeConfig(ctx, updated); err != nil {
		return fmt.Errorf("agent put config: %w", err)
	}
	return nil
}

func (s *LocalSetupService) updateClientTomlViaSSH(machine *db.Machine, transform func(existing string) (string, error)) error {
	sshKey, sshKeyErr := db.GetSSHKeyForMachine(machine)
	if sshKeyErr != nil {
		return fmt.Errorf("no server SSH key available; machine may need to be re-bootstrapped")
	}
	// Don't even attempt SSH without a stored private key — dialing with an empty
	// key can only fail, and the retry loop below would burn 30s doing it. The
	// agent is the transport for public-only machines.
	if sshKey.PrivateKey == "" {
		return fmt.Errorf("no stored SSH private key (public-only) — config push runs via the agent")
	}
	var sshClient *sshpkg.SSHClient
	var sshDialErr error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			time.Sleep(5 * time.Second)
		}
		sshClient, sshDialErr = sshpkg.NewClient(TunnelDialHost(machine), machine.TunnelPort, machine.Username, sshKey.PrivateKey)
		if sshDialErr == nil {
			break
		}
	}
	if sshDialErr != nil {
		return fmt.Errorf("failed to SSH into machine via tunnel (port %d) after retries: %w", machine.TunnelPort, sshDialErr)
	}
	defer sshClient.Close()

	existing, err := sshClient.Execute("cat " + paths.RatholeClientConfig + " 2>/dev/null || cat " + paths.LegacyRatholeClientConfig + " 2>/dev/null || cat ~/.config/rathole/client.toml 2>/dev/null")
	if err != nil {
		existing = ""
	}
	updated, err := transform(existing)
	if err != nil {
		return err
	}

	// Resolve absolute config path (SFTP cannot expand $HOME). Prefer the
	// consolidated /etc/gopher path (agent-migrated machines), fall back to the
	// legacy /etc/rathole path (machines without the migrated agent), then the
	// user-level config for rootless/no-systemd boxes.
	configPath := paths.RatholeClientConfig
	if _, err2 := sshClient.Execute("test -f " + paths.RatholeClientConfig); err2 != nil {
		if _, err3 := sshClient.Execute("test -f " + paths.LegacyRatholeClientConfig); err3 == nil {
			configPath = paths.LegacyRatholeClientConfig
		} else {
			homeDir, _ := sshClient.Execute("echo $HOME")
			homeDir = strings.TrimSpace(homeDir)
			if homeDir == "" {
				homeDir = "/home/" + machine.Username
			}
			configPath = homeDir + "/.config/rathole/client.toml"
			_, _ = sshClient.Execute("mkdir -p " + homeDir + "/.config/rathole")
		}
	}

	// In-place write: rathole's notify watcher subscribes to the inode of
	// client.toml, so a rename-based install (UploadFileSudo) silently
	// breaks the subscription and forces a full systemctl restart on
	// every push — which drops every connected tunnel. UploadFileSudoInPlace
	// uses `sudo tee` to truncate-and-rewrite, preserving the inode so
	// rathole hot-reloads the new config without flapping listeners.
	if err := sshClient.UploadFileSudoInPlace([]byte(updated), configPath, machine.Username); err != nil {
		return fmt.Errorf("failed to write client.toml on machine: %w", err)
	}

	// systemctl start is a no-op on a healthy unit and covers the "stopped"
	// case without flapping existing tunnels.
	_, _ = sshClient.Execute(`{ [ "$(id -u)" -eq 0 ] && systemctl start rathole-client || sudo -n systemctl start rathole-client; } 2>/dev/null; systemctl --user start rathole-client 2>/dev/null; true`)
	return nil
}

// RemoveServiceTunnelCaddy removes only the managed Caddy entry for a tunnel.
func (s *LocalSetupService) RemoveServiceTunnelCaddy(tunnel *db.Tunnel) error {
	if tunnel == nil || tunnel.Subdomain == "" {
		return nil
	}
	settings, err := db.GetSettings()
	if err != nil || settings.Domain == "" {
		return nil
	}

	managedPath := managedTunnelCaddyPath(tunnel.ID)
	if removeErr := os.Remove(managedPath); removeErr != nil && !os.IsNotExist(removeErr) {
		// File owned by caddy / root — fall back to sudo rm. We log the
		// fallback path's failure but don't propagate: the next reconcile
		// will retry, and the tunnel row is gone from DB either way.
		if err := runSudoCommand("rm", "-f", managedPath); err != nil {
			log.Printf("sudo rm of caddy file %s failed: %v", managedPath, err)
		}
	}
	if err := caddyReload(); err != nil {
		log.Printf("caddy reload (post tunnel-remove) failed: %v", err)
	}
	return nil
}

// WriteServiceTunnelCaddy (re)writes the managed conf.d/<id>.caddy block for a
// tunnel from its current state and reloads Caddy. Mirror of
// RemoveServiceTunnelCaddy. The caller decides *when* to call it (subdomain set,
// HTTP, not private); this renders the tunnel as it stands and no-ops when there
// is no subdomain or no configured domain to route under.
func (s *LocalSetupService) WriteServiceTunnelCaddy(tunnel *db.Tunnel) error {
	if tunnel == nil || tunnel.Subdomain == "" {
		return nil
	}
	settings, err := db.GetSettings()
	if err != nil || settings.Domain == "" {
		return nil
	}
	managedPath := managedTunnelCaddyPath(tunnel.ID)
	block := buildTunnelCaddyBlock(tunnel.Subdomain, settings.Domain, tunnel.RatholePort, tunnel.NoTLS, tunnel.BotProtectionEnabled || tunnel.AuthEnabled, settings.BindIP, tunnel.TLSSkipVerify, tunnel.Private)
	if err := writeLocalFile(managedPath, block); err != nil {
		return fmt.Errorf("write caddy block for %s: %w", tunnel.ID, err)
	}
	if err := caddyReload(); err != nil {
		return fmt.Errorf("caddy reload failed: %w", err)
	}
	return nil
}

// RemoveServiceTunnel keeps backwards compatibility with older callers by
// performing full tunnel cleanup in the canonical order.
func (s *LocalSetupService) RemoveServiceTunnel(tunnel *db.Tunnel, machine *db.Machine) {
	_ = s.RemoveServiceTunnelClient(tunnel, machine)
	_ = s.ReconcileServerConfig()
	_ = s.RemoveServiceTunnelCaddy(tunnel)
}

// RemoveMachineClient triggers full cleanup on the client machine: stops
// gopher-agent + rathole-client, removes their binaries, configs, sudoers
// rule, and the VPS public key from authorized_keys.
//
// Two transports:
//   - agent path: POST /uninstall to the agent over the rathole back-channel.
//     The agent spawns a detached worker (own session via setsid) that
//     outlives both the agent process and the rathole tunnel, then runs the
//     canonical /usr/local/bin/gopher-uninstall script. No SSH involved.
//   - SSH fallback: for legacy machines without the agent, exec the same
//     /usr/local/bin/gopher-uninstall script over SSH using nohup + setsid
//     so it survives the SSH session drop.
//
// Errors are best-effort — callers should proceed with DB cleanup even on
// failure (the worst case is a stale client; the operator can re-run
// gopher-uninstall manually on the box).
func (s *LocalSetupService) RemoveMachineClient(machine *db.Machine) error {
	if machine.TunnelPort == 0 {
		return fmt.Errorf("machine has no tunnel port")
	}

	if machine.AgentInstalled && machine.AgentRemotePort > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := NewAgentClient(machine).Uninstall(ctx); err == nil {
			return nil
		} else {
			// Agent unreachable — likely already partially torn down. Fall
			// through to SSH so we still try to leave the box clean.
			log.Printf("agent uninstall for %s (%s) failed, falling back to SSH: %v", machine.ID, machine.Name, err)
		}
	}

	return s.removeMachineClientViaSSH(machine)
}

// removeMachineClientViaSSH is the legacy delete path. Used for machines that
// haven't been migrated to the agent yet. Identical end-state to the agent
// path — invokes the same on-disk gopher-uninstall script in a detached
// worker via setsid.
//
// We precheck that gopher-uninstall both exists and is allowed under
// NOPASSWD sudoers BEFORE firing the detached worker. The detached worker
// runs `sudo -n` (non-interactive); without that sudoers line it silently
// exits 1, and because the worker is backgrounded we never observe the
// failure — the box is left dirty and the operator has no signal as to why.
// The precheck collapses both failure modes into a clear error.
func (s *LocalSetupService) removeMachineClientViaSSH(machine *db.Machine) error {
	sshKey, err := db.GetSSHKeyForMachine(machine)
	if err != nil {
		return fmt.Errorf("no server SSH key available")
	}
	if sshKey.PrivateKey == "" {
		return fmt.Errorf("no stored SSH private key (public-only) — client teardown runs via the agent")
	}

	sshClient, err := sshpkg.NewClient(TunnelDialHost(machine), machine.TunnelPort, machine.Username, sshKey.PrivateKey)
	if err != nil {
		return fmt.Errorf("failed to SSH into machine via tunnel (port %d): %w", machine.TunnelPort, err)
	}
	defer sshClient.Close()

	// Precheck 1: script is on disk + executable. `sh -c` to keep it portable
	// across the various login shells the bootstrap script may have run as.
	if out, perr := sshClient.Execute("test -x /usr/local/bin/gopher-uninstall && echo OK"); perr != nil || strings.TrimSpace(out) != "OK" {
		return fmt.Errorf("gopher-uninstall not found on machine (was bootstrap completed? expected at /usr/local/bin/gopher-uninstall): %v", perr)
	}
	// Precheck 2: passwordless sudo for the script. `sudo -nl <cmd>` exits 0
	// only when the user has a NOPASSWD entry for that exact path; otherwise
	// it prints "a password is required" or "may not run" to stderr and exits
	// non-zero. Capture that for the oper ator-visible error message.
	if out, perr := sshClient.Execute("sudo -nl /usr/local/bin/gopher-uninstall 2>&1"); perr != nil {
		return fmt.Errorf("client lacks NOPASSWD sudo for gopher-uninstall (re-run bootstrap to refresh /etc/sudoers.d/gopher; %s): %w", strings.TrimSpace(out), perr)
	}

	// Fire detached uninstall. setsid + nohup keeps the script alive when
	// gopher-uninstall stops rathole-client (which kills our SSH session).
	// Output goes to /tmp/.gopher-uninstall.log on the client for post-mortem.
	_, err = sshClient.Execute(`setsid nohup sh -c 'sleep 3; sudo -n /usr/local/bin/gopher-uninstall' >/tmp/.gopher-uninstall.log 2>&1 </dev/null &`)
	if err != nil {
		return fmt.Errorf("failed to spawn remote uninstall worker: %w", err)
	}
	return nil
}

// removeSSHPublicKey removes a public key line from an authorized_keys document.
// Lines are matched by their base64 key blob (the second field), so key-type
// prefix and trailing comment are ignored.
func removeSSHPublicKey(authorizedKeys, publicKey string) string {
	targetBlob := sshKeyBlob(publicKey)
	if targetBlob == "" {
		return authorizedKeys
	}
	var out []string
	for _, line := range strings.Split(authorizedKeys, "\n") {
		if sshKeyBlob(line) != targetBlob {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// sshKeyBlob returns the base64 key blob (second whitespace-separated field)
// from an authorized_keys line, or "" for blank/malformed lines.
func sshKeyBlob(line string) string {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 2 {
		return ""
	}
	return fields[1]
}

func buildClientTunnelSection(tunnel *db.Tunnel) string {
	token := tunnel.RatholeToken
	if token == "" {
		token = tunnel.ID // backward compat
	}
	transport := tunnel.Transport
	if transport != "udp" {
		transport = "tcp"
	}
	return fmt.Sprintf(`# gopher-tunnel-start: %s
[client.services.tunnel-%s]
type = "%s"
token = "%s"
local_addr = "localhost:%d"
# gopher-tunnel-end: %s
`, tunnel.ID, tunnel.ID, transport, token, tunnel.LocalPort, tunnel.ID)
}

func buildClientMachineSection(machine *db.Machine) string {
	if machine == nil || machine.ID == "" || machine.RatholeSSHToken == "" {
		return ""
	}
	return fmt.Sprintf(`# gopher-machine-start: %s
[client.services.machine-%s-ssh]
type = "tcp"
token = "%s"
local_addr = "0.0.0.0:22"
# gopher-machine-end: %s
`, machine.ID, machine.ID, machine.RatholeSSHToken, machine.ID)
}

// buildClientMachineAgentSection emits the rathole client entry that connects
// the local gopher-agent (127.0.0.1:AgentLocalPort) to the VPS-side bind so
// the control plane can reach the agent. Empty when the machine doesn't have
// agent fields populated — legacy machines without the agent fall through.
//
// Gates on the same predicate as the server-side emitter (see
// config.GenerateRatholeServerConfig) so the two sides can never disagree
// about whether to write the entry. Without this symmetry, a row mid-
// allocation could end up with the client config writing the agent
// service while the server hasn't bound the matching port — rathole-client
// then loops "service not found" until the next reconcile fills in the gap.
func buildClientMachineAgentSection(machine *db.Machine) string {
	if machine == nil || machine.ID == "" {
		return ""
	}
	if machine.AgentRatholeToken == "" || machine.AgentLocalPort == 0 || machine.AgentRemotePort == 0 {
		return ""
	}
	return fmt.Sprintf(`# gopher-machine-agent-start: %s
[client.services.machine-%s-agent]
type = "tcp"
token = "%s"
local_addr = "127.0.0.1:%d"
# gopher-machine-agent-end: %s
`, machine.ID, machine.ID, machine.AgentRatholeToken, machine.AgentLocalPort, machine.ID)
}

func mergeClientManagedConfig(existing string, machine *db.Machine, tunnels []db.Tunnel, ratholeHost, noisePubKey string) (string, error) {
	base := strings.TrimSpace(existing)
	if base == "" {
		ratholeHost = strings.TrimSpace(ratholeHost)
		if ratholeHost == "" {
			return "", fmt.Errorf("no existing client.toml and no domain configured; bootstrap the machine again with a reachable server host")
		}
		base = fmt.Sprintf("[client]\nremote_addr = \"%s:2333\"\n", ratholeHost)
	}

	machineSection := strings.TrimSpace(buildClientMachineSection(machine))
	// An SSH-enabled machine must have its SSH section — a missing token there
	// means an incomplete bootstrap. But an agent-only machine (no SSH tunnel,
	// TunnelPort 0) legitimately has no SSH section; its agent back-channel +
	// service tunnels below carry the config. Only error for the former.
	if machineSection == "" && machine != nil && machine.TunnelPort != 0 {
		return "", fmt.Errorf("machine is missing SSH tunnel token; bootstrap the machine again")
	}

	// Synchronise the [client.transport] block with what the server currently
	// expects. Without this step, a machine that was bootstrapped before
	// noise was enabled keeps its plaintext-only client.toml across config
	// pushes and silently fails to reconnect the moment the server flips to
	// noise. Strip any stale block and re-emit from the canonical key.
	base = stripClientTransportSection(base)

	cleaned := stripClientManagedSections(base)
	updated := strings.TrimRight(cleaned, "\n")
	if block := strings.TrimRight(config.RenderClientNoiseTransport(noisePubKey), "\n"); block != "" {
		updated += "\n\n" + block
	}

	sections := []string{}
	if machineSection != "" {
		sections = append(sections, machineSection)
	}
	if agentSection := strings.TrimSpace(buildClientMachineAgentSection(machine)); agentSection != "" {
		sections = append(sections, agentSection)
	}
	for i := range tunnels {
		if tunnels[i].MachineID != "" && machine != nil && tunnels[i].MachineID != machine.ID {
			continue
		}
		sections = append(sections, strings.TrimSpace(buildClientTunnelSection(&tunnels[i])))
	}

	for _, section := range sections {
		if section == "" {
			continue
		}
		if strings.TrimSpace(updated) == "" {
			updated = section
		} else {
			updated += "\n\n" + section
		}
	}

	if !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}
	return updated, nil
}

func stripClientManagedSections(content string) string {
	stripped := stripClientManagedMarkerBlocks(content)
	stripped = removeTomlSectionsWithPrefix(stripped, "client.services.tunnel-")
	stripped = removeTomlSectionsWithPrefix(stripped, "client.services.machine-")
	return stripped
}

// stripClientTransportSection removes the [client.transport] header and its
// [client.transport.noise] subsection. Used during config merges so the
// canonical transport block can be re-emitted from the current server pubkey
// — if we only stripped the [client.transport] header, the orphan
// [client.transport.noise] section below it would cause rathole to error.
func stripClientTransportSection(content string) string {
	content = removeTomlSection(content, "client.transport")
	content = removeTomlSection(content, "client.transport.noise")
	return content
}

func stripClientManagedMarkerBlocks(content string) string {
	lines := strings.Split(content, "\n")
	result := make([]string, 0, len(lines))
	skip := ""

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if skip == "" {
			// Order matters: machine-agent must be checked before machine
			// because both share the "# gopher-machine" prefix.
			if strings.HasPrefix(trimmed, "# gopher-machine-agent-start:") {
				skip = "machine-agent"
				continue
			}
			if strings.HasPrefix(trimmed, "# gopher-machine-start:") {
				skip = "machine"
				continue
			}
			if strings.HasPrefix(trimmed, "# gopher-tunnel-start:") {
				skip = "tunnel"
				continue
			}
			result = append(result, line)
			continue
		}

		if skip == "machine-agent" && strings.HasPrefix(trimmed, "# gopher-machine-agent-end:") {
			skip = ""
			continue
		}
		if skip == "machine" && strings.HasPrefix(trimmed, "# gopher-machine-end:") {
			skip = ""
			continue
		}
		if skip == "tunnel" && strings.HasPrefix(trimmed, "# gopher-tunnel-end:") {
			skip = ""
			continue
		}
	}

	return strings.Join(result, "\n")
}

func removeTomlSectionsWithPrefix(content, sectionPrefix string) string {
	lines := strings.Split(content, "\n")
	result := make([]string, 0, len(lines))
	skip := false
	headerPrefix := "[" + sectionPrefix

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, headerPrefix) && strings.HasSuffix(trimmed, "]") {
			skip = true
			continue
		}
		if skip && strings.HasPrefix(trimmed, "[") {
			skip = false
		}
		if !skip {
			result = append(result, line)
		}
	}

	return strings.Join(result, "\n")
}

func removeClientManagedSection(content, entryType, id string) string {
	if id == "" || entryType == "" {
		return content
	}
	startMarker := fmt.Sprintf("# gopher-%s-start: %s", entryType, id)
	endMarker := fmt.Sprintf("# gopher-%s-end: %s", entryType, id)

	lines := strings.Split(content, "\n")
	result := make([]string, 0, len(lines))
	skip := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == startMarker {
			skip = true
			continue
		}
		if skip {
			if trimmed == endMarker {
				skip = false
			}
			continue
		}
		result = append(result, line)
	}

	updated := strings.Join(result, "\n")
	if entryType == "tunnel" {
		updated = removeTomlSection(updated, fmt.Sprintf("client.services.tunnel-%s", id))
	}
	if entryType == "machine" {
		updated = removeTomlSection(updated, fmt.Sprintf("client.services.machine-%s-ssh", id))
	}
	if entryType == "machine-agent" {
		updated = removeTomlSection(updated, fmt.Sprintf("client.services.machine-%s-agent", id))
	}
	return updated
}

// removeTomlSection removes a [section.name] block from a TOML string.
func removeTomlSection(content, sectionName string) string {
	lines := strings.Split(content, "\n")
	result := make([]string, 0, len(lines))
	skip := false
	header := fmt.Sprintf("[%s]", sectionName)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == header {
			skip = true
			continue
		}
		if skip && strings.HasPrefix(trimmed, "[") {
			skip = false
		}
		if !skip {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}
