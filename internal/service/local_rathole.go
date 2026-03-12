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
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", configPath, err)
	}

	content := string(existing)

	// Split the file into the header zone (above BEGIN) and the user's custom section.
	userBody := ""
	if bIdx := strings.Index(content, beginMarker); bIdx != -1 {
		above := content[:bIdx]
		below := content[bIdx+len(beginMarker):]
		if eIdx := strings.Index(below, endMarker); eIdx != -1 {
			userBody = below[:eIdx]
		} else {
			userBody = below
		}
		content = above
	}
	// Strip any gopher service entries that old versions incorrectly placed inside
	// the custom section, and normalise whitespace.
	userBody = strings.TrimSpace(stripGopherServiceSections(userBody))

	// Strip gopher-managed entries from the header zone and trim trailing whitespace.
	base := strings.TrimRight(stripGopherServiceSections(content), "\n") + "\n"

	// Rebuild gopher-managed entries from the database.
	machines, _ := db.GetMachines()
	tunnels, _ := db.GetTunnels()

	var managed strings.Builder
	for _, m := range machines {
		if m.RatholeSSHToken == "" || m.TunnelPort == 0 {
			continue
		}
		fmt.Fprintf(&managed, "\n[server.services.machine-%s-ssh]\ntoken = \"%s\"\nbind_addr = \"0.0.0.0:%d\"\n",
			m.ID, m.RatholeSSHToken, m.TunnelPort)
	}
	for _, t := range tunnels {
		if t.RatholePort == 0 {
			continue
		}
		token := t.RatholeToken
		if token == "" {
			token = t.ID // backward compat for tunnels created before this change
		}
		fmt.Fprintf(&managed, "\n[server.services.tunnel-%s]\ntoken = \"%s\"\nbind_addr = \"0.0.0.0:%d\"\n",
			t.ID, token, t.RatholePort)
	}

	// Rathole requires at least one [server.services.*] entry to start.
	// Use a harmless placeholder when both managed and user sections are empty.
	if managed.Len() == 0 && strings.TrimSpace(userBody) == "" {
		managed.WriteString("\n[server.services.placeholder]\ntoken = \"placeholder\"\nbind_addr = \"0.0.0.0:52000\"\n")
	}

	// Assemble: base config + gopher entries + custom section.
	customBlock := beginMarker + "\n"
	if userBody != "" {
		customBlock += userBody + "\n"
	}
	customBlock += endMarker + "\n"
	newContent := base + managed.String() + "\n" + customBlock

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

// stripGopherServiceSections removes all [server.services.machine-UUID-ssh] and
// [server.services.tunnel-UUID] blocks from a TOML string. Used to clean both
// the header zone and any legacy entries that were incorrectly placed inside the
// custom section by older versions.
func stripGopherServiceSections(content string) string {
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
	return strings.Join(out, "\n")
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
	token := tunnel.RatholeToken
	if token == "" {
		token = tunnel.ID // backward compat
	}
	clientEntry := fmt.Sprintf("\n[client.services.tunnel-%s]\ntype = \"tcp\"\ntoken = \"%s\"\nlocal_addr = \"localhost:%d\"\n",
		tunnel.ID, token, tunnel.LocalPort)
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

// RemoveMachineClient SSHes into a client machine via its reverse tunnel and
// removes all gopher-managed configuration: the rathole-client service,
// client.toml, and the VPS public key from ~/.ssh/authorized_keys.
//
// Errors are best-effort — callers should proceed with DB cleanup even on failure.
func (s *LocalSetupService) RemoveMachineClient(machine *db.Machine) error {
	settings, err := db.GetSettings()
	if err != nil || settings.SSHPrivateKey == "" {
		return fmt.Errorf("no server SSH key available")
	}
	if machine.TunnelPort == 0 {
		return fmt.Errorf("machine has no tunnel port")
	}

	sshClient, err := sshpkg.NewClient("localhost", machine.TunnelPort, machine.Username, settings.SSHPrivateKey)
	if err != nil {
		return fmt.Errorf("failed to SSH into machine via tunnel (port %d): %w", machine.TunnelPort, err)
	}
	defer sshClient.Close()

	// Stop and disable rathole-client (system service and user-level service).
	_, _ = sshClient.Execute(
		"pkill -x rathole 2>/dev/null; " +
			"sudo -n systemctl stop rathole-client 2>/dev/null; " +
			"sudo -n systemctl disable rathole-client 2>/dev/null; " +
			"systemctl --user stop rathole-client 2>/dev/null; " +
			"systemctl --user disable rathole-client 2>/dev/null; true")

	// Remove service unit files and trigger daemon reload.
	_, _ = sshClient.Execute(
		"sudo -n rm -f /etc/systemd/system/rathole-client.service 2>/dev/null; " +
			"rm -f ~/.config/systemd/user/rathole-client.service 2>/dev/null; " +
			"sudo -n systemctl daemon-reload 2>/dev/null; " +
			"systemctl --user daemon-reload 2>/dev/null; true")

	// Resolve the home directory (SFTP can't expand $HOME).
	homeDir, _ := sshClient.Execute("echo $HOME")
	homeDir = strings.TrimSpace(homeDir)
	// Sanitize: reject values that look like shell injection attempts.
	if !strings.HasPrefix(homeDir, "/") || strings.ContainsAny(homeDir, ";|&$`\\\"'") {
		homeDir = "/home/" + machine.Username
	}

	// Remove client.toml from all known locations.
	_, _ = sshClient.Execute(
		"sudo -n rm -f /etc/rathole/client.toml 2>/dev/null; " +
			"rm -f " + homeDir + "/.config/rathole/client.toml 2>/dev/null; true")

	// Remove the VPS public key from authorized_keys.
	if settings.SSHPublicKey != "" {
		akContent, readErr := sshClient.Execute("cat " + homeDir + "/.ssh/authorized_keys 2>/dev/null")
		if readErr == nil {
			filtered := removeSSHPublicKey(akContent, settings.SSHPublicKey)
			_ = sshClient.UploadFile([]byte(filtered), homeDir+"/.ssh/authorized_keys")
		}
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
