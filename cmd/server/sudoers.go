package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

func ensurePasswordlessSudoForCurrentUser() error {
	if os.Geteuid() == 0 {
		return nil
	}
	if !stdinIsInteractive() {
		// Non-interactive contexts (e.g. systemd service) cannot answer sudo
		// prompts and should not attempt bootstrap.
		return nil
	}

	if isPasswordlessSudoEnabled() {
		return nil
	}

	if _, err := exec.LookPath("sudo"); err != nil {
		return fmt.Errorf("sudo not found: %w", err)
	}

	username, err := currentNonRootUsername()
	if err != nil {
		return err
	}

	sudoersPath := filepath.Join("/etc/sudoers.d", "gopher-"+sanitizeSudoersName(username))
	if err := writeSudoersWithSudo(sudoersPath, buildBootstrapSudoers(username)); err != nil {
		return err
	}

	if err := runCommand("set sudoers file permissions", "sudo", "chmod", "0440", sudoersPath); err != nil {
		return err
	}
	if err := runCommand("validate sudoers file", "sudo", "visudo", "-c", "-f", sudoersPath); err != nil {
		_ = exec.Command("sudo", "rm", "-f", sudoersPath).Run() // #nosec G204
		return err
	}

	if !isPasswordlessSudoEnabled() {
		return fmt.Errorf("passwordless sudo is still unavailable for user %s", username)
	}

	return nil
}

// buildBootstrapSudoers generates limited passwordless sudo rules for machine bootstrap.
// Allows only the commands necessary for managing rathole-client and Caddy services.
func buildBootstrapSudoers(username string) string {
	pkgMgrLine := "/usr/bin/apt-get, /bin/apt-get"
	if p, err := exec.LookPath("dnf"); err == nil {
		pkgMgrLine = p
	} else if p, err := exec.LookPath("yum"); err == nil {
		pkgMgrLine = p
	}
	// No shells, no downloaders: a "narrow" list containing bash or curl is
	// root-equivalent. SETENV on the package manager lets the firewall
	// takeover pass DEBIAN_FRONTEND=noninteractive on the sudo command line.
	return fmt.Sprintf(`# Gopher rathole bootstrap - limited sudo access
%s ALL=(ALL:ALL) NOPASSWD: /bin/mkdir, /usr/bin/mkdir
%s ALL=(ALL:ALL) NOPASSWD: /bin/systemctl, /usr/bin/systemctl
%s ALL=(ALL:ALL) NOPASSWD: /bin/mv, /usr/bin/mv
%s ALL=(ALL:ALL) NOPASSWD: /bin/rm, /usr/bin/rm
%s ALL=(ALL:ALL) NOPASSWD: /usr/bin/chown, /bin/chown
%s ALL=(ALL:ALL) NOPASSWD: /usr/bin/tee, /bin/tee
%s ALL=(ALL:ALL) NOPASSWD: /bin/chmod, /usr/bin/chmod
%s ALL=(ALL:ALL) NOPASSWD:SETENV: `+pkgMgrLine+`
%s ALL=(ALL:ALL) NOPASSWD: /usr/bin/fail2ban-client, /usr/local/bin/fail2ban-client
`, username, username, username, username, username, username, username, username, username)
}

func runWithSudo(subcommand string, args []string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to resolve current executable: %w", err)
	}

	cmdArgs := append([]string{exePath, subcommand}, args...)
	cmd := exec.Command("sudo", cmdArgs...) // #nosec G204
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudo elevation failed: %w", err)
	}
	return nil
}

func writeSudoersWithSudo(path, content string) error {
	cmd := exec.Command("sudo", "tee", path) // #nosec G204
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = io.Discard
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to write sudoers file: %w (%s)", err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

func stdinIsInteractive() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	if (info.Mode() & os.ModeCharDevice) == 0 {
		return false
	}
	if target, err := os.Readlink("/proc/self/fd/0"); err == nil && target == "/dev/null" {
		return false
	}
	term := strings.TrimSpace(os.Getenv("TERM"))
	if term == "" || strings.EqualFold(term, "dumb") {
		return false
	}
	return true
}

func currentNonRootUsername() (string, error) {
	if sudoUser := strings.TrimSpace(os.Getenv("SUDO_USER")); sudoUser != "" && sudoUser != "root" {
		return sudoUser, nil
	}
	if envUser := strings.TrimSpace(os.Getenv("USER")); envUser != "" && envUser != "root" {
		return envUser, nil
	}

	currentUser, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("failed to resolve current user: %w", err)
	}
	username := strings.TrimSpace(filepath.Base(currentUser.Username))
	if username == "" || username == "root" {
		return "", fmt.Errorf("could not determine a non-root user for sudoers setup")
	}
	return username, nil
}

func sanitizeSudoersName(name string) string {
	if name == "" {
		return "user"
	}
	var builder strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			builder.WriteRune(r)
		} else {
			builder.WriteRune('_')
		}
	}
	if builder.Len() == 0 {
		return "user"
	}
	return builder.String()
}

func isPasswordlessSudoEnabled() bool {
	return exec.Command("sudo", "-n", "true").Run() == nil // #nosec G204
}
