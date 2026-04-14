package service

import (
	"fmt"
	"os"
	"os/exec"
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
	managedConfig := config.GenerateRatholeServerConfig(machines, tunnels)

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

	// Send SIGHUP to reload config in-place — avoids dropping existing tunnel connections.
	// Falls back to restart only if rathole is not running.
	if exec.Command("sudo", "pkill", "-HUP", "-x", "rathole").Run() != nil { // #nosec G204
		_ = exec.Command("sudo", "systemctl", "restart", "rathole-server").Run() // #nosec G204
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
// /etc/rathole/server.toml and SSHes into the machine to update its
// /etc/rathole/client.toml. If subdomain is set it also writes a managed
// Caddy site file under /etc/caddy/conf.d.
func (s *LocalSetupService) AddServiceTunnel(tunnel *db.Tunnel, machine *db.Machine) error {
	settings, err := db.GetSettings()
	if err != nil {
		return err
	}

	// --- 1. Update server.toml (full reconcile ensures consistency) ---
	if err := s.ReconcileServerConfig(); err != nil {
		return fmt.Errorf("failed to update server.toml: %w", err)
	}

	// --- 2. Update managed Caddy entry if subdomain is set (TCP only; UDP/private have no HTTP routing) ---
	if tunnel.Subdomain != "" && settings.Domain != "" && tunnel.Transport != "udp" && !tunnel.Private {
		if err := ensureManagedCaddyLayout(); err != nil {
			return fmt.Errorf("failed to prepare Caddy managed layout: %w", err)
		}
		if err := writeLocalFile(managedRouterCaddyPath(), buildRouterCaddyBlock(settings.Domain)); err != nil {
			return fmt.Errorf("failed to write router Caddy file: %w", err)
		}
		managedPath := managedTunnelCaddyPath(tunnel.ID)
		block := buildTunnelCaddyBlock(tunnel.Subdomain, settings.Domain, tunnel.RatholePort, tunnel.NoTLS)
		if err := writeLocalFile(managedPath, block); err != nil {
			return fmt.Errorf("failed to write tunnel Caddy file %s: %w", managedPath, err)
		}
		_ = exec.Command("sudo", "systemctl", "reload", "caddy").Run() // #nosec G204
	}

	// --- 3. SSH into client and update client.toml ---
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
		sshClient, sshDialErr = sshpkg.NewClient("localhost", machine.TunnelPort, machine.Username, sshKey.PrivateKey)
		if sshDialErr == nil {
			break
		}
	}
	if sshDialErr != nil {
		return fmt.Errorf("failed to SSH into machine via tunnel (port %d) after retries: %w", machine.TunnelPort, sshDialErr)
	}
	defer sshClient.Close()

	// Read existing client.toml
	existing, err := sshClient.Execute("cat /etc/rathole/client.toml 2>/dev/null || cat ~/.config/rathole/client.toml 2>/dev/null")
	if err != nil {
		existing = ""
	}
	machineTunnels, err := db.GetTunnelsByMachine(machine.ID)
	if err != nil {
		return fmt.Errorf("failed to load machine tunnels: %w", err)
	}
	ratholeHost := settings.ServerHost
	if ratholeHost == "" {
		ratholeHost = settings.Domain
	}
	updated, err := mergeClientManagedConfig(existing, machine, machineTunnels, ratholeHost)
	if err != nil {
		return err
	}

	// Determine the absolute config path on the remote machine.
	// SFTP does not expand shell variables, so resolve the home dir explicitly.
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

	// Write config directly via SFTP — works when the SSH user owns /etc/rathole/
	// (bootstrap runs chown for sudo-capable machines). UploadFileSudo handles legacy
	// machines where the file ended up root-owned by falling back to a sudo mv.
	if err := sshClient.UploadFileSudo([]byte(updated), configPath, machine.Username); err != nil {
		return fmt.Errorf("failed to write client.toml on machine: %w", err)
	}

	// Restart rathole-client. Bootstrap now runs the service as the SSH user,
	// so pkill is sufficient (systemd Restart=always brings it back).
	// sudo -n is attempted as fallback for older installs with NOPASSWD configured.
	_, _ = sshClient.Execute("pkill -x rathole 2>/dev/null; sudo -n systemctl restart rathole-client 2>/dev/null; systemctl --user restart rathole-client 2>/dev/null; true")

	return nil
}

// RemoveServiceTunnelClient removes only the tunnel's section from the
// client machine's /etc/rathole/client.toml (or user-level fallback).
func (s *LocalSetupService) RemoveServiceTunnelClient(tunnel *db.Tunnel, machine *db.Machine) error {
	if tunnel == nil || machine == nil {
		return nil
	}
	sshKey, err := db.GetSSHKeyForMachine(machine)
	if err != nil {
		return nil
	}
	sshClient, err := sshpkg.NewClient("localhost", machine.TunnelPort, machine.Username, sshKey.PrivateKey)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	existing, err := sshClient.Execute("cat /etc/rathole/client.toml 2>/dev/null || cat ~/.config/rathole/client.toml 2>/dev/null")
	if err != nil {
		return nil
	}
	updated := removeClientManagedSection(existing, "tunnel", tunnel.ID)

	// Resolve absolute config path (SFTP cannot expand $HOME).
	confPath := "/etc/rathole/client.toml"
	if _, err2 := sshClient.Execute("test -f /etc/rathole/client.toml"); err2 != nil {
		homeDir, _ := sshClient.Execute("echo $HOME")
		homeDir = strings.TrimSpace(homeDir)
		if homeDir == "" {
			homeDir = "/home/" + machine.Username
		}
		confPath = homeDir + "/.config/rathole/client.toml"
	}

	if err := sshClient.UploadFileSudo([]byte(updated), confPath, machine.Username); err != nil {
		return err
	}
	_, _ = sshClient.Execute("pkill -x rathole 2>/dev/null; sudo -n systemctl restart rathole-client 2>/dev/null; systemctl --user restart rathole-client 2>/dev/null; true")
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
		_ = exec.Command("sudo", "rm", "-f", managedPath).Run() // #nosec G204
	}
	_ = exec.Command("sudo", "systemctl", "reload", "caddy").Run() // #nosec G204
	return nil
}

// RemoveServiceTunnel keeps backwards compatibility with older callers by
// performing full tunnel cleanup in the canonical order.
func (s *LocalSetupService) RemoveServiceTunnel(tunnel *db.Tunnel, machine *db.Machine) {
	_ = s.RemoveServiceTunnelClient(tunnel, machine)
	_ = s.ReconcileServerConfig()
	_ = s.RemoveServiceTunnelCaddy(tunnel)
}

// RemoveMachineClient SSHes into a client machine via its reverse tunnel and
// removes all gopher-managed configuration: the rathole-client service,
// client.toml, and the VPS public key from ~/.ssh/authorized_keys.
//
// Errors are best-effort — callers should proceed with DB cleanup even on failure.
func (s *LocalSetupService) RemoveMachineClient(machine *db.Machine) error {
	sshKey, err := db.GetSSHKeyForMachine(machine)
	if err != nil {
		return fmt.Errorf("no server SSH key available")
	}
	if machine.TunnelPort == 0 {
		return fmt.Errorf("machine has no tunnel port")
	}

	sshClient, err := sshpkg.NewClient("localhost", machine.TunnelPort, machine.Username, sshKey.PrivateKey)
	if err != nil {
		return fmt.Errorf("failed to SSH into machine via tunnel (port %d): %w", machine.TunnelPort, err)
	}
	defer sshClient.Close()

	// Resolve the home directory (SFTP can't expand $HOME).
	homeDir, _ := sshClient.Execute("echo $HOME")
	homeDir = strings.TrimSpace(homeDir)
	// Sanitize: reject values that look like shell injection attempts.
	if !strings.HasPrefix(homeDir, "/") || strings.ContainsAny(homeDir, ";|&$`\\\"'") {
		homeDir = "/home/" + machine.Username
	}

	// Remove the VPS public key from authorized_keys.
	if sshKey.PublicKey != "" {
		akContent, readErr := sshClient.Execute("cat " + homeDir + "/.ssh/authorized_keys 2>/dev/null")
		if readErr == nil {
			filtered := removeSSHPublicKey(akContent, sshKey.PublicKey)
			_ = sshClient.UploadFile([]byte(filtered), homeDir+"/.ssh/authorized_keys")
		}
	}

	// Run uninstall asynchronously on the client so it can complete even after
	// rathole is stopped and this SSH-over-tunnel session drops.
	const remoteScriptPath = "/tmp/.gopher-remove-rathole.sh"
	script := buildMachineClientCleanupScript(homeDir, remoteScriptPath)
	if err := sshClient.UploadFile([]byte(script), remoteScriptPath); err != nil {
		return fmt.Errorf("failed to upload remote cleanup script: %w", err)
	}
	_, _ = sshClient.Execute("chmod +x " + remoteScriptPath)
	_, err = sshClient.Execute("nohup sh " + remoteScriptPath + " >/tmp/.gopher-remove-rathole.log 2>&1 < /dev/null &")
	if err != nil {
		return fmt.Errorf("failed to start remote cleanup script: %w", err)
	}

	return nil
}

func buildMachineClientCleanupScript(homeDir, scriptPath string) string {
	return fmt.Sprintf(`#!/bin/sh
set -e
HOME_DIR=%q

sudo -n systemctl stop rathole-client 2>/dev/null || true
sudo -n systemctl disable rathole-client 2>/dev/null || true
systemctl --user stop rathole-client 2>/dev/null || true
systemctl --user disable rathole-client 2>/dev/null || true

sudo -n rm -f /etc/systemd/system/rathole-client.service 2>/dev/null || true
rm -f "$HOME_DIR/.config/systemd/user/rathole-client.service" 2>/dev/null || true
sudo -n systemctl daemon-reload 2>/dev/null || true
systemctl --user daemon-reload 2>/dev/null || true

sudo -n rm -f /etc/rathole/client.toml 2>/dev/null || true
rm -f "$HOME_DIR/.config/rathole/client.toml" 2>/dev/null || true

sudo -n rm -f /usr/local/bin/rathole 2>/dev/null || true
rm -f "$HOME_DIR/.local/bin/rathole" 2>/dev/null || true

rm -f %q 2>/dev/null || true
`, homeDir, scriptPath)
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
