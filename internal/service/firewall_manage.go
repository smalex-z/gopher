package service

import (
	"errors"
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

// ErrFirewallNotManaged is returned by mutating firewall operations when the
// operator chose "manual" or "none" mode — gopher must not touch iptables (or
// record settings that only iptables can enforce) on a host it doesn't manage.
var ErrFirewallNotManaged = errors.New("firewall is not gopher-managed")

// -- LocalSetupService firewall methods --------------------------------------

// FirewallDetect returns the current firewall state on the local system.
func (s *LocalSetupService) FirewallDetect() *FirewallStatus {
	return DetectFirewall()
}

// FirewallConfigure persists the chosen mode and, for "gopher" mode, takes over
// iptables management asynchronously, streaming progress to the log hub.
//
// Returns ErrOpInProgress if another op is already streaming through the
// shared LogHub.
func (s *LocalSetupService) FirewallConfigure(mode string) error {
	if !s.hub.TryAcquireOp() {
		return ErrOpInProgress
	}
	go goSafe("firewallConfigure", func() {
		defer s.hub.ReleaseOp()
		w := &hubWriter{hub: s.hub}
		if err := doFirewallConfigure(mode, w); err != nil {
			fmt.Fprintf(w, "ERROR: %v\n", err)
			s.hub.Broadcast("\x00ERROR")
			return
		}
		s.hub.Broadcast("\x00DONE")
	})
	return nil
}

func doFirewallConfigure(mode string, logWriter io.Writer) error {
	fmt.Fprintf(logWriter, "=== Firewall Configuration (mode: %s) ===\n", mode)

	if mode == "gopher" {
		if err := firewallTakeover(logWriter); err != nil {
			return err
		}
	}

	if err := db.MutateSettings(func(s *db.AppSettings) error {
		s.FirewallMode = mode
		// The install step defaults DashboardPrivate=true assuming the takeover
		// will enforce it (step 5b). Choosing manual/none means nothing ever
		// will — clear it so status doesn't claim a privacy no firewall
		// provides.
		if mode != "gopher" {
			s.DashboardPrivate = false
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to save firewall mode: %w", err)
	}

	// Apply any custom rules already saved in the DB. The takeover only creates
	// the (empty) GOPHER_CUSTOM chain; rules persisted during a previous gopher
	// period would otherwise stay latent until the next restart's reconcile.
	// Must run after the mode save — reloadCustomChain no-ops unless "gopher".
	if mode == "gopher" {
		if err := reloadCustomChain(); err != nil {
			fmt.Fprintf(logWriter, "WARN: could not apply saved custom rules: %v\n", err)
		}
	}

	fmt.Fprintln(logWriter, "=== Firewall configuration complete ===")
	return nil
}

// SwitchFirewallMode changes the firewall strategy after setup completes (the
// wizard's /firewall/configure endpoint is locked once a mode is chosen).
//
// Both directions are full rebuilds, never diffs, so there is no half-migrated
// state to land in:
//   - → "gopher" runs the same takeover as the wizard (re-runnable by design,
//     see firewallInitRuleSteps) — async, streaming to the log hub. Existing
//     tunnel ports and saved custom rules are applied as part of it.
//   - "gopher" → "manual"/"none" tears down every piece of gopher iptables
//     state (chains, jumps, default-deny policies — TeardownFirewall, shared
//     with uninstall) and persists the now-permissive ruleset so a reboot
//     doesn't resurrect the old one. The host is left wide open until the
//     operator configures their own firewall — callers must warn about that.
//     DashboardPrivate is reset too: it was enforced by iptables alone, and
//     status must not claim a privacy nothing provides anymore.
//   - "manual" ↔ "none" only saves the setting.
//
// Returns started=true when an async gopher takeover was launched (completion
// signalled via the log hub); started=false when the switch finished here.
func (s *LocalSetupService) SwitchFirewallMode(mode string) (started bool, err error) {
	settings, err := db.GetSettings()
	if err != nil {
		return false, err
	}
	if settings.FirewallMode == mode {
		return false, nil
	}
	if mode == "gopher" {
		return true, s.FirewallConfigure(mode)
	}

	// Serialize with install/takeover ops even though nothing streams — a
	// teardown interleaving with a running takeover would corrupt both.
	if !s.hub.TryAcquireOp() {
		return false, ErrOpInProgress
	}
	defer s.hub.ReleaseOp()

	if settings.FirewallMode == "gopher" {
		logw := log.Writer()
		TeardownFirewall(logw)
		persistRules()
		firewallSaveRules6(logw, privilegedCmdPrefix())
	}
	return false, db.MutateSettings(func(as *db.AppSettings) error {
		as.FirewallMode = mode
		if settings.FirewallMode == "gopher" {
			as.DashboardPrivate = false
		}
		return nil
	})
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
	fmt.Fprintln(logWriter, "  NOTE: on a cloud VPS (OCI, AWS, etc.) TCP 80/443/2333 must also be")
	fmt.Fprintln(logWriter, "  opened in the provider's own firewall (e.g. OCI VCN Security List) —")
	fmt.Fprintln(logWriter, "  that's a separate layer in front of the OS that Gopher cannot manage.")

	// Step 3b: Mirror the same default-deny baseline onto IPv6 (best-effort) so
	// IPv4 restrictions don't silently leak over IPv6 on a dual-stack host.
	fmt.Fprintln(logWriter, "Step 3b: Applying IPv6 baseline (ip6tables)...")
	firewallInitRules6(logWriter, sudo)

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
	firewallSaveRules6(logWriter, sudo)

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
	if caddyAvailable() {
		if settings, sErr := db.GetSettings(); sErr == nil && settings.Domain != "" {
			fmt.Fprintln(logWriter, "Step 8: Reloading Caddy to retry cert issuance on now-open port 80...")
			// Admin-API reload, not `systemctl reload caddy`: the supervised Caddy
			// has no systemd unit (the legacy one is masked), so systemctl would
			// fail and the ACME retry this step exists for would never fire.
			if err := caddyReload(); err != nil {
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

// firewallInitRuleSteps returns the ordered iptables invocations that establish
// the default-deny INPUT baseline (each step is prefixed with `sudo`).
//
// Ordering is anti-lockout — identical rationale to firewallInitRules6:
// FirewallConfigure("gopher") is a re-runnable endpoint, so on a second run
// INPUT is already DROP from the prior takeover. Resetting INPUT to ACCEPT
// BEFORE the flush means the flush never strips the SSH/established allows
// while DROP is in force, and adding every allow BEFORE the default-deny
// policy means a partial failure mid-sequence leaves INPUT in ACCEPT (its
// pre-takeover, still-reachable state) rather than a locked DROP-with-no-SSH.
// NOTE: only port 22 is hard-allowed; a non-standard sshd port would be
// reachable only via conntrack until reboot. Deployments here run on 22.
//
// This ordering is the single most safety-critical invariant in the file, so
// it's built as pure data here and asserted by TestFirewallInitRuleOrdering —
// keep the sequence and that test in sync. All arguments are hardcoded
// constants — no user input. #nosec G204
func firewallInitRuleSteps(sudo []string) [][]string {
	return [][]string{
		append(sudo, "iptables", "-P", "INPUT", "ACCEPT"),                   // open BEFORE flush (anti-lockout)
		append(sudo, "iptables", "-F"),                                      // flush rules
		append(sudo, "iptables", "-X"),                                      // delete user chains
		append(sudo, "iptables", "-P", "OUTPUT", "ACCEPT"),                  // allow outgoing
		append(sudo, "iptables", "-A", "INPUT", "-i", "lo", "-j", "ACCEPT"), // loopback
		append(sudo, "iptables", "-A", "INPUT", "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"), // established
		append(sudo, "iptables", "-A", "INPUT", "-p", "tcp", "--dport", "22", "-j", "ACCEPT"),                          // SSH — never block
		append(sudo, "iptables", "-A", "INPUT", "-p", "tcp", "--dport", "80", "-j", "ACCEPT"),                          // HTTP
		append(sudo, "iptables", "-A", "INPUT", "-p", "tcp", "--dport", "443", "-j", "ACCEPT"),                         // HTTPS
		append(sudo, "iptables", "-A", "INPUT", "-p", "tcp", "--dport", "2333", "-j", "ACCEPT"),                        // rathole control
		// Default-deny policy LAST — only after every allow above is in place.
		append(sudo, "iptables", "-P", "INPUT", "DROP"),   // default deny incoming
		append(sudo, "iptables", "-P", "FORWARD", "DROP"), // default deny forwarding
		// Dashboard port (Gopher) is handled via GOPHER_TUNNELS by ApplyDashboardPort, not hardcoded here.
	}
}

func firewallInitRules(logWriter io.Writer, sudo []string) error {
	for _, args := range firewallInitRuleSteps(sudo) {
		if err := runLocalCmd(logWriter, args[0], args[1:]...); err != nil {
			return err
		}
	}
	return nil
}

// firewallInitRules6 mirrors the IPv4 baseline onto ip6tables so the same
// default-deny + allow-list applies over IPv6. Without it the IPv4 rules
// (including the dashboard-private restriction) silently don't apply to v6 — a
// dual-stack listener like the dashboard would stay reachable over IPv6.
//
// Best-effort by design: a host may have no IPv6 or no ip6tables, and the IPv4
// firewall is the primary, so this never aborts the takeover. Allow rules are
// added BEFORE the default-DROP policy, so a partial failure leaves IPv6 in its
// prior (open) state rather than half-locked with no SSH.
func firewallInitRules6(logWriter io.Writer, sudo []string) {
	if logWriter == nil {
		logWriter = io.Discard
	}
	allow := [][]string{
		// Reset INPUT to ACCEPT BEFORE flushing. On a re-run the policy is
		// already DROP from the prior takeover; flushing then would strip the
		// SSH allow while DROP is in force, and any later failure would return
		// with v6 locked. Opening first means a partial failure leaves v6 open
		// (its pre-takeover state), never half-locked.
		append(sudo, "ip6tables", "-P", "INPUT", "ACCEPT"),
		append(sudo, "ip6tables", "-F"),
		append(sudo, "ip6tables", "-X"),
		append(sudo, "ip6tables", "-A", "INPUT", "-i", "lo", "-j", "ACCEPT"),
		append(sudo, "ip6tables", "-A", "INPUT", "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"),
		append(sudo, "ip6tables", "-A", "INPUT", "-p", "tcp", "--dport", "22", "-j", "ACCEPT"),
		append(sudo, "ip6tables", "-A", "INPUT", "-p", "tcp", "--dport", "80", "-j", "ACCEPT"),
		append(sudo, "ip6tables", "-A", "INPUT", "-p", "tcp", "--dport", "443", "-j", "ACCEPT"),
		append(sudo, "ip6tables", "-A", "INPUT", "-p", "tcp", "--dport", "2333", "-j", "ACCEPT"),
	}
	for _, args := range allow {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil { // #nosec G204
			fmt.Fprintf(logWriter, "  WARN: IPv6 firewall not applied (%s) — leaving ip6tables untouched. IPv6 may be unconfigured on this host.\n", strings.TrimSpace(string(out)))
			return
		}
	}
	deny := [][]string{
		append(sudo, "ip6tables", "-P", "INPUT", "DROP"),
		append(sudo, "ip6tables", "-P", "FORWARD", "DROP"),
		append(sudo, "ip6tables", "-P", "OUTPUT", "ACCEPT"),
	}
	for _, args := range deny {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil { // #nosec G204
			fmt.Fprintf(logWriter, "  WARN: ip6tables policy step failed: %s\n", strings.TrimSpace(string(out)))
		}
	}
	fmt.Fprintln(logWriter, "  IPv6 baseline applied (ip6tables default-deny mirrors IPv4)")
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
			if err := iptablesOpenPortRateLimited(m.TunnelPort, "tcp"); err != nil {
				fmt.Fprintf(logWriter, "  WARN: port %d/tcp (machine %s SSH, public): %v\n", m.TunnelPort, m.ID, err)
			} else {
				fmt.Fprintf(logWriter, "  Opened port %d/tcp with rate limit (machine %s SSH, public)\n", m.TunnelPort, m.ID)
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
			if err := aptInstallNoninteractive(logWriter, sudo, "iptables-persistent"); err != nil {
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

// aptInstallNoninteractive runs `apt-get install -y -qq <pkgs>` with
// DEBIAN_FRONTEND=noninteractive. sudo's env_reset strips the variable from
// the process environment, so under sudo it must ride the command line —
// permitted by the SETENV: tag on the package-manager entry in the bootstrap
// sudoers (and implied by NOPASSWD:ALL on installed services). As root it
// just goes in the env. This exists so the narrow sudoers doesn't need to
// grant bash for a one-off `bash -c "VAR=x apt-get ..."`.
func aptInstallNoninteractive(logWriter io.Writer, sudo []string, pkgs ...string) error {
	aptArgs := append([]string{"install", "-y", "-qq"}, pkgs...)
	var cmd *exec.Cmd
	if len(sudo) == 0 {
		cmd = exec.Command("apt-get", aptArgs...) // #nosec G204
		cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	} else {
		args := append(append(append([]string{}, sudo[1:]...), "DEBIAN_FRONTEND=noninteractive", "apt-get"), aptArgs...)
		cmd = exec.Command(sudo[0], args...) // #nosec G204
	}
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	return cmd.Run()
}

// firewallSaveRules6 persists the ip6tables baseline so it survives reboot.
// Best-effort mirror of firewallSaveRules for IPv6.
func firewallSaveRules6(logWriter io.Writer, sudo []string) {
	if logWriter == nil {
		logWriter = io.Discard
	}
	saveArgs := append(sudo, "ip6tables-save")
	out, err := exec.Command(saveArgs[0], saveArgs[1:]...).Output() // #nosec G204
	if err != nil {
		fmt.Fprintf(logWriter, "  WARN: ip6tables-save failed: %v (IPv6 rules won't persist across reboot)\n", err)
		return
	}
	rules := string(out)
	switch pkgManager() {
	case "dnf", "yum":
		if werr := writeLocalFile("/etc/sysconfig/ip6tables", rules); werr != nil {
			fmt.Fprintf(logWriter, "  WARN: write /etc/sysconfig/ip6tables: %v\n", werr)
			return
		}
		enableArgs := append(sudo, "systemctl", "enable", "ip6tables")
		_ = runLocalCmd(logWriter, enableArgs[0], enableArgs[1:]...)
	default: // apt — iptables-persistent (installed for v4) also restores rules.v6
		if werr := writeLocalFile("/etc/iptables/rules.v6", rules); werr != nil {
			fmt.Fprintf(logWriter, "  WARN: write /etc/iptables/rules.v6: %v\n", werr)
			return
		}
	}
	fmt.Fprintln(logWriter, "  IPv6 rules persisted")
}

// -- Dynamic port management -------------------------------------------------

// ApplyTunnelPort opens or restricts a firewall port for a tunnel when in Gopher-managed mode.
// Private tunnels are restricted to localhost (127.0.0.1) via iptablesMakePrivate.
// Errors are non-fatal — tunnel creation is not blocked by firewall state.
// ApplyTunnelPort returns an error on failure (in addition to logging and
// recording a persistent event) — see recordFirewallFailure's doc comment for
// why this matters. Most callers still treat the error as non-fatal (the
// tunnel/machine row itself is more important to keep than to roll back over
// a firewall hiccup) but MUST at least check it so the failure isn't silently
// dropped on the floor the way it used to be.
func ApplyTunnelPort(port int, transport string, private bool) error {
	settings, err := db.GetSettings()
	if err != nil || settings.FirewallMode != "gopher" {
		return nil
	}
	if !firewallChainExists() {
		return nil
	}
	proto := transport
	if proto == "" {
		proto = "tcp"
	}
	action := "open"
	if private {
		action = "restrict"
	}
	if private {
		if err := iptablesMakePrivate(port, proto); err != nil {
			return recordFirewallFailure(port, proto, action, err)
		}
	} else {
		if err := iptablesOpenPort(port, proto); err != nil {
			return recordFirewallFailure(port, proto, action, err)
		}
	}
	persistRules()
	return nil
}

// recordFirewallFailure logs AND persists a firewall_apply_failed event.
//
// Found in a QA sweep: ApplyTunnelPort/RevokeTunnelPort/ApplyDashboardPort/
// ApplyPublicSSHPort were void functions — a failure only ever reached
// log.Printf, never the caller, never the DB. Combined with there being no
// periodic firewall reconciliation loop anywhere (only a one-shot check at
// server startup), a failed rule application had NO path to visibility or
// self-healing: a tunnel meant to be private could stay open, or one meant to
// be open could stay closed, until an operator noticed something was wrong by
// other means (or a restart happened to paper over it) and re-ran firewall
// setup by hand. This doesn't add automatic re-reconciliation (a firewall
// drift-repair loop deserves its own dedicated design, not a bolt-on here) —
// it makes the failure impossible to miss on the Events/Security page instead
// of invisible.
func recordFirewallFailure(port int, proto, action string, cause error) error {
	err := fmt.Errorf("firewall: could not %s port %d/%s: %w", action, port, proto, cause)
	log.Print(err)
	db.RecordEvent(&db.Event{
		Severity:     "error",
		Source:       "firewall",
		Kind:         "firewall_apply_failed",
		ResourceType: "port",
		ResourceID:   strconv.Itoa(port),
		Message:      err.Error(),
	})
	return err
}

// ApplyPublicSSHPort opens a publicly-exposed SSH tunnel port with edge-side
// per-source-IP connection rate limiting. This is the defense for public SSH:
// the rathole tunnel hides the real client IP from the origin's sshd (it sees
// 127.0.0.1), so origin-side fail2ban can't work — the edge is the only place
// that sees the attacker's IP and can throttle brute force. No-op unless gopher
// manages the firewall.
func ApplyPublicSSHPort(port int) error {
	settings, err := db.GetSettings()
	if err != nil || settings.FirewallMode != "gopher" {
		return nil
	}
	if !firewallChainExists() {
		return nil
	}
	if err := iptablesOpenPortRateLimited(port, "tcp"); err != nil {
		return recordFirewallFailure(port, "tcp", "open (rate-limited)", err)
	}
	persistRules()
	return nil
}

// ApplyDashboardPort opens or restricts the dashboard port based on the
// DashboardPrivate setting. No-op if not in Gopher-managed firewall mode.
func ApplyDashboardPort(private bool) error {
	settings, err := db.GetSettings()
	if err != nil || settings.FirewallMode != "gopher" {
		return nil
	}
	if !firewallChainExists() {
		return nil
	}
	if private {
		if err := iptablesMakePrivate(dashboardPort, "tcp"); err != nil {
			return recordFirewallFailure(dashboardPort, "tcp", "restrict", err)
		}
	} else {
		if err := iptablesOpenPort(dashboardPort, "tcp"); err != nil {
			return recordFirewallFailure(dashboardPort, "tcp", "open", err)
		}
	}
	persistRules()
	return nil
}

// RevokeTunnelPort removes the firewall rule for a deleted tunnel.
// iptablesClosePort's own deletes are already idempotent best-effort (missing
// rules are not an error), so there's nothing to surface here beyond what it
// already logs internally — this stays void deliberately, unlike the Apply*
// functions above where "did the open/restrict actually take" is the whole
// point of the call.
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

// Per-source-IP new-connection rate limit for public SSH ports. Under the
// threshold, connections pass; a source above it is dropped until it backs off.
// Tuned for legitimate use (a human reconnecting) while blunting brute force.
const (
	sshRateLimit = "6/min"
	sshRateBurst = "6"
)

// sshRateLimitDropSpec is the hashlimit DROP rule that precedes the ACCEPT for a
// public SSH port. Kept in one place so open and close use identical args (an
// iptables -D must match the -A byte-for-byte).
func sshRateLimitDropSpec(portStr string) []string {
	return []string{
		"-p", "tcp", "--dport", portStr,
		"-m", "conntrack", "--ctstate", "NEW",
		"-m", "hashlimit",
		"--hashlimit-above", sshRateLimit,
		"--hashlimit-burst", sshRateBurst,
		"--hashlimit-mode", "srcip",
		"--hashlimit-name", "gopher_ssh_" + portStr,
		"-j", "DROP",
	}
}

// iptablesOpenPortRateLimited lays down the [rate-limit DROP, ACCEPT] pair for a
// public SSH port, in that order. It first clears any existing rules for the
// port (plain accept, private, or a stale pair) so the ordering is deterministic
// every reconcile. During the brief rebuild the port falls through to the
// INPUT DROP policy (fail-closed), never open.
func iptablesOpenPortRateLimited(port int, proto string) error {
	portStr := strconv.Itoa(port)
	// Clear prior rules for this port so we can re-lay the pair in order.
	iptablesDeleteRule(gopherChain, "-p", proto, "--dport", portStr, "-j", "ACCEPT")
	iptablesDeleteRule(gopherChain, "-p", proto, "--dport", portStr, "-s", "127.0.0.1", "-j", "ACCEPT")
	iptablesDeleteRule(gopherChain, "-p", proto, "--dport", portStr, "-j", "DROP")
	iptablesDeleteRule(gopherChain, sshRateLimitDropSpec(portStr)...)

	dropArgs := append([]string{"-n", "iptables", "-A", gopherChain}, sshRateLimitDropSpec(portStr)...)
	if out, err := exec.Command("sudo", dropArgs...).CombinedOutput(); err != nil { // #nosec G204
		return fmt.Errorf("iptables add SSH rate-limit: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	acceptCmd := exec.Command("sudo", "-n", "iptables", "-A", gopherChain, // #nosec G204
		"-p", proto, "--dport", portStr, "-j", "ACCEPT")
	if out, err := acceptCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("iptables add SSH accept: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// iptablesClosePort removes all GOPHER_TUNNELS rules for port/proto.
func iptablesClosePort(port int, proto string) {
	portStr := strconv.Itoa(port)
	iptablesDeleteRule(gopherChain, "-p", proto, "--dport", portStr, "-j", "ACCEPT")
	iptablesDeleteRule(gopherChain, "-p", proto, "--dport", portStr, "-s", "127.0.0.1", "-j", "ACCEPT")
	iptablesDeleteRule(gopherChain, "-p", proto, "--dport", portStr, "-j", "DROP")
	// Also drop the public-SSH rate-limit rule, if this was such a port.
	if proto == "tcp" {
		iptablesDeleteRule(gopherChain, sshRateLimitDropSpec(portStr)...)
	}
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

// TeardownFirewall undoes the gopher firewall takeover on uninstall: it removes
// the GOPHER_TUNNELS/GOPHER_CUSTOM chains + their INPUT jumps and resets the
// INPUT/FORWARD policies to ACCEPT, so the box is left with a permissive default
// rather than a DROP policy whose managing rules are gone (which would otherwise
// silently strand every tunnel port the chain held + an orphaned chain). No-op
// when the chain doesn't exist (gopher never took over), so it never touches an
// operator's own firewall. All best-effort — uninstall must not fail on this.
func TeardownFirewall(logWriter io.Writer) {
	if logWriter == nil {
		logWriter = io.Discard
	}
	if !isCommandAvailable("iptables") {
		return
	}
	sudo := privilegedCmdPrefix()
	chk := append(append([]string{}, sudo...), "iptables", "-L", gopherChain, "-n")
	if exec.Command(chk[0], chk[1:]...).Run() != nil { // #nosec G204 — proof the takeover ran
		return
	}
	fmt.Fprintln(logWriter, "Removing Gopher firewall chains and resetting INPUT/FORWARD policy...")

	for _, chain := range []string{gopherChain, gopherCustomChain} {
		// Drop every INPUT jump to this chain (there may be duplicates).
		del := append(append([]string{}, sudo...), "iptables", "-D", "INPUT", "-j", chain)
		for exec.Command(del[0], del[1:]...).Run() == nil { // #nosec G204
		}
		flush := append(append([]string{}, sudo...), "iptables", "-F", chain)
		_ = exec.Command(flush[0], flush[1:]...).Run() // #nosec G204
		drop := append(append([]string{}, sudo...), "iptables", "-X", chain)
		_ = exec.Command(drop[0], drop[1:]...).Run() // #nosec G204
	}
	// Reset to a permissive default so the box isn't locked down with no manager.
	for _, chain := range []string{"INPUT", "FORWARD"} {
		pol := append(append([]string{}, sudo...), "iptables", "-P", chain, "ACCEPT")
		_ = exec.Command(pol[0], pol[1:]...).Run() // #nosec G204
	}
	// The takeover mirrored default-deny onto ip6tables (firewallInitRules6);
	// reset those policies too so IPv6 isn't left half-locked with no manager.
	// The leftover allow rules are harmless under an ACCEPT policy.
	if isCommandAvailable("ip6tables") {
		for _, chain := range []string{"INPUT", "FORWARD"} {
			pol := append(append([]string{}, sudo...), "ip6tables", "-P", chain, "ACCEPT")
			_ = exec.Command(pol[0], pol[1:]...).Run() // #nosec G204
		}
	}
	fmt.Fprintln(logWriter, "  Gopher firewall state removed; INPUT/FORWARD policy = ACCEPT")
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
