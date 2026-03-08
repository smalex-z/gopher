package service

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/smalex-z/gopher/internal/db"
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

[server.default_token]
default_token = "changeme"

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
	}, nil
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

func (s *LocalSetupService) Skip() error {
	settings, err := db.GetSettings()
	if err != nil {
		return err
	}
	settings.LocalSetupDone = true
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
	fmt.Fprintf(logWriter, "Dashboard: https://dashboard.%s\n", domain)
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
	var arch string
	if runtime.GOARCH == "arm64" {
		arch = "aarch64"
	} else {
		arch = "x86_64"
	}
	url := fmt.Sprintf(
		"https://github.com/rapiz1/rathole/releases/download/%s/rathole-%s-unknown-linux-musl.tar.gz",
		version, arch,
	)
	steps := [][]string{
		{"curl", "-Lo", "/tmp/rathole.tar.gz", url},
		{"tar", "-xzf", "/tmp/rathole.tar.gz", "-C", "/tmp"},
		{"mv", "/tmp/rathole", "/usr/local/bin/rathole"},
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

dashboard.%s {
    reverse_proxy localhost:8080
}

# ===== BEGIN CUSTOM CONFIGURATION =====
# Everything below this line will NOT be overwritten on local setup.
# Add any custom Caddy directives or site blocks here.
# ===== END CUSTOM CONFIGURATION =====
`, domain, domain)
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
	dashboardBlock := fmt.Sprintf("dashboard.%s {\n    reverse_proxy localhost:8080\n}\n", domain)

	if idx := strings.Index(existing, beginMarker); idx != -1 {
		// Markers already present: managed zone is everything before BEGIN.
		managedZone := strings.TrimSpace(existing[:idx])
		customSection := existing[idx:] // preserve from BEGIN to end verbatim
		if strings.Contains(managedZone, fmt.Sprintf("dashboard.%s", domain)) {
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
