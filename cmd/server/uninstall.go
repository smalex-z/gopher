package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func runUninstall(args []string) error {
	cfg := installConfig{}
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.StringVar(&cfg.user, "user", defaultInstallUser, "system user that owns gopher sudoers entry")
	fs.StringVar(&cfg.installDir, "install-dir", defaultInstallDir, "installation directory")
	fs.StringVar(&cfg.dataDir, "data-dir", defaultDataDir, "data directory")
	fs.StringVar(&cfg.serviceName, "service-name", defaultServiceName, "systemd service name")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if os.Geteuid() != 0 {
		return runWithSudo("uninstall", args)
	}

	fmt.Println("Uninstalling Gopher service...")

	systemctlPath, _ := exec.LookPath("systemctl")

	if systemctlPath != "" {
		runCommandBestEffort(systemctlPath, "stop", cfg.serviceName)
		runCommandBestEffort(systemctlPath, "disable", cfg.serviceName)
	}

	resetCaddy, err := promptYesNo("Reset Caddyfile to remove Gopher-managed changes? [y/N]: ")
	if err != nil {
		return fmt.Errorf("failed reading Caddy reset confirmation: %w", err)
	}
	removeCaddy, err := promptYesNo("Remove Caddy completely (service, package, config)? [y/N]: ")
	if err != nil {
		return fmt.Errorf("failed reading Caddy removal confirmation: %w", err)
	}
	resetRathole, err := promptYesNo("Reset rathole server config to remove Gopher-managed changes? [y/N]: ")
	if err != nil {
		return fmt.Errorf("failed reading rathole reset confirmation: %w", err)
	}
	removeRathole, err := promptYesNo("Remove rathole completely (service, binary, config)? [y/N]: ")
	if err != nil {
		return fmt.Errorf("failed reading rathole removal confirmation: %w", err)
	}

	caddySummary := "Caddy left unchanged"
	if removeCaddy {
		if err := removeCaddyCompletely(); err != nil {
			return err
		}
		caddySummary = "Caddy removed completely"
	} else if resetCaddy {
		if err := resetCaddyManagedConfig(); err != nil {
			return err
		}
		caddySummary = "Caddyfile reset to remove Gopher-managed blocks"
	}

	ratholeSummary := "rathole left unchanged"
	if removeRathole {
		if err := removeRatholeCompletely(); err != nil {
			return err
		}
		ratholeSummary = "rathole removed completely"
	} else if resetRathole {
		if err := resetRatholeManagedConfig(); err != nil {
			return err
		}
		ratholeSummary = "rathole config reset to remove Gopher-managed blocks"
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

	targetBinary := filepath.Join(cfg.installDir, "gopher")
	if _, err := removeFileIfExists(targetBinary); err != nil {
		return fmt.Errorf("failed to remove installed binary: %w", err)
	}
	_ = os.Remove(cfg.installDir)

	if err := ensureSafeRemovalPath(cfg.dataDir); err != nil {
		return fmt.Errorf("refusing to remove data dir: %w", err)
	}
	if err := os.RemoveAll(cfg.dataDir); err != nil {
		return fmt.Errorf("failed to remove data dir: %w", err)
	}

	userSummary := fmt.Sprintf("System user not found: %s", cfg.user)
	if systemUserExists(cfg.user) {
		removeUser, err := promptYesNo(fmt.Sprintf("Remove system user %q? [y/N]: ", cfg.user))
		if err != nil {
			return fmt.Errorf("failed reading user removal confirmation: %w", err)
		}
		if removeUser {
			if err := removeSystemUser(cfg.user); err != nil {
				return err
			}
			userSummary = fmt.Sprintf("System user removed: %s", cfg.user)
		} else {
			userSummary = fmt.Sprintf("System user kept: %s", cfg.user)
		}
	}

	fmt.Println("Uninstall complete.")
	fmt.Printf("Service unit removed: %s\n", servicePath)
	fmt.Printf("Sudoers removed: %s\n", sudoersPath)
	fmt.Printf("Binary removed: %s\n", targetBinary)
	fmt.Printf("Data removed: %s\n", cfg.dataDir)
	fmt.Println(caddySummary)
	fmt.Println(ratholeSummary)
	fmt.Println(userSummary)
	return nil
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
	return runCommand("remove system user "+username, userdelPath, username)
}
