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
func (s *LocalSetupService) ReconcileServerConfig() error {
	const configPath = "/etc/rathole/server.toml"
	const beginMarker = "# ===== BEGIN CUSTOM CONFIGURATION ====="
	const endMarker = "# ===== END CUSTOM CONFIGURATION ====="

	existing, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read %s: %w", configPath, err)
	}

	content := string(existing)

	// Extract the user-owned custom section.
	userBody := ""
	if bIdx := strings.Index(content, beginMarker); bIdx != -1 {
		below := content[bIdx+len(beginMarker):]
		if eIdx := strings.Index(below, endMarker); eIdx != -1 {
			userBody = below[:eIdx]
		} else {
			userBody = below
		}
	}

	// Strip any gopher-managed entries that old versions may have placed inside
	// the custom section, then normalise whitespace.
	userBody = strings.TrimSpace(stripGopherServiceSections(userBody))

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
	if settings != nil {
		bindIP = settings.BindIP
	}
	managedConfig := config.GenerateRatholeServerConfig(machines, tunnels, bindIP)

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

	if err := writeLocalFile(configPath, newContent); err != nil {
		return fmt.Errorf("failed to write %s: %w", configPath, err)
	}

	// rathole's notify watcher picks up the config change via inotify — no
	// signal or restart needed when it's already running. Sending an extra
	// SIGHUP causes a second reload ~1s after notify's, which churns every
	// listener (including the dashboard's own tunnel) twice per machine-add.
	// systemctl start is a no-op on an active unit; covers the "not running"
	// case without forcing a restart on healthy ones. The error is logged
	// rather than propagated because the on-disk config IS already updated
	// — the caller's intent (DB→disk) succeeded; only the runtime kick
	// failed, and the next reconcile cycle (or a manual systemctl start)
	// will pick it up.
	if err := systemctlStart("rathole-server"); err != nil {
		log.Printf("rathole-server start (post-reconcile) failed: %v", err)
	}
	return nil
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
			strings.HasPrefix(stripped, "# gopher-tunnel-start:") {
			skip = true
			continue
		}
		if strings.HasPrefix(stripped, "# gopher-machine-end:") ||
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
	ratholeHost := settings.ServerHost
	if ratholeHost == "" {
		ratholeHost = settings.Domain
	}
	transformer := func(existing string) (string, error) {
		return mergeClientManagedConfig(existing, machine, machineTunnels, ratholeHost)
	}
	if err := s.updateClientToml(machine, transformer); err != nil {
		return fmt.Errorf("failed to write client.toml on machine: %w", err)
	}

	// --- 2. Update server.toml (full reconcile ensures consistency) ---
	if err := s.ReconcileServerConfig(); err != nil {
		return fmt.Errorf("failed to update server.toml: %w", err)
	}

	// --- 3. Update managed Caddy entry if subdomain is set (TCP only; UDP/private have no HTTP routing) ---
	if tunnel.Subdomain != "" && settings.Domain != "" && tunnel.Transport != "udp" && !tunnel.Private {
		if err := ensureManagedCaddyLayout(); err != nil {
			return fmt.Errorf("failed to prepare Caddy managed layout: %w", err)
		}
		if err := writeLocalFile(managedRouterCaddyPath(), buildRouterCaddyBlock(settings.Domain, settings.BindIP)); err != nil {
			return fmt.Errorf("failed to write router Caddy file: %w", err)
		}
		managedPath := managedTunnelCaddyPath(tunnel.ID)
		block := buildTunnelCaddyBlock(tunnel.Subdomain, settings.Domain, tunnel.RatholePort, tunnel.NoTLS, tunnel.BotProtectionEnabled, settings.BindIP, tunnel.TLSSkipVerify)
		if err := writeLocalFile(managedPath, block); err != nil {
			return fmt.Errorf("failed to write tunnel Caddy file %s: %w", managedPath, err)
		}
		// Caddy reload is the user-visible step: a syntax error in the
		// custom-config block fails here and the new tunnel never routes.
		// Propagate so the API returns the failure rather than reporting
		// success while the subdomain 502s.
		if err := systemctlReload("caddy"); err != nil {
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

	if machine.AgentInstalled && machine.AgentRemotePort > 0 {
		if err := s.updateClientTomlViaAgent(machine, transform); err == nil {
			return nil
		} else {
			// Agent failed (network, timeout, permission). Fall back to SSH so
			// the operation still completes; log so we can spot persistent
			// agent issues that should be debugged.
			log.Printf("agent client.toml push failed for machine %s (%s): %v — falling back to SSH", machine.ID, machine.Name, err)
		}
	}

	return s.updateClientTomlViaSSH(machine, transform)
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

	existing, err := sshClient.Execute("cat /etc/rathole/client.toml 2>/dev/null || cat ~/.config/rathole/client.toml 2>/dev/null")
	if err != nil {
		existing = ""
	}
	updated, err := transform(existing)
	if err != nil {
		return err
	}

	// Resolve absolute config path (SFTP cannot expand $HOME).
	configPath := "/etc/rathole/client.toml"
	if _, err2 := sshClient.Execute("test -f /etc/rathole/client.toml"); err2 != nil {
		homeDir, _ := sshClient.Execute("echo $HOME")
		homeDir = strings.TrimSpace(homeDir)
		if homeDir == "" {
			homeDir = "/home/" + machine.Username
		}
		configPath = homeDir + "/.config/rathole/client.toml"
		_, _ = sshClient.Execute("mkdir -p " + homeDir + "/.config/rathole")
	}

	if err := sshClient.UploadFileSudo([]byte(updated), configPath, machine.Username); err != nil {
		return fmt.Errorf("failed to write client.toml on machine: %w", err)
	}

	// rathole's notify watcher reloads on file change; systemctl start is a
	// no-op on a healthy unit and covers the "stopped" case without flapping
	// existing tunnels.
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
	if err := systemctlReload("caddy"); err != nil {
		log.Printf("caddy reload (post tunnel-remove) failed: %v", err)
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
	// non-zero. Capture that for the operator-visible error message.
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
func buildClientMachineAgentSection(machine *db.Machine) string {
	if machine == nil || machine.ID == "" || machine.AgentRatholeToken == "" || machine.AgentLocalPort == 0 {
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

func mergeClientManagedConfig(existing string, machine *db.Machine, tunnels []db.Tunnel, ratholeHost string) (string, error) {
	base := strings.TrimSpace(existing)
	if base == "" {
		ratholeHost = strings.TrimSpace(ratholeHost)
		if ratholeHost == "" {
			return "", fmt.Errorf("no existing client.toml and no domain configured; bootstrap the machine again with a reachable server host")
		}
		base = fmt.Sprintf("[client]\nremote_addr = \"%s:2333\"\n", ratholeHost)
	}

	machineSection := strings.TrimSpace(buildClientMachineSection(machine))
	if machineSection == "" {
		return "", fmt.Errorf("machine is missing SSH tunnel token; bootstrap the machine again")
	}

	cleaned := stripClientManagedSections(base)
	updated := strings.TrimRight(cleaned, "\n")

	sections := []string{machineSection}
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
