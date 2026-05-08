package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	defaultInstallUser = "gopher"
	defaultInstallDir  = "/opt/gopher"
	defaultDataDir     = "/var/lib/gopher"
	defaultServiceName = "gopher"

	// defaultJumpboxUser is a separate, deliberately privilege-free system
	// user whose ~/.ssh/authorized_keys holds Gopher-managed keys. The
	// dashboard's OS user (defaultInstallUser) used to hold those keys
	// directly, which meant a leaked Gopher SSH key gave the holder shell
	// access to the dashboard host with sudo iptables rights and read
	// access to gopher.db (every per-machine SSH private key, every token).
	//
	// The jumpbox user has no shell, no sudo, no homedir contents the
	// dashboard cares about. Its authorized_keys lines are written with
	// `restrict,permitopen="127.0.0.1:*"` so even a fully-compromised key
	// can only be used to forward to localhost ports on the VPS — exactly
	// the rathole bind addresses operators legitimately reach via the
	// jumpbox flow.
	defaultJumpboxUser    = "gopher-jump"
	defaultJumpboxHomeDir = "/var/lib/gopher-jump"

	// defaultDashboardPort matches the --port flag's default in cmd/server/main.go.
	// The install path opens this port in iptables so a freshly installed VPS
	// is reachable for setup; once the operator picks a different port at
	// runtime, ApplyDashboardPort handles the transition.
	defaultDashboardPort = 4321
)

type installConfig struct {
	user        string
	installDir  string
	dataDir     string
	serviceName string
}

func runInstall(args []string) error {
	cfg := installConfig{}
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.StringVar(&cfg.user, "user", defaultInstallUser, "system user to run gopher service")
	fs.StringVar(&cfg.installDir, "install-dir", defaultInstallDir, "installation directory")
	fs.StringVar(&cfg.dataDir, "data-dir", defaultDataDir, "data directory")
	fs.StringVar(&cfg.serviceName, "service-name", defaultServiceName, "systemd service name")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Before anything else, set up passwordless sudo for current user
	// This configures limited sudo access so subsequent commands don't need password prompts
	if err := ensurePasswordlessSudoForCurrentUser(); err != nil {
		fmt.Printf("Warning: could not configure passwordless sudo: %v\n", err)
		fmt.Println("Install will continue but may prompt for password for sudo operations")
	}

	if os.Geteuid() != 0 {
		return runWithSudo("install", args)
	}

	systemctlPath, err := exec.LookPath("systemctl")
	if err != nil {
		return fmt.Errorf("systemctl not found: %w", err)
	}
	teePath, err := exec.LookPath("tee")
	if err != nil {
		return fmt.Errorf("tee not found: %w", err)
	}
	mkdirPath, err := exec.LookPath("mkdir")
	if err != nil {
		return fmt.Errorf("mkdir not found: %w", err)
	}
	pkillPath, err := exec.LookPath("pkill")
	if err != nil {
		return fmt.Errorf("pkill not found: %w", err)
	}
	// iptables is best-effort — we only use it to open the dashboard port at
	// the very end. A box without iptables (e.g. nftables-only) just gets a
	// warning instead of a hard failure, since the operator can still reach
	// the dashboard via cloud-firewall rules or by switching modes later.
	iptablesPath, _ := exec.LookPath("iptables")

	fmt.Println("Installing Gopher service...")

	if err := ensureSystemUser(cfg.user, cfg.installDir); err != nil {
		return err
	}

	// Create the jumpbox user. Idempotent — pre-existing installs that
	// re-run install pick this up automatically. The user is created with
	// no shell so even if its authorized_keys lines somehow lacked the
	// `restrict` option, the keys still couldn't open a shell.
	if err := ensureSystemUser(defaultJumpboxUser, defaultJumpboxHomeDir); err != nil {
		return fmt.Errorf("create jumpbox user: %w", err)
	}
	// Ensure ~gopher-jump/.ssh exists with correct perms so the runtime
	// reconcile can write authorized_keys there without race-creating it.
	jumpboxSSHDir := filepath.Join(defaultJumpboxHomeDir, ".ssh")
	if err := os.MkdirAll(jumpboxSSHDir, 0700); err != nil {
		return fmt.Errorf("create %s: %w", jumpboxSSHDir, err)
	}
	if err := chownRecursive(defaultJumpboxUser, jumpboxSSHDir); err != nil {
		return fmt.Errorf("chown %s: %w", jumpboxSSHDir, err)
	}

	if err := os.MkdirAll(cfg.installDir, 0755); err != nil {
		return fmt.Errorf("failed to create install dir: %w", err)
	}
	if err := os.Chmod(cfg.installDir, 0755); err != nil {
		return fmt.Errorf("failed to set install dir mode: %w", err)
	}
	if err := os.Chown(cfg.installDir, 0, 0); err != nil {
		return fmt.Errorf("failed to set install dir ownership: %w", err)
	}
	if err := os.MkdirAll(cfg.dataDir, 0750); err != nil {
		return fmt.Errorf("failed to create data dir: %w", err)
	}

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to resolve current executable: %w", err)
	}
	targetBinary := filepath.Join(cfg.installDir, "gopher")
	if err := copyFile(exePath, targetBinary, 0755); err != nil {
		return fmt.Errorf("failed to deploy binary: %w", err)
	}
	if err := os.Chown(targetBinary, 0, 0); err != nil {
		return fmt.Errorf("failed to set binary ownership: %w", err)
	}

	if err := chownRecursive(cfg.user, cfg.dataDir); err != nil {
		return fmt.Errorf("failed to set data dir ownership: %w", err)
	}

	sudoersPath := filepath.Join("/etc/sudoers.d", cfg.user)
	sudoersContent := buildSudoers(cfg.user, systemctlPath, teePath, mkdirPath, pkillPath)
	if err := os.WriteFile(sudoersPath, []byte(sudoersContent), 0440); err != nil {
		return fmt.Errorf("failed to write sudoers file: %w", err)
	}
	if err := validateSudoers(sudoersPath); err != nil {
		_ = os.Remove(sudoersPath)
		return err
	}

	if invokingUser := strings.TrimSpace(os.Getenv("SUDO_USER")); invokingUser != "" && invokingUser != "root" && invokingUser != cfg.user {
		invokingUserPath := filepath.Join("/etc/sudoers.d", "gopher-"+sanitizeSudoersName(invokingUser))
		invokingUserContent := buildSudoers(invokingUser, systemctlPath, teePath, mkdirPath, pkillPath)
		if err := os.WriteFile(invokingUserPath, []byte(invokingUserContent), 0440); err != nil {
			return fmt.Errorf("failed to write invoking user sudoers file: %w", err)
		}
		if err := validateSudoers(invokingUserPath); err != nil {
			_ = os.Remove(invokingUserPath)
			return err
		}
	}

	servicePath := filepath.Join("/etc/systemd/system", cfg.serviceName+".service")
	serviceContent := buildServiceUnit(cfg.user, targetBinary, filepath.Join(cfg.dataDir, "gopher.db"))
	if err := os.WriteFile(servicePath, []byte(serviceContent), 0644); err != nil {
		return fmt.Errorf("failed to write systemd service: %w", err)
	}

	if err := runCommand("systemctl daemon-reload", systemctlPath, "daemon-reload"); err != nil {
		return err
	}
	if err := runCommand("systemctl enable "+cfg.serviceName, systemctlPath, "enable", cfg.serviceName); err != nil {
		return err
	}
	if err := runCommand("systemctl restart "+cfg.serviceName, systemctlPath, "restart", cfg.serviceName); err != nil {
		return err
	}

	if iptablesPath != "" {
		if err := ensureDashboardPortOpen(iptablesPath, defaultDashboardPort); err != nil {
			fmt.Printf("Warning: could not open dashboard port %d in iptables: %v\n", defaultDashboardPort, err)
			fmt.Printf("         Open it manually: sudo iptables -I INPUT -p tcp --dport %d -j ACCEPT\n", defaultDashboardPort)
		}
	} else {
		fmt.Printf("Note: iptables not found; ensure your firewall allows tcp/%d to reach the dashboard.\n", defaultDashboardPort)
	}

	fmt.Println()
	fmt.Println("Installation complete.")
	fmt.Printf("  Service: %s\n", cfg.serviceName)
	fmt.Printf("  Binary:  %s\n", targetBinary)
	fmt.Printf("  Data:    %s\n", cfg.dataDir)
	fmt.Printf("  Manage:  systemctl status %s\n", cfg.serviceName)
	fmt.Println()

	ips := detectPublicIPs()
	fmt.Println("Next step — finish setup in your browser:")
	if len(ips) == 0 {
		fmt.Printf("  http://<server-ip>:%d\n", defaultDashboardPort)
	} else {
		for _, ip := range ips {
			fmt.Printf("  http://%s:%d\n", ip, defaultDashboardPort)
		}
	}
	fmt.Println()
	fmt.Println("If your VPS sits behind a cloud firewall (AWS SG, GCP, etc.),")
	fmt.Printf("  also allow inbound tcp/%d there before opening the URL.\n", defaultDashboardPort)
	return nil
}

// ensureDashboardPortOpen idempotently inserts an INPUT ACCEPT rule for the
// dashboard port. iptables -C exits non-zero if the rule is missing, so we
// only insert when -C reports absent.
func ensureDashboardPortOpen(iptablesPath string, port int) error {
	portStr := fmt.Sprintf("%d", port)
	check := exec.Command(iptablesPath, "-C", "INPUT", "-p", "tcp", "--dport", portStr, "-j", "ACCEPT")
	if err := check.Run(); err == nil {
		return nil // rule already present
	}
	insert := exec.Command(iptablesPath, "-I", "INPUT", "-p", "tcp", "--dport", portStr, "-j", "ACCEPT")
	if out, err := insert.CombinedOutput(); err != nil {
		return fmt.Errorf("%w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// detectPublicIPs returns non-loopback IPv4 addresses from local interfaces.
// This is a best-effort hint for the operator — on cloud VMs the public IP
// is usually NAT'd outside the box, but the private one we surface is still
// useful for confirming "yes the service is bound" before they SSH into it.
func detectPublicIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var ips []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			ips = append(ips, ip.String())
		}
	}
	return ips
}

func ensureSystemUser(username, homeDir string) error {
	if err := exec.Command("id", "-u", username).Run(); err == nil {
		return nil
	}

	shell := "/usr/sbin/nologin"
	if _, err := os.Stat(shell); err != nil {
		shell = "/bin/false"
	}

	useraddPath, err := exec.LookPath("useradd")
	if err != nil {
		return fmt.Errorf("useradd not found in PATH: %w", err)
	}

	cmd := exec.Command(useraddPath, "-r", "-s", shell, "-d", homeDir, "-m", username)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create user %s: %w (%s)", username, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	if dstInfo, err := os.Stat(dst); err == nil && os.SameFile(srcInfo, dstInfo) {
		return os.Chmod(dst, mode)
	}

	// Write to a sibling temp file then atomic-rename over dst. open(O_TRUNC)
	// on a running ELF binary fails with ETXTBSY ("text file busy"), so we
	// can't truncate-and-write the destination directly when the gopher
	// service is currently executing /opt/gopher/gopher. rename(2) is a
	// directory-entry swap; the running process keeps its old inode open
	// until it exits, and the next systemctl restart picks up the new file.
	// This is what makes `gopher install` safe to re-run as an upgrade
	// path without first stopping the service.
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()

	tmp := dst + ".new"
	t, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(t, s); err != nil {
		_ = t.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := t.Chmod(mode); err != nil {
		_ = t.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := t.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func chownRecursive(username, path string) error {
	cmd := exec.Command("chown", "-R", username, path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("chown failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func validateSudoers(path string) error {
	visudoPath, err := exec.LookPath("visudo")
	if err != nil {
		return errors.New("visudo not found; cannot validate sudoers")
	}
	cmd := exec.Command(visudoPath, "-c", "-f", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sudoers validation failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runCommand(label, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s failed: %w (%s)", label, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func buildServiceUnit(user, binaryPath, dbPath string) string {
	return fmt.Sprintf(`[Unit]
Description=Gopher Tunnel Gateway
After=network.target

[Service]
Type=simple
User=%s
ExecStart=%s --db %s
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, user, binaryPath, dbPath)
}

func buildSudoers(user, systemctlPath, teePath, mkdirPath, pkillPath string) string {
	var lines []string
	lines = append(lines, "# Gopher server - limited sudo access")

	if systemctlPath != "" {
		lines = append(lines, fmt.Sprintf("%s ALL=(ALL:ALL) NOPASSWD: %s", user, systemctlPath))
	}
	if teePath != "" {
		lines = append(lines, fmt.Sprintf("%s ALL=(ALL:ALL) NOPASSWD: %s", user, teePath))
	}
	if mkdirPath != "" {
		lines = append(lines, fmt.Sprintf("%s ALL=(ALL:ALL) NOPASSWD: %s", user, mkdirPath))
	}
	if pkillPath != "" {
		lines = append(lines, fmt.Sprintf("%s ALL=(ALL:ALL) NOPASSWD: %s", user, pkillPath))
	}

	// File operations needed for config management and binary updates.
	lines = append(lines, fmt.Sprintf("%s ALL=(ALL:ALL) NOPASSWD: /bin/mv, /usr/bin/mv", user))
	lines = append(lines, fmt.Sprintf("%s ALL=(ALL:ALL) NOPASSWD: /bin/rm, /usr/bin/rm", user))
	lines = append(lines, fmt.Sprintf("%s ALL=(ALL:ALL) NOPASSWD: /usr/bin/chown, /bin/chown", user))
	lines = append(lines, fmt.Sprintf("%s ALL=(ALL:ALL) NOPASSWD: /bin/chmod, /usr/bin/chmod", user))

	// Firewall management.
	lines = append(lines, fmt.Sprintf("%s ALL=(ALL:ALL) NOPASSWD: /usr/sbin/iptables, /sbin/iptables", user))
	lines = append(lines, fmt.Sprintf("%s ALL=(ALL:ALL) NOPASSWD: /usr/sbin/iptables-save, /sbin/iptables-save", user))
	lines = append(lines, fmt.Sprintf("%s ALL=(ALL:ALL) NOPASSWD: /usr/sbin/iptables-restore, /sbin/iptables-restore", user))
	lines = append(lines, fmt.Sprintf("%s ALL=(ALL:ALL) NOPASSWD: /usr/sbin/ufw, /usr/bin/ufw", user))

	// Package manager for local service installation.
	if pkgMgrPath, err := exec.LookPath("dnf"); err == nil {
		lines = append(lines, fmt.Sprintf("%s ALL=(ALL:ALL) NOPASSWD: %s", user, pkgMgrPath))
	} else if pkgMgrPath, err := exec.LookPath("yum"); err == nil {
		lines = append(lines, fmt.Sprintf("%s ALL=(ALL:ALL) NOPASSWD: %s", user, pkgMgrPath))
	} else {
		lines = append(lines, fmt.Sprintf("%s ALL=(ALL:ALL) NOPASSWD: /usr/bin/apt-get, /bin/apt-get", user))
	}
	lines = append(lines, fmt.Sprintf("%s ALL=(ALL:ALL) NOPASSWD: /bin/bash, /usr/bin/bash", user))
	lines = append(lines, fmt.Sprintf("%s ALL=(ALL:ALL) NOPASSWD: /usr/bin/curl, /bin/curl", user))

	// Fail2ban management.
	lines = append(lines, fmt.Sprintf("%s ALL=(ALL:ALL) NOPASSWD: /usr/bin/fail2ban-client, /usr/local/bin/fail2ban-client", user))

	return strings.Join(lines, "\n") + "\n"
}
