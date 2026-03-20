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
token = "placeholder"
bind_addr = "0.0.0.0:52000"

# ===== BEGIN CUSTOM CONFIGURATION =====
# Add your own rathole service entries here. Gopher will not modify this section.
# ===== END CUSTOM CONFIGURATION =====
`

// hasInstallPermission returns true if local setup can run with current
// privileges. Root always passes. For non-root users, full passwordless sudo
// is required so all privileged operations (apt-get, systemctl, writing to
// system paths) can be executed via sudo.
func hasInstallPermission() bool {
	if os.Getuid() == 0 {
		return true
	}

	if !isCommandAvailable("sudo") {
		return false
	}
	return exec.Command("sudo", "-n", "true").Run() == nil // #nosec G204
}

// privilegedCmdPrefix returns a "sudo" prefix slice when not running as root,
// or nil when already root, so it can be prepended to privileged command args.
func privilegedCmdPrefix() []string {
	if os.Getuid() == 0 {
		return nil
	}
	return []string{"sudo"}
}

// Install runs the full local setup in a background goroutine, streaming logs
// to the shared deploy hub so the frontend WebSocket log viewer can follow along.
func (s *LocalSetupService) Install(domain string, skipCaddy bool) {
	go func() {
		w := &hubWriter{hub: s.hub}
		if err := s.doInstall(domain, skipCaddy, w); err != nil {
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

func (s *LocalSetupService) doInstall(domain string, skipCaddy bool, logWriter io.Writer) error {
	fmt.Fprintln(logWriter, "=== Installing Local Services ===")

	// Step 1: Caddy (optional)
	if skipCaddy {
		fmt.Fprintln(logWriter, "Step 1: Skipping Caddy setup (reverse proxy disabled)")
	} else {
		if !isCommandAvailable("caddy") {
			fmt.Fprintln(logWriter, "Step 1: Installing Caddy via apt...")
			if err := installLocalCaddy(logWriter); err != nil {
				return fmt.Errorf("failed to install Caddy: %w", err)
			}
		} else {
			fmt.Fprintln(logWriter, "Step 1: Caddy already installed ✓")
		}
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

	// Step 3: Caddyfile — import-based layout + managed conf.d entries
	if skipCaddy {
		fmt.Fprintln(logWriter, "Step 3: Skipping Caddyfile configuration")
	} else {
		fmt.Fprintln(logWriter, "Step 3: Configuring import-based /etc/caddy/Caddyfile and /etc/caddy/conf.d...")
		existingCaddy := ""
		if data, readErr := os.ReadFile(caddyConfigPath); readErr == nil {
			existingCaddy = string(data)
		}
		managedCaddy := buildManagedCaddyfile(existingCaddy)
		managedHosts := []string{fmt.Sprintf("router.%s", domain)}
		if tunnels, tunErr := db.GetTunnels(); tunErr == nil {
			for _, tunnel := range tunnels {
				if tunnel.Subdomain != "" {
					managedHosts = append(managedHosts, fmt.Sprintf("%s.%s", tunnel.Subdomain, domain))
				}
			}
		}
		managedCaddy = removeHostsFromCustomSection(managedCaddy, managedHosts)

		if err := sudoMkdir(caddyManagedDir); err != nil {
			return fmt.Errorf("failed to create %s: %w", caddyManagedDir, err)
		}
		if err := writeLocalFile(caddyConfigPath, managedCaddy); err != nil {
			return fmt.Errorf("failed to write %s: %w", caddyConfigPath, err)
		}
		if err := writeLocalFile(managedRouterCaddyPath(), buildRouterCaddyBlock(domain)); err != nil {
			return fmt.Errorf("failed to write router Caddy file: %w", err)
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
	if skipCaddy {
		fmt.Fprintln(logWriter, "Step 7: Skipping caddy.service enable/start")
	} else {
		fmt.Fprintln(logWriter, "Step 7: Enabling and starting caddy.service...")
		if err := runLocalCmd(logWriter, "sudo", "systemctl", "enable", "--now", "caddy"); err != nil {
			return err
		}
	}

	// Step 8: Enable + start rathole-server
	fmt.Fprintln(logWriter, "Step 8: Enabling and starting rathole-server.service...")
	if err := runLocalCmd(logWriter, "sudo", "systemctl", "enable", "--now", "rathole-server"); err != nil {
		return err
	}

	// Step 9: Reload Caddy so new Caddyfile takes effect
	if skipCaddy {
		fmt.Fprintln(logWriter, "Step 9: Skipping Caddy reload")
	} else {
		fmt.Fprintln(logWriter, "Step 9: Reloading Caddy config...")
		_ = runLocalCmd(logWriter, "sudo", "systemctl", "reload", "caddy")
	}

	// Step 10: Persist settings
	settings, err := db.GetSettings()
	if err != nil {
		return err
	}
	if skipCaddy {
		settings.Domain = ""
	} else {
		settings.Domain = domain
	}
	settings.LocalSetupDone = true
	if err := db.SaveSettings(settings); err != nil {
		return fmt.Errorf("failed to save settings: %w", err)
	}

	fmt.Fprintln(logWriter, "=== Local Setup Complete ===")
	if skipCaddy {
		fmt.Fprintln(logWriter, "Dashboard: local mode (reverse proxy disabled)")
	} else {
		fmt.Fprintf(logWriter, "Dashboard: https://router.%s\n", domain)
	}
	return nil
}

func installLocalCaddy(logWriter io.Writer) error {
	sudo := privilegedCmdPrefix()
	steps := [][]string{
		append(sudo, "apt-get", "update", "-y"),
		append(sudo, "apt-get", "install", "-y", "debian-keyring", "debian-archive-keyring", "apt-transport-https", "curl"),
		append(sudo, "bash", "-c", `curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor --batch --yes --no-tty -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg`),
		append(sudo, "bash", "-c", `curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | tee /etc/apt/sources.list.d/caddy-stable.list`),
		append(sudo, "apt-get", "update", "-y"),
		append(sudo, "apt-get", "install", "-y", "caddy"),
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

	// Ensure unzip is available before attempting to extract.
	if !isCommandAvailable("unzip") {
		fmt.Fprintln(logWriter, "  unzip not found, installing via apt...")
		aptSudo := privilegedCmdPrefix()
		aptUpdate := append(aptSudo, "apt-get", "update", "-qq")
		aptInstall := append(aptSudo, "apt-get", "install", "-y", "-qq", "unzip")
		if err := runLocalCmd(logWriter, aptUpdate[0], aptUpdate[1:]...); err != nil {
			return fmt.Errorf("failed to run apt-get update: %w", err)
		}
		if err := runLocalCmd(logWriter, aptInstall[0], aptInstall[1:]...); err != nil {
			return fmt.Errorf("failed to install unzip: %w", err)
		}
	}

	sudo := privilegedCmdPrefix()
	steps := [][]string{
		{"curl", "-fsSL", url, "-o", "/tmp/rathole.zip"},
		{"unzip", "-q", "-o", "/tmp/rathole.zip", "-d", "/tmp/rathole-dl"},
		append(sudo, "mv", "/tmp/rathole-dl/rathole", "/usr/local/bin/rathole"),
		append(sudo, "chmod", "+x", "/usr/local/bin/rathole"),
		{"rm", "-rf", "/tmp/rathole.zip", "/tmp/rathole-dl"},
	}
	for _, args := range steps {
		if err := runLocalCmd(logWriter, args[0], args[1:]...); err != nil {
			return err
		}
	}
	return nil
}
