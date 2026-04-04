package service

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/smalex-z/gopher/internal/db"
)

// pkgManager detects the system package manager. Returns "apt", "dnf", or "yum".
// Defaults to "apt" if none of the others are found.
func pkgManager() string {
	if isCommandAvailable("dnf") {
		return "dnf"
	}
	if isCommandAvailable("yum") {
		return "yum"
	}
	return "apt"
}

//go:embed templates/rathole-server.service
var ratholeServerServiceTemplate string

//go:embed templates/rathole-server-initial.toml
var ratholeServerInitialConfig string

func buildRatholeServiceUnit(binaryPath string) string {
	unit := ratholeServerServiceTemplate
	unit = strings.ReplaceAll(unit, "{{.BinaryPath}}", binaryPath)
	return unit
}

// hasInstallPermission returns true if local setup can run with current
// privileges. Root always passes. For non-root users, sudo must be available
// (password prompt is acceptable during install).
func hasInstallPermission() bool {
	if os.Getuid() == 0 {
		return true
	}
	// Sudo doesn't need to be passwordless for install — elevation will prompt
	return isCommandAvailable("sudo")
}

// privilegedCmdPrefix returns a "sudo -n" prefix slice when not running as root,
// or nil when already root. The -n flag enforces non-interactive mode so sudo
// won't prompt for passwords and will use the passwordless sudo entry instead.
func privilegedCmdPrefix() []string {
	if os.Getuid() == 0 {
		return nil
	}
	return []string{"sudo", "-n"}
}

// Install runs the full local setup in a background goroutine, streaming logs
// to the shared deploy hub so the frontend WebSocket log viewer can follow along.
func (s *LocalSetupService) Install(domain string, skipCaddy bool) {
	go func() {
		w := &hubWriter{hub: s.hub}
		if err := s.doInstall(domain, skipCaddy, w); err != nil {
			fmt.Fprintf(w, "ERROR: %v\n", err)
			s.hub.Broadcast("\x00ERROR")
			return
		}
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
			fmt.Fprintf(logWriter, "Step 1: Installing Caddy via %s...\n", pkgManager())
			if err := installLocalCaddy(logWriter); err != nil {
				return fmt.Errorf("failed to install Caddy: %w", err)
			}
		} else {
			fmt.Fprintln(logWriter, "Step 1: Caddy already installed ✓")
		}
	}

	// Step 2: Rathole — always ensure fresh binary and config
	fmt.Fprintln(logWriter, "Step 2: Ensuring rathole binary...")
	if err := installLocalRathole(logWriter); err != nil {
		return fmt.Errorf("failed to install rathole: %w", err)
	}
	ratholeExePath := findCommandPath("rathole")
	if ratholeExePath == "" {
		return fmt.Errorf("rathole binary not found after installation")
	}
	fmt.Fprintf(logWriter, "  Rathole ready at %s ✓\n", ratholeExePath)

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
			if err := writeLocalFile("/etc/rathole/server.toml", ratholeServerInitialConfig); err != nil {
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
		settings.DashboardPrivate = false // no Caddy — must keep :8080 public
	} else {
		settings.Domain = domain
		settings.DashboardPrivate = true // Caddy is set up; restrict :8080, use router.domain
	}
	settings.LocalSetupDone = true
	if err := db.SaveSettings(settings); err != nil {
		return fmt.Errorf("failed to save settings: %w", err)
	}
	// Apply 8080 visibility to firewall now that settings are persisted.
	ApplyDashboardPort(settings.DashboardPrivate)

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
	switch pkgManager() {
	case "dnf", "yum":
		pm := pkgManager()
		steps := [][]string{
			append(sudo, "bash", "-c", `curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor --batch --yes --no-tty -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg`),
			append(sudo, "bash", "-c", `curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/config.rpm.txt' | tee /etc/yum.repos.d/caddy-stable.repo`),
			append(sudo, pm, "install", "-y", "caddy"),
		}
		for _, args := range steps {
			if err := runLocalCmd(logWriter, args[0], args[1:]...); err != nil {
				return err
			}
		}
	default: // apt
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
		pm := pkgManager()
		fmt.Fprintf(logWriter, "  unzip not found, installing via %s...\n", pm)
		pkgSudo := privilegedCmdPrefix()
		var installCmd []string
		switch pm {
		case "dnf", "yum":
			installCmd = append(pkgSudo, pm, "install", "-y", "-q", "unzip")
		default: // apt
			update := append(pkgSudo, "apt-get", "update", "-qq")
			if err := runLocalCmd(logWriter, update[0], update[1:]...); err != nil {
				return fmt.Errorf("failed to run apt-get update: %w", err)
			}
			installCmd = append(pkgSudo, "apt-get", "install", "-y", "-qq", "unzip")
		}
		if err := runLocalCmd(logWriter, installCmd[0], installCmd[1:]...); err != nil {
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
