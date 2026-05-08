package service

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/smalex-z/gopher/internal/db"
)

// gopherChain is the iptables chain that holds all dynamic tunnel rules.
const gopherChain = "GOPHER_TUNNELS"

// -- LocalSetupService firewall methods --------------------------------------

// FirewallDetect returns the current firewall state on the local system.
func (s *LocalSetupService) FirewallDetect() *FirewallStatus {
	return DetectFirewall()
}

// FirewallConfigure persists the chosen mode and, for "gopher" mode, takes over
// iptables management asynchronously, streaming progress to the log hub.
func (s *LocalSetupService) FirewallConfigure(mode string) {
	go func() {
		w := &hubWriter{hub: s.hub}
		if err := doFirewallConfigure(mode, w); err != nil {
			fmt.Fprintf(w, "ERROR: %v\n", err)
			s.hub.Broadcast("\x00ERROR")
			return
		}
		s.hub.Broadcast("\x00DONE")
	}()
}

func doFirewallConfigure(mode string, logWriter io.Writer) error {
	fmt.Fprintf(logWriter, "=== Firewall Configuration (mode: %s) ===\n", mode)

	if mode == "gopher" {
		if err := firewallTakeover(logWriter); err != nil {
			return err
		}
	}

	settings, err := db.GetSettings()
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}
	settings.FirewallMode = mode
	if err := db.SaveSettings(settings); err != nil {
		return fmt.Errorf("failed to save firewall mode: %w", err)
	}

	fmt.Fprintln(logWriter, "=== Firewall configuration complete ===")
	return nil
}

// -- Takeover sequence -------------------------------------------------------

func firewallTakeover(logWriter io.Writer) error {
	sudo := privilegedCmdPrefix()
	status := DetectFirewall()

	// Step 1: Backup existing rules.
	fmt.Fprintln(logWriter, "Step 1: Backing up existing iptables rules...")
	if err := firewallBackup(logWriter, sudo); err != nil {
		fmt.Fprintf(logWriter, "  WARN: could not back up rules: %v\n", err)
	}

	// Step 2: Disable conflicting firewall managers.
	fmt.Fprintln(logWriter, "Step 2: Disabling conflicting firewall managers...")
	if err := firewallDisableConflicting(status, logWriter, sudo); err != nil {
		return err
	}

	// Step 3: Flush existing rules and set safe default policies.
	fmt.Fprintln(logWriter, "Step 3: Initializing iptables rules...")
	if err := firewallInitRules(logWriter, sudo); err != nil {
		return err
	}

	// Step 4: Create GOPHER_TUNNELS and GOPHER_CUSTOM chains.
	fmt.Fprintf(logWriter, "Step 4: Creating %s and %s chains...\n", gopherChain, gopherCustomChain)
	if err := firewallCreateChain(logWriter, sudo); err != nil {
		return err
	}
	if err := ensureCustomChain(); err != nil {
		fmt.Fprintf(logWriter, "  WARN: could not create %s chain: %v\n", gopherCustomChain, err)
	}

	// Step 5: Open ports for tunnels already in the DB.
	fmt.Fprintln(logWriter, "Step 5: Opening existing tunnel ports...")
	if err := firewallOpenExistingTunnelPorts(logWriter); err != nil {
		fmt.Fprintf(logWriter, "  WARN: could not open some tunnel ports: %v\n", err)
	}

	// Step 5b: Apply dashboard port visibility per saved setting.
	settings, err := db.GetSettings()
	if err == nil {
		if settings.DashboardPrivate {
			fmt.Fprintf(logWriter, "  Dashboard port %d: restricting to localhost (DashboardPrivate=true)\n", dashboardPort)
			if dErr := iptablesMakePrivate(dashboardPort, "tcp"); dErr != nil {
				fmt.Fprintf(logWriter, "  WARN: could not restrict dashboard port: %v\n", dErr)
			}
		} else {
			fmt.Fprintf(logWriter, "  Dashboard port %d: opening publicly\n", dashboardPort)
			if dErr := iptablesOpenPort(dashboardPort, "tcp"); dErr != nil {
				fmt.Fprintf(logWriter, "  WARN: could not open dashboard port: %v\n", dErr)
			}
		}
	}

	// Step 6: Persist rules across reboots.
	fmt.Fprintln(logWriter, "Step 6: Saving iptables rules for persistence...")
	if err := firewallSaveRules(logWriter, sudo); err != nil {
		fmt.Fprintf(logWriter, "  WARN: could not persist rules: %v\n", err)
	}

	// Step 7: Reload fail2ban so it recreates its chains on top of the fresh ruleset.
	// iptables -F/-X above wiped fail2ban's f2b-* chains; without a reload, active
	// bans remain in fail2ban's internal state but are no longer enforced in iptables.
	// Use systemd's reload-or-restart so a fail2ban that's not yet up doesn't error
	// out the takeover (its socket may not be ready right after the install step).
	if isCommandAvailable("fail2ban-client") {
		fmt.Fprintln(logWriter, "Step 7: Reloading fail2ban to restore ban rules...")
		reloadCmd := append(sudo, "systemctl", "reload-or-restart", "fail2ban")
		if err := exec.Command(reloadCmd[0], reloadCmd[1:]...).Run(); err != nil { // #nosec G204
			fmt.Fprintf(logWriter, "  WARN: fail2ban reload failed: %v\n", err)
		} else {
			fmt.Fprintln(logWriter, "  fail2ban reloaded ✓")
		}
	}

	// Step 8: Kick Caddy so it retries ACME cert issuance immediately now that
	// port 80 is open. Without this, Caddy keeps backing off (up to ~minutes)
	// from earlier failed attempts before the firewall opened.
	if isCommandAvailable("caddy") {
		if settings, sErr := db.GetSettings(); sErr == nil && settings.Domain != "" {
			fmt.Fprintln(logWriter, "Step 8: Reloading Caddy to retry cert issuance on now-open port 80...")
			reloadCaddy := append(sudo, "systemctl", "reload", "caddy")
			if err := exec.Command(reloadCaddy[0], reloadCaddy[1:]...).Run(); err != nil { // #nosec G204
				fmt.Fprintf(logWriter, "  WARN: caddy reload failed: %v\n", err)
			} else {
				fmt.Fprintln(logWriter, "  Caddy reloaded ✓ (cert issuance may take ~30s)")
			}
		}
	}

	return nil
}

func firewallBackup(logWriter io.Writer, sudo []string) error {
	args := append(sudo, "iptables-save")
	cmd := exec.Command(args[0], args[1:]...) // #nosec G204
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	backupPath := "/root/gopher-firewall-backup.rules"
	if err := writeLocalFile(backupPath, string(out)); err != nil {
		return err
	}
	fmt.Fprintf(logWriter, "  Rules backed up to %s\n", backupPath)
	return nil
}

func firewallDisableConflicting(status *FirewallStatus, logWriter io.Writer, sudo []string) error {
	disabled := false
	if status.UFW.Active {
		fmt.Fprintln(logWriter, "  Disabling UFW...")
		args := append(sudo, "ufw", "disable")
		if err := runLocalCmd(logWriter, args[0], args[1:]...); err != nil {
			return fmt.Errorf("ufw disable: %w", err)
		}
		disabled = true
	}
	if status.Firewalld.Active {
		fmt.Fprintln(logWriter, "  Stopping firewalld...")
		stopArgs := append(sudo, "systemctl", "stop", "firewalld")
		_ = runLocalCmd(logWriter, stopArgs[0], stopArgs[1:]...)
		disArgs := append(sudo, "systemctl", "disable", "firewalld")
		_ = runLocalCmd(logWriter, disArgs[0], disArgs[1:]...)
		disabled = true
	}
	if status.NFTables.Active {
		fmt.Fprintln(logWriter, "  Stopping nftables...")
		stopArgs := append(sudo, "systemctl", "stop", "nftables")
		_ = runLocalCmd(logWriter, stopArgs[0], stopArgs[1:]...)
		disArgs := append(sudo, "systemctl", "disable", "nftables")
		_ = runLocalCmd(logWriter, disArgs[0], disArgs[1:]...)
		disabled = true
	}
	if !disabled {
		fmt.Fprintln(logWriter, "  No active firewall managers to disable.")
	}
	return nil
}

func firewallInitRules(logWriter io.Writer, sudo []string) error {
	// All arguments below are hardcoded constants — no user input. #nosec G204
	steps := [][]string{
		append(sudo, "iptables", "-F"),                                                                                             // flush rules
		append(sudo, "iptables", "-X"),                                                                                             // delete user chains
		append(sudo, "iptables", "-P", "INPUT", "DROP"),                                                                            // default deny incoming
		append(sudo, "iptables", "-P", "FORWARD", "DROP"),                                                                          // default deny forwarding
		append(sudo, "iptables", "-P", "OUTPUT", "ACCEPT"),                                                                         // allow outgoing
		append(sudo, "iptables", "-A", "INPUT", "-i", "lo", "-j", "ACCEPT"),                                                        // loopback
		append(sudo, "iptables", "-A", "INPUT", "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"),             // established
		append(sudo, "iptables", "-A", "INPUT", "-p", "tcp", "--dport", "22", "-j", "ACCEPT"),                                      // SSH — never block
		append(sudo, "iptables", "-A", "INPUT", "-p", "tcp", "--dport", "80", "-j", "ACCEPT"),                                      // HTTP
		append(sudo, "iptables", "-A", "INPUT", "-p", "tcp", "--dport", "443", "-j", "ACCEPT"),                                     // HTTPS
		append(sudo, "iptables", "-A", "INPUT", "-p", "tcp", "--dport", "2333", "-j", "ACCEPT"),                                    // rathole control
		// Dashboard port (Gopher) is handled via GOPHER_TUNNELS by ApplyDashboardPort, not hardcoded here.
	}
	for _, args := range steps {
		if err := runLocalCmd(logWriter, args[0], args[1:]...); err != nil {
			return err
		}
	}
	return nil
}

func firewallCreateChain(logWriter io.Writer, sudo []string) error {
	if logWriter == nil {
		logWriter = io.Discard
	}
	createArgs := append(sudo, "iptables", "-N", gopherChain)
	cmd := exec.Command(createArgs[0], createArgs[1:]...) // #nosec G204
	out, err := cmd.CombinedOutput()
	if err != nil {
		combined := string(out)
		if !strings.Contains(combined, "already exists") && !strings.Contains(combined, "Chain already exists") {
			return fmt.Errorf("create chain %s: %w (%s)", gopherChain, err, strings.TrimSpace(combined))
		}
		fmt.Fprintf(logWriter, "  Chain %s already exists, reusing.\n", gopherChain)
	} else {
		fmt.Fprintf(logWriter, "  Chain %s created.\n", gopherChain)
	}

	// Remove any duplicate INPUT → GOPHER_TUNNELS jumps, then add exactly one.
	delArgs := append(append([]string{}, sudo...), "iptables", "-D", "INPUT", "-j", gopherChain)
	for {
		if exec.Command(delArgs[0], delArgs[1:]...).Run() != nil { // #nosec G204
			break
		}
	}
	jumpArgs := append(sudo, "iptables", "-A", "INPUT", "-j", gopherChain)
	if err := runLocalCmd(logWriter, jumpArgs[0], jumpArgs[1:]...); err != nil {
		return fmt.Errorf("add INPUT -> %s jump: %w", gopherChain, err)
	}
	return nil
}

func firewallOpenExistingTunnelPorts(logWriter io.Writer) error {
	if logWriter == nil {
		logWriter = io.Discard
	}
	tunnels, err := db.GetTunnels()
	if err != nil {
		return err
	}
	machines, err := db.GetMachines()
	if err != nil {
		return err
	}
	for _, t := range tunnels {
		proto := t.Transport
		if proto == "" {
			proto = "tcp"
		}
		if t.Private {
			if err := iptablesMakePrivate(t.RatholePort, proto); err != nil {
				fmt.Fprintf(logWriter, "  WARN: port %d/%s (tunnel %s, private): %v\n", t.RatholePort, proto, t.ID, err)
			} else {
				fmt.Fprintf(logWriter, "  Restricted port %d/%s to localhost (tunnel %s)\n", t.RatholePort, proto, t.ID)
			}
		} else {
			if err := iptablesOpenPort(t.RatholePort, proto); err != nil {
				fmt.Fprintf(logWriter, "  WARN: port %d/%s (tunnel %s): %v\n", t.RatholePort, proto, t.ID, err)
			} else {
				fmt.Fprintf(logWriter, "  Opened port %d/%s (tunnel %s)\n", t.RatholePort, proto, t.ID)
			}
		}
	}
	for _, m := range machines {
		if m.TunnelPort == 0 {
			continue
		}
		if m.PublicSSH {
			if err := iptablesOpenPort(m.TunnelPort, "tcp"); err != nil {
				fmt.Fprintf(logWriter, "  WARN: port %d/tcp (machine %s SSH, public): %v\n", m.TunnelPort, m.ID, err)
			} else {
				fmt.Fprintf(logWriter, "  Opened port %d/tcp (machine %s SSH, public)\n", m.TunnelPort, m.ID)
			}
		} else {
			if err := iptablesMakePrivate(m.TunnelPort, "tcp"); err != nil {
				fmt.Fprintf(logWriter, "  WARN: port %d/tcp (machine %s SSH, private): %v\n", m.TunnelPort, m.ID, err)
			} else {
				fmt.Fprintf(logWriter, "  Restricted port %d/tcp to localhost (machine %s SSH)\n", m.TunnelPort, m.ID)
			}
		}
	}
	return nil
}

func firewallSaveRules(logWriter io.Writer, sudo []string) error {
	saveArgs := append(sudo, "iptables-save")
	cmd := exec.Command(saveArgs[0], saveArgs[1:]...) // #nosec G204
	rulesOut, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("iptables-save: %w", err)
	}
	rules := string(rulesOut)

	switch pkgManager() {
	case "dnf", "yum":
		pm := pkgManager()
		// Install iptables-services if not already present.
		if _, statErr := os.Stat("/etc/sysconfig/iptables"); os.IsNotExist(statErr) {
			installArgs := append(sudo, pm, "install", "-y", "-q", "iptables-services")
			if err := runLocalCmd(logWriter, installArgs[0], installArgs[1:]...); err != nil {
				return fmt.Errorf("install iptables-services: %w", err)
			}
			enableArgs := append(sudo, "systemctl", "enable", "iptables")
			_ = runLocalCmd(logWriter, enableArgs[0], enableArgs[1:]...)
		}
		if err := writeLocalFile("/etc/sysconfig/iptables", rules); err != nil {
			return fmt.Errorf("write /etc/sysconfig/iptables: %w", err)
		}
		fmt.Fprintln(logWriter, "  Rules saved to /etc/sysconfig/iptables")

	default: // apt (Debian/Ubuntu)
		// Install iptables-persistent if the rules file doesn't exist yet.
		if _, statErr := os.Stat("/etc/iptables/rules.v4"); os.IsNotExist(statErr) {
			installArgs := append(sudo, "bash", "-c",
				"DEBIAN_FRONTEND=noninteractive apt-get install -y -qq iptables-persistent")
			if err := runLocalCmd(logWriter, installArgs[0], installArgs[1:]...); err != nil {
				return fmt.Errorf("install iptables-persistent: %w", err)
			}
		}
		if err := sudoMkdir("/etc/iptables"); err != nil {
			return err
		}
		if err := writeLocalFile("/etc/iptables/rules.v4", rules); err != nil {
			return fmt.Errorf("write /etc/iptables/rules.v4: %w", err)
		}
		fmt.Fprintln(logWriter, "  Rules saved to /etc/iptables/rules.v4")
	}
	return nil
}

// -- Dynamic port management -------------------------------------------------

// ApplyTunnelPort opens or restricts a firewall port for a tunnel when in Gopher-managed mode.
// Private tunnels are restricted to localhost (127.0.0.1) via iptablesMakePrivate.
// Errors are non-fatal — tunnel creation is not blocked by firewall state.
func ApplyTunnelPort(port int, transport string, private bool) {
	settings, err := db.GetSettings()
	if err != nil || settings.FirewallMode != "gopher" {
		return
	}
	if !firewallChainExists() {
		return
	}
	proto := transport
	if proto == "" {
		proto = "tcp"
	}
	if private {
		if err := iptablesMakePrivate(port, proto); err != nil {
			log.Printf("firewall: could not restrict port %d/%s: %v", port, proto, err)
			return
		}
	} else {
		if err := iptablesOpenPort(port, proto); err != nil {
			log.Printf("firewall: could not open port %d/%s: %v", port, proto, err)
			return
		}
	}
	persistRules()
}

// ApplyDashboardPort opens or restricts the dashboard port based on the
// DashboardPrivate setting. No-op if not in Gopher-managed firewall mode.
func ApplyDashboardPort(private bool) {
	settings, err := db.GetSettings()
	if err != nil || settings.FirewallMode != "gopher" {
		return
	}
	if !firewallChainExists() {
		return
	}
	if private {
		if err := iptablesMakePrivate(dashboardPort, "tcp"); err != nil {
			log.Printf("firewall: could not restrict dashboard port %d: %v", dashboardPort, err)
			return
		}
	} else {
		if err := iptablesOpenPort(dashboardPort, "tcp"); err != nil {
			log.Printf("firewall: could not open dashboard port %d: %v", dashboardPort, err)
			return
		}
	}
	persistRules()
}

// RevokeTunnelPort removes the firewall rule for a deleted tunnel.
func RevokeTunnelPort(port int, transport string) {
	settings, err := db.GetSettings()
	if err != nil || settings.FirewallMode != "gopher" {
		return
	}
	if !firewallChainExists() {
		return
	}
	proto := transport
	if proto == "" {
		proto = "tcp"
	}
	iptablesClosePort(port, proto)
	persistRules()
}

// -- Low-level iptables helpers -----------------------------------------------

// iptablesOpenPort adds an ACCEPT rule for port/proto in GOPHER_TUNNELS (idempotent).
func iptablesOpenPort(port int, proto string) error {
	portStr := strconv.Itoa(port)
	// Use -C (check) to test existence before adding — avoids duplicates.
	checkCmd := exec.Command("sudo", "-n", "iptables", "-C", gopherChain, // #nosec G204
		"-p", proto, "--dport", portStr, "-j", "ACCEPT")
	if checkCmd.Run() == nil {
		return nil // rule already present
	}
	// Remove any residual private DROP rules for this port.
	iptablesDeleteRule(gopherChain, "-p", proto, "--dport", portStr, "-s", "127.0.0.1", "-j", "ACCEPT")
	iptablesDeleteRule(gopherChain, "-p", proto, "--dport", portStr, "-j", "DROP")

	addCmd := exec.Command("sudo", "-n", "iptables", "-A", gopherChain, // #nosec G204
		"-p", proto, "--dport", portStr, "-j", "ACCEPT")
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("iptables -A GOPHER_TUNNELS: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// iptablesClosePort removes all GOPHER_TUNNELS rules for port/proto.
func iptablesClosePort(port int, proto string) {
	portStr := strconv.Itoa(port)
	iptablesDeleteRule(gopherChain, "-p", proto, "--dport", portStr, "-j", "ACCEPT")
	iptablesDeleteRule(gopherChain, "-p", proto, "--dport", portStr, "-s", "127.0.0.1", "-j", "ACCEPT")
	iptablesDeleteRule(gopherChain, "-p", proto, "--dport", portStr, "-j", "DROP")
}

// iptablesMakePrivate restricts port to localhost, dropping all external traffic.
func iptablesMakePrivate(port int, proto string) error {
	portStr := strconv.Itoa(port)
	iptablesDeleteRule(gopherChain, "-p", proto, "--dport", portStr, "-j", "ACCEPT")

	loCmd := exec.Command("sudo", "-n", "iptables", "-A", gopherChain, // #nosec G204
		"-p", proto, "--dport", portStr, "-s", "127.0.0.1", "-j", "ACCEPT")
	if out, err := loCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("iptables private accept: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	dropCmd := exec.Command("sudo", "-n", "iptables", "-A", gopherChain, // #nosec G204
		"-p", proto, "--dport", portStr, "-j", "DROP")
	if out, err := dropCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("iptables private drop: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// iptablesDeleteRule loops -D until no more matching rules exist (idempotent).
func iptablesDeleteRule(chain string, ruleSpec ...string) {
	args := append([]string{"-n", "iptables", "-D", chain}, ruleSpec...)
	for {
		cmd := exec.Command("sudo", args...) // #nosec G204
		if cmd.Run() != nil {
			break
		}
	}
}

// firewallChainExists returns true if the GOPHER_TUNNELS chain is present in iptables.
func firewallChainExists() bool {
	cmd := exec.Command("sudo", "-n", "iptables", "-L", gopherChain, "-n") // #nosec G204
	return cmd.Run() == nil
}

// persistRules is a best-effort save of iptables rules after a dynamic change.
func persistRules() {
	sudo := privilegedCmdPrefix()
	var savePath string
	switch pkgManager() {
	case "dnf", "yum":
		savePath = "/etc/sysconfig/iptables"
	default:
		savePath = "/etc/iptables/rules.v4"
	}
	saveArgs := append(sudo, "iptables-save")
	cmd := exec.Command(saveArgs[0], saveArgs[1:]...) // #nosec G204
	if out, err := cmd.Output(); err == nil {
		_ = writeLocalFile(savePath, string(out))
	}
}
