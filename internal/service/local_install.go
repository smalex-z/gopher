package service

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"

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

[server.services.placeholder]
token = "changeme"
bind_addr = "0.0.0.0:52000"

# ===== BEGIN CUSTOM CONFIGURATION =====
# Everything below this line will NOT be overwritten on deploy.
# Add any custom rathole service entries here.
# ===== END CUSTOM CONFIGURATION =====
`

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
