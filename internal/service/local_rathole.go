package service

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/db"
	sshpkg "github.com/smalex-z/gopher/internal/ssh"
)

// AddMachineSSHTunnel appends a new [server.services.*-ssh] entry to
// /etc/rathole/server.toml inside the custom section, then reloads the service.
func (s *LocalSetupService) AddMachineSSHTunnel(machine *db.Machine) error {
	return s.ReconcileServerConfig()
}

// ReconcileServerConfig rebuilds the custom section of /etc/rathole/server.toml
// from the database. This fixes desync between the DB and the config file
// (e.g. after manual edits, restarts, or stale entries from deleted records).
func (s *LocalSetupService) ReconcileServerConfig() error {
	const configPath = "/etc/rathole/server.toml"
	const beginMarker = "# ===== BEGIN CUSTOM CONFIGURATION ====="
	const endMarker = "# ===== END CUSTOM CONFIGURATION ====="

	existing, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", configPath, err)
	}

	// Strip all gopher-managed entries from the ENTIRE file (line-by-line parse).
	// This handles entries that leaked above the markers from older append logic.
	header := stripGopherServerEntries(string(existing), beginMarker, endMarker)

	// Rebuild custom entries from DB
	machines, _ := db.GetMachines()
	tunnels, _ := db.GetTunnels()

	var entries strings.Builder
	for _, m := range machines {
		if m.RatholeSSHToken == "" || m.TunnelPort == 0 {
			continue
		}
		fmt.Fprintf(&entries, "\n[server.services.machine-%s-ssh]\ntoken = \"%s\"\nbind_addr = \"0.0.0.0:%d\"\n",
			m.ID, m.RatholeSSHToken, m.TunnelPort)
	}
	for _, t := range tunnels {
		if t.RatholePort == 0 {
			continue
		}
		fmt.Fprintf(&entries, "\n[server.services.tunnel-%s]\ntoken = \"%s\"\nbind_addr = \"0.0.0.0:%d\"\n",
			t.ID, t.ID, t.RatholePort)
	}

	newContent := header + beginMarker + "\n" + entries.String() + "\n" + endMarker + "\n"
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

// stripGopherServerEntries removes all [server.services.machine-UUID-ssh] and
// [server.services.tunnel-UUID] sections from the file content, then strips
// everything from beginMarker onward (so the caller can append a fresh section).
func stripGopherServerEntries(content, beginMarker, endMarker string) string {
	// UUID pattern: 8-4-4-4-12 hex chars
	isGopherSection := func(line string) bool {
		if len(line) < 10 || line[0] != '[' {
			return false
		}
		const pfxM = "[server.services.machine-"
		const pfxT = "[server.services.tunnel-"
		if strings.HasPrefix(line, pfxM) && strings.HasSuffix(line, "-ssh]") {
			return isUUID(line[len(pfxM) : len(line)-len("-ssh]")])
		}
		if strings.HasPrefix(line, pfxT) && strings.HasSuffix(line, "]") {
			return isUUID(line[len(pfxT) : len(line)-1])
		}
		return false
	}

	var out []string
	skip := false
	for _, line := range strings.Split(content, "\n") {
		stripped := strings.TrimSpace(line)
		if stripped == beginMarker || stripped == endMarker {
			skip = false
			// Drop the old marker lines; caller will append new ones
			continue
		}
		if isGopherSection(stripped) {
			skip = true
			continue
		}
		if skip && strings.HasPrefix(stripped, "[") {
			skip = false
		}
		if !skip {
			out = append(out, line)
		}
	}
	// Trim trailing blank lines and return header ready for appending
	header := strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n\n"
	return header
}

// isUUID reports whether s is a standard 8-4-4-4-12 hex UUID.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

// AddServiceTunnel adds a user-defined service tunnel to the server's
// /etc/rathole/server.toml and SSHes into the machine to update its
// /etc/rathole/client.toml. If subdomain is set it also adds a Caddy block.
func (s *LocalSetupService) AddServiceTunnel(tunnel *db.Tunnel, machine *db.Machine) error {
	settings, err := db.GetSettings()
	if err != nil {
		return err
	}

	// --- 1. Update server.toml (full reconcile ensures consistency) ---
	if err := s.ReconcileServerConfig(); err != nil {
		return fmt.Errorf("failed to update server.toml: %w", err)
	}

	// --- 2. Update Caddyfile if subdomain is set ---
	if tunnel.Subdomain != "" && settings.Domain != "" {
		const caddyFile = "/etc/caddy/Caddyfile"
		caddyContent, err := os.ReadFile(caddyFile)
		if err != nil {
			return fmt.Errorf("failed to read Caddyfile: %w", err)
		}
		caddyBlock := fmt.Sprintf("\n%s.%s {\n    reverse_proxy localhost:%d\n}\n",
			tunnel.Subdomain, settings.Domain, tunnel.RatholePort)
		cc := string(caddyContent)
		const caddyEnd = "# ===== END CUSTOM CONFIGURATION ====="
		if idx := strings.Index(cc, caddyEnd); idx != -1 {
			cc = cc[:idx] + caddyBlock + "\n" + cc[idx:]
		} else {
			cc += caddyBlock
		}
		if err := writeLocalFile(caddyFile, cc); err != nil {
			return fmt.Errorf("failed to write Caddyfile: %w", err)
		}
		_ = exec.Command("sudo", "systemctl", "reload", "caddy").Run() // #nosec G204
	}

	// --- 3. SSH into client and update client.toml ---
	if settings.SSHPrivateKey == "" {
		return fmt.Errorf("no server SSH key available; machine may need to be re-bootstrapped")
	}
	var sshClient *sshpkg.SSHClient
	var sshDialErr error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			time.Sleep(5 * time.Second)
		}
		sshClient, sshDialErr = sshpkg.NewClient("localhost", machine.TunnelPort, machine.Username, settings.SSHPrivateKey)
		if sshDialErr == nil {
			break
		}
	}
	if sshDialErr != nil {
		return fmt.Errorf("failed to SSH into machine via tunnel (port %d) after retries: %w", machine.TunnelPort, sshDialErr)
	}
	defer sshClient.Close()

	// Read existing client.toml
	ratholeHost := settings.Domain
	if ratholeHost == "" {
		return fmt.Errorf("domain not configured; set the domain in Setup before adding tunnels")
	}
	clientEntry := fmt.Sprintf("\n[client.services.tunnel-%s]\ntype = \"tcp\"\ntoken = \"%s\"\nlocal_addr = \"localhost:%d\"\n",
		tunnel.ID, tunnel.ID, tunnel.LocalPort)
	existing, err := sshClient.Execute("cat /etc/rathole/client.toml 2>/dev/null || cat ~/.config/rathole/client.toml 2>/dev/null")
	if err != nil || strings.TrimSpace(existing) == "" {
		// Build from scratch
		existing = fmt.Sprintf("[client]\nremote_addr = \"%s:2333\"\n", ratholeHost)
	}
	updated := strings.TrimRight(existing, "\n") + clientEntry

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

	// Write config directly via SFTP — no sudo required.
	// Bootstrap sets the user as owner of /etc/rathole/ so this works.
	// For machines bootstrapped before this fix, run on client:
	//   sudo chown <user> /etc/rathole /etc/rathole/client.toml
	if err := sshClient.UploadFile([]byte(updated), configPath); err != nil {
		return fmt.Errorf("failed to write client.toml on machine: %w\n\nFix: on the client machine run: sudo chown %s /etc/rathole /etc/rathole/client.toml", err, machine.Username)
	}

	// Restart rathole-client. Bootstrap now runs the service as the SSH user,
	// so pkill is sufficient (systemd Restart=always brings it back).
	// sudo -n is attempted as fallback for older installs with NOPASSWD configured.
	_, _ = sshClient.Execute("pkill -x rathole 2>/dev/null; sudo -n systemctl restart rathole-client 2>/dev/null; systemctl --user restart rathole-client 2>/dev/null; true")

	return nil
}

// RemoveServiceTunnel removes a tunnel's entries from server.toml, Caddyfile,
// and the client machine's client.toml.
func (s *LocalSetupService) RemoveServiceTunnel(tunnel *db.Tunnel, machine *db.Machine) {
	settings, _ := db.GetSettings()

	// Remove from server.toml — use full reconcile so stale entries are also cleaned up
	_ = s.ReconcileServerConfig()

	// Remove from Caddyfile
	if tunnel.Subdomain != "" && settings != nil && settings.Domain != "" {
		const caddyFile = "/etc/caddy/Caddyfile"
		if cc, err := os.ReadFile(caddyFile); err == nil {
			block := fmt.Sprintf("%s.%s", tunnel.Subdomain, settings.Domain)
			updated := removeCaddyBlock(string(cc), block)
			_ = writeLocalFile(caddyFile, updated)
			_ = exec.Command("sudo", "systemctl", "reload", "caddy").Run() // #nosec G204
		}
	}

	// Remove from client's client.toml
	if settings != nil && settings.SSHPrivateKey != "" && machine != nil {
		if sshClient, err := sshpkg.NewClient("localhost", machine.TunnelPort, machine.Username, settings.SSHPrivateKey); err == nil {
			defer sshClient.Close()
			if existing, err := sshClient.Execute("cat /etc/rathole/client.toml 2>/dev/null || cat ~/.config/rathole/client.toml 2>/dev/null"); err == nil {
				updated := removeTomlSection(existing, fmt.Sprintf("client.services.tunnel-%s", tunnel.ID))
				// Resolve absolute config path (SFTP can't expand $HOME)
				confPath := "/etc/rathole/client.toml"
				if _, err2 := sshClient.Execute("test -f /etc/rathole/client.toml"); err2 != nil {
					homeDir, _ := sshClient.Execute("echo $HOME")
					homeDir = strings.TrimSpace(homeDir)
					if homeDir == "" {
						homeDir = "/home/" + machine.Username
					}
					confPath = homeDir + "/.config/rathole/client.toml"
				}
				_ = sshClient.UploadFile([]byte(updated), confPath)
				_, _ = sshClient.Execute("pkill -x rathole 2>/dev/null; sudo -n systemctl restart rathole-client 2>/dev/null; systemctl --user restart rathole-client 2>/dev/null; true")
			}
		}
	}
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
