package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/db"
	"github.com/smalex-z/gopher/internal/service"
)

func runUninstall(args []string) error {
	cfg := installConfig{}
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.StringVar(&cfg.user, "user", defaultInstallUser, "system user that owns gopher sudoers entry")
	fs.StringVar(&cfg.installDir, "install-dir", defaultInstallDir, "installation directory")
	fs.StringVar(&cfg.dataDir, "data-dir", defaultDataDir, "data directory")
	fs.StringVar(&cfg.serviceName, "service-name", defaultServiceName, "systemd service name")
	skipPrompts := fs.Bool("skip-prompts", false, "skip all confirmation prompts and remove everything")
	keepData := fs.Bool("keep-data", false, "preserve the data directory (database, certs, state); overrides --skip-prompts")
	keepOrigins := fs.Bool("keep-origins", false, "don't uninstall the connected origin machines (leave their agent/tunnel installed)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if os.Geteuid() != 0 {
		return runWithSudo("uninstall", args)
	}

	// Tear down reachable origins first, while the rathole tunnels are still up
	// — so the whole deployment comes down with one command. Origins that are
	// offline (unreachable) are reported as orphans for a manual gopher-uninstall.
	maybeTeardownOrigins(filepath.Join(cfg.dataDir, "gopher.db"), *skipPrompts, *keepOrigins)

	fmt.Println("Uninstalling Gopher service...")

	systemctlPath, _ := exec.LookPath("systemctl")

	if systemctlPath != "" {
		runCommandBestEffort(systemctlPath, "stop", cfg.serviceName)
		runCommandBestEffort(systemctlPath, "disable", cfg.serviceName)
	}

	// Undo the firewall takeover: remove gopher's iptables chains/jumps and reset
	// the INPUT/FORWARD policy to ACCEPT. No-op if gopher never took over the
	// firewall, so an operator-managed firewall is left untouched.
	service.TeardownFirewall(os.Stdout)

	// Caddy + rathole are bundled into the gopher binary and supervised as child
	// processes — there is no separate service, package, or binary to uninstall.
	// Stopping gopher (above) already stopped them, and removing the install dir
	// below deletes the extracted /opt/gopher/bin/{caddy,rathole}. The runtime
	// always goes with gopher; the only decision left is their *config* under
	// /etc/gopher.
	//
	// Gopher-managed entries are removed no matter what — the only decision is
	// whether the operator's own config (custom Caddy site blocks, custom
	// rathole services) survives. So the prompt is phrased around THEIR
	// content, and only asked when such content actually exists: an
	// all-Gopher /etc/gopher has nothing worth preserving and is removed
	// without a question. Default keeps their config (each file is backed up
	// to *.gopher-backup first) so a hand-rolled setup survives standalone.
	removeConfig := *skipPrompts
	if !*skipPrompts {
		customCaddy, customRathole := detectCustomConfig()
		if customCaddy || customRathole {
			var found []string
			if customCaddy {
				found = append(found, "Caddy site blocks")
			}
			if customRathole {
				found = append(found, "rathole services")
			}
			var err error
			removeConfig, err = promptYesNo(fmt.Sprintf(
				"Found your own %s under /etc/gopher. Delete them too? Default keeps them; Gopher-managed config is removed either way. [y/N]: ",
				strings.Join(found, " and ")))
			if err != nil {
				return fmt.Errorf("failed reading config removal confirmation: %w", err)
			}
		} else {
			fmt.Println("No custom config found under /etc/gopher — removing Gopher-managed config.")
			removeConfig = true
		}
	}

	var configSummary string
	if removeConfig {
		if err := removeCaddyCompletely(); err != nil {
			return err
		}
		if err := removeRatholeCompletely(); err != nil {
			return err
		}
		configSummary = "Caddy + rathole config removed entirely (/etc/gopher)"
	} else {
		if err := resetCaddyManagedConfig(); err != nil {
			return err
		}
		if err := resetRatholeManagedConfig(); err != nil {
			return err
		}
		configSummary = "Caddy + rathole config kept: custom blocks preserved, Gopher-managed entries stripped (originals backed up to *.gopher-backup)"
	}

	servicePath := filepath.Join("/etc/systemd/system", cfg.serviceName+".service")
	serviceRemoved, err := removeFileIfExists(servicePath)
	if err != nil {
		return fmt.Errorf("failed to remove service unit: %w", err)
	}
	if serviceRemoved && systemctlPath != "" {
		if err := runCommand("systemctl daemon-reload", systemctlPath, "daemon-reload"); err != nil {
			return err
		}
	}

	sudoersPath := filepath.Join("/etc/sudoers.d", cfg.user)
	if _, err := removeFileIfExists(sudoersPath); err != nil {
		return fmt.Errorf("failed to remove sudoers file: %w", err)
	}
	// Also remove any bootstrap sudoers files (gopher-<username>) left by the
	// passwordless-sudo setup so no elevated privileges remain after uninstall.
	if err := removeBootstrapSudoers(); err != nil {
		return fmt.Errorf("failed to remove bootstrap sudoers files: %w", err)
	}

	// Remove the fail2ban jail + filter the wizard installed. These are purely
	// Gopher-managed (like the sudoers and the unit), so they go unconditionally;
	// reload fail2ban so it drops the now-absent jail. All best-effort — fail2ban
	// may not be installed.
	fail2banRemoved := false
	for _, f := range []string{"/etc/fail2ban/jail.d/gopher.conf", "/etc/fail2ban/filter.d/gopher-auth.conf"} {
		if removed, _ := removeFileIfExists(f); removed {
			fail2banRemoved = true
		}
	}
	if fail2banRemoved {
		if f2bPath, err := exec.LookPath("fail2ban-client"); err == nil {
			runCommandBestEffort(f2bPath, "reload")
		}
	}

	// The fail2ban PACKAGE is a system-level side effect: the wizard installs it
	// when absent, but post-uninstall it still protects the rest of the box
	// (distro defaults ban ssh brute-force). So it's a prompt, defaulting to
	// keep — and deliberately NOT included in --skip-prompts' "remove
	// everything", since silently stripping ssh protection in an unattended run
	// is a worse failure mode than leaving a useful package behind.
	f2bSummary := ""
	if _, err := exec.LookPath("fail2ban-client"); err == nil && !*skipPrompts {
		removeF2b, err := promptYesNo("Remove the fail2ban package? Gopher may have installed it, but it also protects sshd on this box. Default keeps it. [y/N]: ")
		if err != nil {
			return fmt.Errorf("failed reading fail2ban removal confirmation: %w", err)
		}
		if removeF2b {
			if systemctlPath != "" {
				runCommandBestEffort(systemctlPath, "disable", "--now", "fail2ban")
			}
			if aptPath, lookErr := exec.LookPath("apt-get"); lookErr == nil {
				runCommandBestEffort(aptPath, "purge", "-y", "-qq", "fail2ban")
				f2bSummary = "fail2ban package removed"
			} else if dnfPath, lookErr := exec.LookPath("dnf"); lookErr == nil {
				runCommandBestEffort(dnfPath, "remove", "-y", "-q", "fail2ban")
				f2bSummary = "fail2ban package removed"
			} else if yumPath, lookErr := exec.LookPath("yum"); lookErr == nil {
				runCommandBestEffort(yumPath, "remove", "-y", "-q", "fail2ban")
				f2bSummary = "fail2ban package removed"
			} else {
				fmt.Println("  no known package manager found — remove fail2ban manually")
				f2bSummary = "fail2ban kept: no known package manager — remove manually"
			}
		} else {
			f2bSummary = "fail2ban package kept (Gopher jail + filter removed)"
		}
	}

	targetBinary := filepath.Join(cfg.installDir, "gopher")
	if _, err := removeFileIfExists(targetBinary); err != nil {
		return fmt.Errorf("failed to remove installed binary: %w", err)
	}
	// RemoveAll, not Remove: the supervisor extracts the bundled caddy + rathole
	// (+ .version stamps) into <installDir>/bin, so the dir is non-empty and a
	// plain os.Remove would fail with ENOTEMPTY and leave the extracted edge
	// binaries behind. Guard the path the same way the data-dir removal does.
	if err := ensureSafeRemovalPath(cfg.installDir); err == nil {
		_ = os.RemoveAll(cfg.installDir)
	}

	// Data dir (database, certs, state) is preserved by default on a bare
	// uninstall, so uninstall-to-reinstall doesn't silently wipe your setup.
	// --skip-prompts removes it; --keep-data always preserves it.
	dataSummary := fmt.Sprintf("Data kept: %s", cfg.dataDir)
	removeData := false
	switch {
	case *keepData:
		removeData = false
	case *skipPrompts:
		removeData = true
	default:
		var err error
		removeData, err = promptYesNo(fmt.Sprintf("Remove Gopher data (database, certs, state) at %q? This is irreversible. [y/N]: ", cfg.dataDir))
		if err != nil {
			return fmt.Errorf("failed reading data removal confirmation: %w", err)
		}
	}
	if removeData {
		if err := ensureSafeRemovalPath(cfg.dataDir); err != nil {
			return fmt.Errorf("refusing to remove data dir: %w", err)
		}
		if err := os.RemoveAll(cfg.dataDir); err != nil {
			return fmt.Errorf("failed to remove data dir: %w", err)
		}
		dataSummary = fmt.Sprintf("Data removed: %s", cfg.dataDir)
	}

	// The install creates two system users: the service user (cfg.user, e.g.
	// "gopher") and the privilege-free jumpbox user ("gopher-jump"). Both are
	// Gopher's footprint, so a single decision covers them — keeping a dangling
	// gopher-jump after the rest is gone was a leftover we hit in testing.
	userSummary := fmt.Sprintf("System user not found: %s", cfg.user)
	if systemUserExists(cfg.user) || systemUserExists(defaultJumpboxUser) {
		removeUser := *skipPrompts
		if !*skipPrompts {
			var err error
			removeUser, err = promptYesNo(fmt.Sprintf("Remove system users %q and %q? [y/N]: ", cfg.user, defaultJumpboxUser))
			if err != nil {
				return fmt.Errorf("failed reading user removal confirmation: %w", err)
			}
		}
		if removeUser {
			var removed []string
			for _, u := range []string{cfg.user, defaultJumpboxUser} {
				if !systemUserExists(u) {
					continue
				}
				if err := removeSystemUser(u); err != nil {
					return err
				}
				removed = append(removed, u)
			}
			if len(removed) > 0 {
				userSummary = "System users removed: " + strings.Join(removed, ", ")
			}
		} else {
			userSummary = fmt.Sprintf("System users kept: %s, %s", cfg.user, defaultJumpboxUser)
		}
	}

	fmt.Println("Uninstall complete.")
	fmt.Printf("Service unit removed: %s\n", servicePath)
	fmt.Printf("Sudoers removed: %s\n", sudoersPath)
	fmt.Printf("Binary removed: %s\n", targetBinary)
	fmt.Println(dataSummary)
	fmt.Println(configSummary)
	if f2bSummary != "" {
		fmt.Println(f2bSummary)
	}
	fmt.Println(userSummary)
	return nil
}

// maybeTeardownOrigins uninstalls the agent + tunnel client on every reachable
// origin before the edge itself is removed. It's best-effort: origins that are
// offline (or otherwise unreachable through the still-up rathole tunnels) are
// reported as orphans for a manual `gopher-uninstall` on the box. Skipped with
// --keep-origins; auto-confirmed with --skip-prompts.
func maybeTeardownOrigins(dbPath string, skipPrompts, keepOrigins bool) {
	if keepOrigins {
		return
	}
	// Best-effort DB open; if there's no DB (fresh/already-removed) there's
	// nothing to tear down.
	if err := db.Initialize(dbPath); err != nil {
		return
	}
	machines, err := db.GetMachines()
	if err != nil {
		fmt.Printf("  WARN: could not list machines to tear down: %v\n", err)
		return
	}
	var withAgent []db.Machine
	for _, m := range machines {
		if m.AgentRemotePort > 0 { // has an agent back-channel we can reach
			withAgent = append(withAgent, m)
		}
	}
	if len(withAgent) == 0 {
		return
	}

	if !skipPrompts {
		ok, err := promptYesNo(fmt.Sprintf("Uninstall the %d connected origin machine(s) too? Offline ones will be left orphaned. [y/N]: ", len(withAgent)))
		if err != nil || !ok {
			fmt.Printf("  Leaving %d origin machine(s) installed — run `sudo gopher-uninstall` on each to remove them.\n", len(withAgent))
			return
		}
	}

	var reached, orphaned []string
	for i := range withAgent {
		m := withAgent[i]
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := service.NewAgentClient(&m).Uninstall(ctx)
		cancel()
		if err != nil {
			orphaned = append(orphaned, m.Name)
		} else {
			reached = append(reached, m.Name)
		}
	}
	if len(reached) > 0 {
		fmt.Printf("  Tore down %d origin(s): %s\n", len(reached), strings.Join(reached, ", "))
	}
	if len(orphaned) > 0 {
		fmt.Printf("  Could NOT reach %d origin(s) (offline?) — run `sudo gopher-uninstall` on each: %s\n", len(orphaned), strings.Join(orphaned, ", "))
	}
}

func removeFileIfExists(path string) (bool, error) {
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func ensureSafeRemovalPath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("unsafe path %q: must be absolute", path)
	}
	cleaned := filepath.Clean(path)
	if cleaned == "/" || cleaned == "." || cleaned == "" {
		return fmt.Errorf("unsafe path %q", path)
	}
	return nil
}

func runCommandBestEffort(name string, args ...string) {
	cmd := exec.Command(name, args...)
	_ = cmd.Run()
}

// removeBootstrapSudoers removes any sudoers files in /etc/sudoers.d that were
// created by the gopher bootstrap flow (prefixed with "gopher-").
func removeBootstrapSudoers() error {
	entries, err := os.ReadDir("/etc/sudoers.d")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, "gopher-") && isSafeSudoersName(name) {
			path := filepath.Join("/etc/sudoers.d", name)
			if _, err := removeFileIfExists(path); err != nil {
				return fmt.Errorf("failed to remove bootstrap sudoers %s: %w", path, err)
			}
		}
	}
	return nil
}

// isSafeSudoersName reports whether name consists only of characters that are
// safe in a sudoers filename (alphanumeric, hyphen, underscore).
func isSafeSudoersName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func promptYesNo(prompt string) (bool, error) {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return isYesResponse(response), nil
}

func isYesResponse(response string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(response))
	return trimmed == "y" || trimmed == "yes"
}

func systemUserExists(username string) bool {
	return exec.Command("id", "-u", username).Run() == nil
}

func removeSystemUser(username string) error {
	if username == "root" {
		return errors.New("refusing to remove root user")
	}
	userdelPath, err := exec.LookPath("userdel")
	if err != nil {
		return fmt.Errorf("userdel not found: %w", err)
	}
	// -r removes the user's home dir too. Both gopher and gopher-jump are created
	// with -m (materialized homes: /opt/gopher and /var/lib/gopher-jump), and the
	// jumpbox home holds .ssh/authorized_keys with every managed key — without -r
	// that whole tree (keys included) is orphaned after a full uninstall.
	return runCommand("remove system user "+username, userdelPath, "-r", username)
}
