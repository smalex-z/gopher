package service

import (
	"fmt"
	"log"
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
// /etc/rathole/client.toml (owned by the SSH user). If subdomain is set it also writes a managed
// Caddy site file under /etc/caddy/conf.d.
func (s *LocalSetupService) AddServiceTunnel(tunnel *db.Tunnel, machine *db.Machine) error {
	settings, err := db.GetSettings()
	if err != nil {
		return err
	}

	// --- 1. Update server.toml (full reconcile ensures consistency) ---
	log.Printf("AddServiceTunnel: reconciling server.toml for tunnel %s", tunnel.ID)
	if err := s.ReconcileServerConfig(); err != nil {
		return fmt.Errorf("failed to update server.toml: %w", err)
	}
	log.Printf("AddServiceTunnel: server.toml updated")

	// --- 2. Update managed Caddy entry if subdomain is set ---
	if tunnel.Subdomain != "" && settings.Domain != "" {
		log.Printf("AddServiceTunnel: writing Caddy entry for subdomain %s.%s", tunnel.Subdomain, settings.Domain)
		if err := ensureManagedCaddyLayout(); err != nil {
			return fmt.Errorf("failed to prepare Caddy managed layout: %w", err)
		}
		if err := writeLocalFile(managedRouterCaddyPath(), buildRouterCaddyBlock(settings.Domain)); err != nil {
			return fmt.Errorf("failed to write router Caddy file: %w", err)
		}
		managedPath := managedTunnelCaddyPath(tunnel.ID)
		block := buildTunnelCaddyBlock(tunnel.Subdomain, settings.Domain, tunnel.RatholePort)
		if err := writeLocalFile(managedPath, block); err != nil {
			return fmt.Errorf("failed to write tunnel Caddy file %s: %w", managedPath, err)
		}
		_ = exec.Command("sudo", "systemctl", "reload", "caddy").Run() // #nosec G204
		log.Printf("AddServiceTunnel: Caddy entry written and reloaded")
	}

	// --- 3. SSH into client and update client.toml ---
	if settings.SSHPrivateKey == "" {
		return fmt.Errorf("no server SSH key available; machine may need to be re-bootstrapped")
	}
	log.Printf("AddServiceTunnel: dialing machine %s via SSH tunnel on port %d (user: %s)", machine.ID, machine.TunnelPort, machine.Username)
	var sshClient *sshpkg.SSHClient
	var sshDialErr error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			log.Printf("AddServiceTunnel: SSH dial attempt %d/6 failed (%v), retrying in 5s...", attempt, sshDialErr)
			time.Sleep(5 * time.Second)
		}
		sshClient, sshDialErr = sshpkg.NewClient("localhost", machine.TunnelPort, machine.Username, settings.SSHPrivateKey)
		if sshDialErr == nil {
			break
		}
	}
	if sshDialErr != nil {
		log.Printf("AddServiceTunnel: all SSH dial attempts failed for machine %s (port %d): %v", machine.ID, machine.TunnelPort, sshDialErr)
		return fmt.Errorf("failed to SSH into machine via tunnel (port %d) after retries: %w", machine.TunnelPort, sshDialErr)
	}
	log.Printf("AddServiceTunnel: SSH connection established to machine %s", machine.ID)
	defer sshClient.Close()

	configPath := "/etc/rathole/client.toml"

	// Read existing client.toml — the file is owned by the SSH user after bootstrap,
	// so no sudo is required.
	log.Printf("AddServiceTunnel: reading existing %s from machine %s", configPath, machine.ID)
	existing, err := sshClient.Execute("cat " + configPath + " 2>/dev/null")
	if err != nil {
		log.Printf("AddServiceTunnel: could not read %s (assuming empty): %v", configPath, err)
		existing = ""
	}
	machineTunnels, err := db.GetTunnelsByMachine(machine.ID)
	if err != nil {
		return fmt.Errorf("failed to load machine tunnels: %w", err)
	}
	updated, err := mergeClientManagedConfig(existing, machine, machineTunnels, settings.Domain)
	if err != nil {
		return err
	}

	log.Printf("AddServiceTunnel: uploading new client.toml to machine %s (%d tunnels)", machine.ID, len(machineTunnels))
	if err := sshClient.UploadFile([]byte(updated), "/tmp/rathole-client.toml"); err != nil {
		log.Printf("AddServiceTunnel: upload failed: %v", err)
		return fmt.Errorf("failed to write rathole config: %w", err)
	}
	// cp overwrites the existing file in-place; the file is owned by the SSH user so
	// no sudo is needed. mv would require write permission on the /etc/rathole dir
	// (owned by root), so we use cp instead.
	if out, err := sshClient.Execute("cp /tmp/rathole-client.toml " + configPath); err != nil {
		log.Printf("AddServiceTunnel: cp failed (out=%q): %v", out, err)
		return fmt.Errorf("failed to install rathole config: %w", err)
	}

	// systemctl restart requires sudo; bootstrap adds a NOPASSWD sudoers rule for
	// this command. Fall back silently for machines bootstrapped before that rule
	// was added — the service will pick up the new config on next restart.
	if out, err := sshClient.Execute("sudo systemctl restart rathole-client 2>/dev/null || true"); err != nil {
		log.Printf("AddServiceTunnel: rathole-client restart failed (out=%q): %v", out, err)
	} else {
		log.Printf("AddServiceTunnel: rathole-client restarted on machine %s", machine.ID)
	}

	log.Printf("AddServiceTunnel: client.toml successfully updated on machine %s", machine.ID)
	return nil
}

// RemoveServiceTunnelClient removes only the tunnel's section from the
// client machine's /etc/rathole/client.toml by calling the installed
// gopher-uninstall script on the client.
func (s *LocalSetupService) RemoveServiceTunnelClient(tunnel *db.Tunnel, machine *db.Machine) error {
	if tunnel == nil || machine == nil {
		return nil
	}
	settings, err := db.GetSettings()
	if err != nil || settings.SSHPrivateKey == "" {
		log.Printf("RemoveServiceTunnelClient: no SSH key, skipping client update for tunnel %s", tunnel.ID)
		return nil
	}

	log.Printf("RemoveServiceTunnelClient: SSHing into machine %s (port %d) to remove tunnel %s", machine.ID, machine.TunnelPort, tunnel.ID)
	sshClient, err := sshpkg.NewClient("localhost", machine.TunnelPort, machine.Username, settings.SSHPrivateKey)
	if err != nil {
		log.Printf("RemoveServiceTunnelClient: SSH failed for machine %s (port %d): %v", machine.ID, machine.TunnelPort, err)
		return err
	}
	defer sshClient.Close()

	out, err := sshClient.Execute("sudo /usr/local/bin/gopher-uninstall --remove-tunnel " + tunnel.ID + " 2>/dev/null || true")
	if err != nil {
		log.Printf("RemoveServiceTunnelClient: gopher-uninstall failed for tunnel %s (out=%q): %v", tunnel.ID, out, err)
	} else {
		log.Printf("RemoveServiceTunnelClient: tunnel %s removed from client (out=%q)", tunnel.ID, out)
	}
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
// calls the installed gopher-uninstall script to remove all gopher-managed
// configuration: rathole service, client.toml, rathole binary, and the VPS
// SSH key from authorized_keys.
//
// The script is launched asynchronously via nohup so it can complete even
// after the SSH tunnel drops when rathole is stopped.
//
// Errors are best-effort — callers should proceed with DB cleanup even on failure.
func (s *LocalSetupService) RemoveMachineClient(machine *db.Machine) error {
	settings, err := db.GetSettings()
	if err != nil || settings.SSHPrivateKey == "" {
		log.Printf("RemoveMachineClient: no SSH key available for machine %s", machine.ID)
		return fmt.Errorf("no server SSH key available")
	}
	if machine.TunnelPort == 0 {
		log.Printf("RemoveMachineClient: machine %s has no tunnel port, skipping uninstall", machine.ID)
		return fmt.Errorf("machine has no tunnel port")
	}

	log.Printf("RemoveMachineClient: SSHing into machine %s (port %d, user %s)", machine.ID, machine.TunnelPort, machine.Username)
	sshClient, err := sshpkg.NewClient("localhost", machine.TunnelPort, machine.Username, settings.SSHPrivateKey)
	if err != nil {
		log.Printf("RemoveMachineClient: SSH failed for machine %s (port %d): %v", machine.ID, machine.TunnelPort, err)
		return fmt.Errorf("failed to SSH into machine via tunnel (port %d): %w", machine.TunnelPort, err)
	}
	defer sshClient.Close()

	log.Printf("RemoveMachineClient: launching gopher-uninstall on machine %s", machine.ID)
	// Launch the uninstall script in the background so it survives the tunnel
	// dropping when rathole is stopped mid-cleanup.
	_, err = sshClient.Execute("nohup sudo /usr/local/bin/gopher-uninstall >/tmp/.gopher-uninstall.log 2>&1 < /dev/null &")
	if err != nil {
		log.Printf("RemoveMachineClient: failed to launch gopher-uninstall on machine %s: %v", machine.ID, err)
		return fmt.Errorf("failed to start gopher-uninstall on remote machine: %w", err)
	}
	log.Printf("RemoveMachineClient: gopher-uninstall launched on machine %s", machine.ID)
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
	return fmt.Sprintf(`# gopher-tunnel-start: %s
[client.services.tunnel-%s]
type = "tcp"
token = "%s"
local_addr = "localhost:%d"
# gopher-tunnel-end: %s
`, tunnel.ID, tunnel.ID, token, tunnel.LocalPort, tunnel.ID)
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
