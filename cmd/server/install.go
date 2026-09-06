package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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

	// Only systemctl is needed in this function (for daemon-reload / enable /
	// restart). Other commands (tee, mkdir, etc.) used to be looked up here
	// solely to embed their absolute paths into a narrow sudoers allowlist;
	// that's gone now that the gopher service user gets full NOPASSWD: ALL
	// (matches the client-side model and stops the "every new feature needs
	// another sudoers entry" treadmill — the narrow list was security theatre
	// since /bin/bash was on it anyway).
	systemctlPath, err := exec.LookPath("systemctl")
	if err != nil {
		return fmt.Errorf("systemctl not found: %w", err)
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

	// The supervisor extracts the bundled caddy/rathole into /opt/gopher/bin at
	// startup; gopher runs as cfg.user (unprivileged), so the dir must be
	// gopher-owned (the install dir itself is root-owned). Mirrors reinstall.sh.
	binDir := filepath.Join(cfg.installDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return fmt.Errorf("failed to create %s: %w", binDir, err)
	}
	if err := chownRecursive(cfg.user, binDir); err != nil {
		return fmt.Errorf("failed to set bin dir ownership: %w", err)
	}

	sudoersPath := filepath.Join("/etc/sudoers.d", cfg.user)
	sudoersContent := buildSudoers(cfg.user)
	if err := os.WriteFile(sudoersPath, []byte(sudoersContent), 0440); err != nil {
		return fmt.Errorf("failed to write sudoers file: %w", err)
	}
	if err := validateSudoers(sudoersPath); err != nil {
		_ = os.Remove(sudoersPath)
		return err
	}

	if invokingUser := strings.TrimSpace(os.Getenv("SUDO_USER")); invokingUser != "" && invokingUser != "root" && invokingUser != cfg.user {
		invokingUserPath := filepath.Join("/etc/sudoers.d", "gopher-"+sanitizeSudoersName(invokingUser))
		invokingUserContent := buildSudoers(invokingUser)
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

	publicIP := detectPublicIP()
	localIPs := detectLocalIPs()
	fmt.Println("Next step — finish setup in your browser:")
	switch {
	case publicIP != "":
		fmt.Printf("  http://%s:%d\n", publicIP, defaultDashboardPort)
		// A cloud VM's public IP is NAT'd, so it won't appear in localIPs;
		// surface the private address too for VPN / private-network access.
		for _, ip := range localIPs {
			if ip != publicIP {
				fmt.Printf("  http://%s:%d   (private network)\n", ip, defaultDashboardPort)
			}
		}
	case len(localIPs) > 0:
		// No public IP detected (egress blocked, or private-only host).
		for _, ip := range localIPs {
			fmt.Printf("  http://%s:%d\n", ip, defaultDashboardPort)
		}
	default:
		fmt.Printf("  http://<server-ip>:%d\n", defaultDashboardPort)
	}
	fmt.Println()
	fmt.Println("Cloud firewalls (AWS Security Groups, OCI Security Lists, GCP rules)")
	fmt.Println("  are a second layer in front of this server that Gopher cannot manage.")
	fmt.Printf("  Allow inbound tcp/%d there now to reach setup.\n", defaultDashboardPort)
	fmt.Println()
	fmt.Println("Simplest long-term: allow ALL inbound TCP at the cloud layer and let")
	fmt.Println("  Gopher's own firewall (set up in the wizard) do the enforcement —")
	fmt.Println("  tunnel ports are assigned dynamically, so per-port cloud rules will")
	fmt.Println("  fight you on every new tunnel. This assumes the VPS is dedicated to")
	fmt.Println("  Gopher: Docker-published ports bypass the OS firewall entirely.")
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
// detectLocalIPs returns the IPv4 addresses bound to local interfaces. On a
// cloud VM these are the private (RFC1918) addresses — the public IP is NAT'd
// upstream and never appears here — so this is only useful as a fallback or for
// VPN / private-network access. See detectPublicIP for the reachable address.
func detectLocalIPs() []string {
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

// detectPublicIP returns the VPS's public IPv4 as seen from the internet by
// asking external echo services. This is the address a browser actually uses to
// reach the dashboard on a NAT'd cloud VM, where detectLocalIPs only surfaces
// the private IP. Requires outbound HTTPS; returns "" if every probe fails
// (egress blocked, or a private-only host) so the caller can fall back.
func detectPublicIP() string {
	// Plain-text "what's my IP" endpoints; each returns the bare IP. Tried in
	// order, first valid public IPv4 wins. Short timeout so a blocked-egress
	// host doesn't stall the install.
	endpoints := []string{
		"https://checkip.amazonaws.com",
		"https://api.ipify.org",
		"https://icanhazip.com",
	}
	client := &http.Client{Timeout: 3 * time.Second}
	for _, url := range endpoints {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		if err != nil {
			continue
		}
		ip := net.ParseIP(strings.TrimSpace(string(body)))
		if ip == nil {
			continue
		}
		v4 := ip.To4()
		if v4 == nil || v4.IsPrivate() || v4.IsLoopback() || v4.IsLinkLocalUnicast() || v4.IsUnspecified() {
			continue
		}
		return v4.String()
	}
	return ""
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
# Marks this as a real managed edge: gopher only extracts/supervises the bundled
# caddy+rathole (and runs the destructive legacy-layout migration) when this is
# set, so test/dev/stray executions never touch a live edge.
Environment=GOPHER_MANAGED=1
ExecStart=%s --db %s
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, user, binaryPath, dbPath)
}

// buildSudoers grants the gopher service user (and the operator who ran the
// installer, when invoked via sudo) full passwordless sudo. This unifies the
// server-side model with the client-side bootstrap (see bootstrap.sh — the
// gopher user on bootstrapped machines also gets NOPASSWD: ALL).
//
// The previous narrow allowlist enumerated every binary the service needs
// (systemctl, tee, iptables, useradd, ...) and required a sudoers patch every
// time we added a new feature. It also included /bin/bash and /usr/bin/curl,
// so an attacker with shell-as-gopher could just `sudo bash` to escalate —
// the narrow scope was security theatre. Going to NOPASSWD: ALL drops ~15
// lines, removes the patch-treadmill, and is no weaker than the previous
// state.
func buildSudoers(user string) string {
	return "# Gopher service - full passwordless sudo\n" +
		user + " ALL=(ALL) NOPASSWD: ALL\n"
}
