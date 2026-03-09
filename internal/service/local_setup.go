package service

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/db"
	sshpkg "github.com/smalex-z/gopher/internal/ssh"
)

func buildRatholeServiceUnit(binaryPath string) string {
	return fmt.Sprintf(`[Unit]
Description=Rathole Server Tunnel
After=network.target

[Service]
Type=simple
ExecStart=%s /etc/rathole/server.toml
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, binaryPath)
}

const localInitialRatholeServerConfig = `[server]
bind_addr = "0.0.0.0:2333"

[server.services.placeholder]
token = "changeme"
bind_addr = "0.0.0.0:52000"

# ===== BEGIN CUSTOM CONFIGURATION =====
# Everything below this line will NOT be overwritten on deploy.
# Add any custom rathole service entries here.
# ===== END CUSTOM CONFIGURATION =====
`

// LocalServiceStatus is returned by GET /api/local/status.
type LocalServiceStatus struct {
	CaddyInstalled       bool   `json:"caddy_installed"`
	CaddyActive          string `json:"caddy_active"`
	RatholeInstalled     bool   `json:"rathole_installed"`
	RatholeActive        string `json:"rathole_active"`
	Domain               string `json:"domain"`
	LocalSetupDone       bool   `json:"local_setup_done"`
	HasInstallPermission bool   `json:"has_install_permission"`
	SSHPublicKey         string `json:"ssh_public_key"`
}

type LocalSetupService struct {
	hub *LogHub
}

func NewLocalSetupService(hub *LogHub) *LocalSetupService {
	return &LocalSetupService{hub: hub}
}

func (s *LocalSetupService) Status() (*LocalServiceStatus, error) {
	settings, err := db.GetSettings()
	if err != nil {
		return nil, err
	}
	return &LocalServiceStatus{
		CaddyInstalled:       isCommandAvailable("caddy"),
		CaddyActive:          systemctlStatus("caddy"),
		RatholeInstalled:     isCommandAvailable("rathole"),
		RatholeActive:        systemctlStatus("rathole-server"),
		Domain:               settings.Domain,
		LocalSetupDone:       settings.LocalSetupDone,
		HasInstallPermission: hasInstallPermission(),
		SSHPublicKey:         settings.SSHPublicKey,
	}, nil
}

// GetSSHPrivateKey returns the server's SSH private key from DB.
func (s *LocalSetupService) GetSSHPrivateKey() (string, error) {
	settings, err := db.GetSettings()
	if err != nil {
		return "", err
	}
	return settings.SSHPrivateKey, nil
}

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

// removeCaddyBlock removes a site block starting with `host {` from a Caddyfile.
func removeCaddyBlock(content, host string) string {
	lines := strings.Split(content, "\n")
	result := make([]string, 0, len(lines))
	depth := 0
	skip := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !skip && strings.HasPrefix(trimmed, host) && strings.HasSuffix(trimmed, "{") {
			skip = true
			depth = 1
			continue
		}
		if skip {
			for _, ch := range line {
				if ch == '{' {
					depth++
				} else if ch == '}' {
					depth--
				}
			}
			if depth <= 0 {
				skip = false
			}
			continue
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

// hasInstallPermission returns true if the process can write to system
// directories — either because it is root (uid 0) or because passwordless
// sudo is available (sudo -n true succeeds).
func hasInstallPermission() bool {
	if os.Getuid() == 0 {
		return true
	}
	return exec.Command("sudo", "-n", "true").Run() == nil // #nosec G204
}

// Install runs the full local setup in a background goroutine, streaming logs
// to the shared deploy hub so the frontend WebSocket log viewer can follow along.
func (s *LocalSetupService) Install(domain string) {
	go func() {
		w := &hubWriter{hub: s.hub}
		if err := s.doInstall(domain, w); err != nil {
			fmt.Fprintf(w, "ERROR: %v\n", err)
		}
		// Sentinel tells the frontend the stream is done so it can close the modal.
		s.hub.Broadcast("\x00DONE")
	}()
}

func (s *LocalSetupService) Skip(domain string) error {
	settings, err := db.GetSettings()
	if err != nil {
		return err
	}
	settings.LocalSetupDone = true
	if domain != "" {
		settings.Domain = domain
	}
	return db.SaveSettings(settings)
}

func (s *LocalSetupService) doInstall(domain string, logWriter io.Writer) error {
	fmt.Fprintln(logWriter, "=== Installing Local Services ===")

	// Step 1: Caddy
	if !isCommandAvailable("caddy") {
		fmt.Fprintln(logWriter, "Step 1: Installing Caddy via apt...")
		if err := installLocalCaddy(logWriter); err != nil {
			return fmt.Errorf("failed to install Caddy: %w", err)
		}
	} else {
		fmt.Fprintln(logWriter, "Step 1: Caddy already installed ✓")
	}

	// Step 2: Rathole
	ratholeExePath := findCommandPath("rathole")
	if ratholeExePath == "" {
		fmt.Fprintln(logWriter, "Step 2: Downloading rathole binary...")
		if err := installLocalRathole(logWriter); err != nil {
			return fmt.Errorf("failed to install rathole: %w", err)
		}
		ratholeExePath = "/usr/local/bin/rathole"
	} else {
		fmt.Fprintf(logWriter, "Step 2: Rathole already installed at %s ✓\n", ratholeExePath)
	}

	// Step 3: Caddyfile — merge if existing, create fresh if not
	fmt.Fprintln(logWriter, "Step 3: Configuring /etc/caddy/Caddyfile...")
	if existingCaddy, readErr := os.ReadFile("/etc/caddy/Caddyfile"); readErr == nil {
		fmt.Fprintln(logWriter, "  Existing Caddyfile found, merging dashboard block...")
		merged := mergeCaddyfile(string(existingCaddy), domain)
		if err := writeLocalFile("/etc/caddy/Caddyfile", merged); err != nil {
			return fmt.Errorf("failed to write Caddyfile: %w", err)
		}
	} else {
		if err := writeLocalFile("/etc/caddy/Caddyfile", buildLocalCaddyfile(domain)); err != nil {
			return fmt.Errorf("failed to write Caddyfile: %w", err)
		}
	}

	// Step 4: Rathole server.toml — migrate existing if present, create fresh if not
	fmt.Fprintln(logWriter, "Step 4: Setting up /etc/rathole/server.toml...")
	if err := sudoMkdir("/etc/rathole"); err != nil {
		return err
	}
	if _, statErr := os.Stat("/etc/rathole/server.toml"); os.IsNotExist(statErr) {
		if existingConfig := findExistingRatholeConfig(logWriter); existingConfig != "" {
			fmt.Fprintln(logWriter, "  Found existing rathole config, migrating with custom section markers...")
			if err := writeLocalFile("/etc/rathole/server.toml", migrateRatholeConfig(existingConfig)); err != nil {
				return fmt.Errorf("failed to write rathole config: %w", err)
			}
		} else {
			if err := writeLocalFile("/etc/rathole/server.toml", localInitialRatholeServerConfig); err != nil {
				return fmt.Errorf("failed to write rathole config: %w", err)
			}
		}
	} else {
		fmt.Fprintln(logWriter, "  /etc/rathole/server.toml already exists, preserving")
	}

	// Step 5: Rathole-server systemd unit (always update to ensure ExecStart path is correct)
	fmt.Fprintf(logWriter, "Step 5: Writing /etc/systemd/system/rathole-server.service (ExecStart=%s)...\n", ratholeExePath)
	if err := writeLocalFile("/etc/systemd/system/rathole-server.service", buildRatholeServiceUnit(ratholeExePath)); err != nil {
		return fmt.Errorf("failed to write rathole service unit: %w", err)
	}

	// Step 6: Reload systemd
	fmt.Fprintln(logWriter, "Step 6: Reloading systemd daemon...")
	if err := runLocalCmd(logWriter, "sudo", "systemctl", "daemon-reload"); err != nil {
		return err
	}

	// Step 7: Enable + start Caddy
	fmt.Fprintln(logWriter, "Step 7: Enabling and starting caddy.service...")
	if err := runLocalCmd(logWriter, "sudo", "systemctl", "enable", "--now", "caddy"); err != nil {
		return err
	}

	// Step 8: Enable + start rathole-server
	fmt.Fprintln(logWriter, "Step 8: Enabling and starting rathole-server.service...")
	if err := runLocalCmd(logWriter, "sudo", "systemctl", "enable", "--now", "rathole-server"); err != nil {
		return err
	}

	// Step 9: Reload Caddy so new Caddyfile takes effect
	fmt.Fprintln(logWriter, "Step 9: Reloading Caddy config...")
	_ = runLocalCmd(logWriter, "sudo", "systemctl", "reload", "caddy")

	// Step 10: Persist settings
	settings, err := db.GetSettings()
	if err != nil {
		return err
	}
	settings.Domain = domain
	settings.LocalSetupDone = true
	if err := db.SaveSettings(settings); err != nil {
		return fmt.Errorf("failed to save settings: %w", err)
	}

	fmt.Fprintln(logWriter, "=== Local Setup Complete ===")
	fmt.Fprintf(logWriter, "Dashboard: https://router.%s\n", domain)
	return nil
}

func installLocalCaddy(logWriter io.Writer) error {
	steps := [][]string{
		{"apt-get", "update", "-y"},
		{"apt-get", "install", "-y", "debian-keyring", "debian-archive-keyring", "apt-transport-https", "curl"},
		{"bash", "-c", `curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg`},
		{"bash", "-c", `curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | tee /etc/apt/sources.list.d/caddy-stable.list`},
		{"apt-get", "update", "-y"},
		{"apt-get", "install", "-y", "caddy"},
	}
	for _, args := range steps {
		if err := runLocalCmd(logWriter, args[0], args[1:]...); err != nil {
			return err
		}
	}
	return nil
}

func installLocalRathole(logWriter io.Writer) error {
	const version = "v0.5.0"
	var archTag string
	switch runtime.GOARCH {
	case "arm64":
		archTag = "aarch64-unknown-linux-musl"
	default:
		archTag = "x86_64-unknown-linux-gnu"
	}
	url := fmt.Sprintf(
		"https://github.com/rathole-org/rathole/releases/download/%s/rathole-%s.zip",
		version, archTag,
	)
	steps := [][]string{
		{"curl", "-fsSL", url, "-o", "/tmp/rathole.zip"},
		{"unzip", "-q", "/tmp/rathole.zip", "-d", "/tmp/rathole-dl"},
		{"mv", "/tmp/rathole-dl/rathole", "/usr/local/bin/rathole"},
		{"chmod", "+x", "/usr/local/bin/rathole"},
	}
	for _, args := range steps {
		if err := runLocalCmd(logWriter, args[0], args[1:]...); err != nil {
			return err
		}
	}
	return nil
}

func buildLocalCaddyfile(domain string) string {
	return fmt.Sprintf(`{
    email admin@%s
}

router.%s {
    reverse_proxy localhost:8080
}

%s:8080 {
    redir https://router.%s{uri} permanent
}

# ===== BEGIN CUSTOM CONFIGURATION =====
# Everything below this line will NOT be overwritten on local setup.
# Add any custom Caddy directives or site blocks here.
# ===== END CUSTOM CONFIGURATION =====
`, domain, domain, domain, domain)
}

// writeLocalFile writes content to path. If the direct write fails due to
// permissions, it falls back to `sudo tee` so the app can write to system
// directories without running as root itself.
func writeLocalFile(path, content string) error {
	// Try direct write first (works when running as root or owning the dir).
	if err := os.MkdirAll(filepath.Dir(path), 0755); err == nil {
		if err2 := os.WriteFile(path, []byte(content), 0644); err2 == nil {
			return nil
		}
	}
	// Fall back to sudo tee (requires passwordless sudo or NOPASSWD in sudoers).
	cmd := exec.Command("sudo", "tee", path) // #nosec G204
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = io.Discard
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// sudoMkdir creates a directory using sudo when os.MkdirAll fails.
func sudoMkdir(path string) error {
	if err := os.MkdirAll(path, 0755); err == nil {
		return nil
	}
	return exec.Command("sudo", "mkdir", "-p", path).Run() // #nosec G204
}

// runLocalCmd executes a command, streaming stdout+stderr to logWriter.
// Args are all hardcoded constants — no user input reaches this function.
func runLocalCmd(logWriter io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...) // #nosec G204
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	return cmd.Run()
}

// findCommandPath returns the absolute path to a binary, checking both $PATH
// and a set of well-known directories that may be absent from a restricted PATH
// (e.g. when the app runs as a systemd service).
func findCommandPath(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range []string{"/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func isCommandAvailable(name string) bool {
	return findCommandPath(name) != ""
}

// findExistingRatholeConfig looks for a rathole server config in common locations.
func findExistingRatholeConfig(logWriter io.Writer) string {
	candidates := []string{
		"/etc/rathole.toml",
		"/etc/rathole/rathole.toml",
		"/home/rathole/server.toml",
		"/opt/rathole/server.toml",
	}
	for _, p := range candidates {
		if data, err := os.ReadFile(p); err == nil {
			fmt.Fprintf(logWriter, "  Found existing rathole config at %s\n", p)
			return string(data)
		}
	}
	return ""
}

// migrateRatholeConfig appends custom-section markers to an existing config if
// they are not already present.
func migrateRatholeConfig(existing string) string {
	const beginMarker = "# ===== BEGIN CUSTOM CONFIGURATION ====="
	if strings.Contains(existing, beginMarker) {
		return existing
	}
	return strings.TrimRight(existing, "\n") + "\n\n" +
		"# ===== BEGIN CUSTOM CONFIGURATION =====\n" +
		"# Everything below this line will NOT be overwritten on deploy.\n" +
		"# Add any custom rathole service entries here.\n" +
		"# ===== END CUSTOM CONFIGURATION =====\n"
}

// mergeCaddyfile builds a new Caddyfile that places Gopher's managed
// dashboard block above the custom section.
//
// If the file already has custom-section markers (a previous Gopher run):
//   - Content above BEGIN is Gopher's managed zone — update dashboard block there.
//   - Content between BEGIN/END is the user's zone — leave it untouched.
//
// If the file has NO markers yet (first time Gopher touches it):
//   - Treat ALL existing content as user config → wrap it in the custom section.
//   - Place Gopher's dashboard block ABOVE the custom section.
func mergeCaddyfile(existing, domain string) string {
	const beginMarker = "# ===== BEGIN CUSTOM CONFIGURATION ====="
	const endMarker = "# ===== END CUSTOM CONFIGURATION ====="
	dashboardBlock := fmt.Sprintf("router.%s {\n    reverse_proxy localhost:8080\n}\n\n%s:8080 {\n    redir https://router.%s{uri} permanent\n}\n", domain, domain, domain)

	if idx := strings.Index(existing, beginMarker); idx != -1 {
		// Markers already present: managed zone is everything before BEGIN.
		managedZone := strings.TrimSpace(existing[:idx])
		customSection := existing[idx:] // preserve from BEGIN to end verbatim
		if strings.Contains(managedZone, fmt.Sprintf("router.%s", domain)) {
			// Dashboard block already in managed zone — nothing to do.
			return existing
		}
		return managedZone + "\n\n" + dashboardBlock + "\n" + customSection
	}

	// No markers yet: move all existing content into the custom section.
	trimmed := strings.TrimRight(existing, "\n")
	return dashboardBlock + "\n" +
		beginMarker + "\n" +
		"# Everything below this line will NOT be overwritten.\n" +
		"# Add your own Caddy site blocks here.\n" +
		trimmed + "\n" +
		endMarker + "\n"
}

func systemctlStatus(service string) string {
	out, err := exec.Command("systemctl", "is-active", service).Output() // #nosec G204
	if err != nil {
		check, _ := exec.Command("systemctl", "status", service).CombinedOutput() // #nosec G204
		if strings.Contains(string(check), "could not be found") || strings.Contains(string(check), "not-found") {
			return "not-found"
		}
		s := strings.TrimSpace(string(out))
		if s == "" {
			return "inactive"
		}
		return s
	}
	return strings.TrimSpace(string(out))
}
