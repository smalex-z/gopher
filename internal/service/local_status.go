package service

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/smalex-z/gopher/internal/db"
	sshpkg "github.com/smalex-z/gopher/internal/ssh"
)

// LocalServiceStatus is returned by GET /api/local/status.
type LocalServiceStatus struct {
	CaddyInstalled       bool   `json:"caddy_installed"`
	CaddyActive          string `json:"caddy_active"`
	RatholeInstalled     bool   `json:"rathole_installed"`
	RatholeActive        string `json:"rathole_active"`
	Domain               string `json:"domain"`
	LocalSetupDone       bool   `json:"local_setup_done"`
	HasInstallPermission bool   `json:"has_install_permission"`
	SSHPublicKey         string `json:"ssh_public_key"`
}

type LocalSetupService struct {
	hub *LogHub
}

func NewLocalSetupService(hub *LogHub) *LocalSetupService {
	return &LocalSetupService{hub: hub}
}

func (s *LocalSetupService) Status() (*LocalServiceStatus, error) {
	settings, err := db.GetSettings()
	if err != nil {
		return nil, err
	}
	return &LocalServiceStatus{
		CaddyInstalled:       isCommandAvailable("caddy"),
		CaddyActive:          systemctlStatus("caddy"),
		RatholeInstalled:     isCommandAvailable("rathole"),
		RatholeActive:        systemctlStatus("rathole-server"),
		Domain:               settings.Domain,
		LocalSetupDone:       settings.LocalSetupDone,
		HasInstallPermission: hasInstallPermission(),
		SSHPublicKey:         settings.SSHPublicKey,
	}, nil
}

// GetSSHPrivateKey returns the server's SSH private key from DB.
func (s *LocalSetupService) GetSSHPrivateKey() (string, error) {
	settings, err := db.GetSettings()
	if err != nil {
		return "", err
	}
	return settings.SSHPrivateKey, nil
}

// GenerateSSHKey creates a new RSA 4096-bit keypair, stores it in settings, and returns the public key.
func (s *LocalSetupService) GenerateSSHKey() (string, error) {
	privKey, pubKey, err := sshpkg.GenerateRSAKeypair()
	if err != nil {
		return "", fmt.Errorf("failed to generate SSH keypair: %w", err)
	}
	settings, err := db.GetSettings()
	if err != nil {
		return "", err
	}
	settings.SSHPublicKey = pubKey
	settings.SSHPrivateKey = privKey
	if err := db.SaveSettings(settings); err != nil {
		return "", err
	}
	return pubKey, nil
}

// SetSSHKey validates that privateKey and publicKey form a matching pair, then stores them.
func (s *LocalSetupService) SetSSHKey(privateKey, publicKey string) error {
	if err := sshpkg.ValidateKeyPair(privateKey, publicKey); err != nil {
		return err
	}
	settings, err := db.GetSettings()
	if err != nil {
		return err
	}
	settings.SSHPublicKey = publicKey
	settings.SSHPrivateKey = privateKey
	return db.SaveSettings(settings)
}

// writeLocalFile writes content to path. If the direct write fails due to
// permissions, it falls back to `sudo tee` so the app can write to system
// directories without running as root itself.
func writeLocalFile(path, content string) error {
	// Try direct write first (works when running as root or owning the dir).
	if err := os.MkdirAll(filepath.Dir(path), 0755); err == nil {
		if err2 := os.WriteFile(path, []byte(content), 0644); err2 == nil {
			return nil
		}
	}
	// Fall back to sudo tee (requires passwordless sudo or NOPASSWD in sudoers).
	cmd := exec.Command("sudo", "tee", path) // #nosec G204
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = io.Discard
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// sudoMkdir creates a directory using sudo when os.MkdirAll fails.
func sudoMkdir(path string) error {
	if err := os.MkdirAll(path, 0755); err == nil {
		return nil
	}
	return exec.Command("sudo", "mkdir", "-p", path).Run() // #nosec G204
}

// runLocalCmd executes a command, streaming stdout+stderr to logWriter.
// Args are all hardcoded constants — no user input reaches this function.
func runLocalCmd(logWriter io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...) // #nosec G204
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	return cmd.Run()
}

// findCommandPath returns the absolute path to a binary, checking both $PATH
// and a set of well-known directories that may be absent from a restricted PATH
// (e.g. when the app runs as a systemd service).
func findCommandPath(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range []string{"/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func isCommandAvailable(name string) bool {
	return findCommandPath(name) != ""
}

// findExistingRatholeConfig looks for a rathole server config in common locations.
func findExistingRatholeConfig(logWriter io.Writer) string {
	candidates := []string{
		"/etc/rathole.toml",
		"/etc/rathole/rathole.toml",
		"/home/rathole/server.toml",
		"/opt/rathole/server.toml",
	}
	for _, p := range candidates {
		if data, err := os.ReadFile(p); err == nil {
			fmt.Fprintf(logWriter, "  Found existing rathole config at %s\n", p)
			return string(data)
		}
	}
	return ""
}

// migrateRatholeConfig appends custom-section markers to an existing config if
// they are not already present.
func migrateRatholeConfig(existing string) string {
	const beginMarker = "# ===== BEGIN CUSTOM CONFIGURATION ====="
	if strings.Contains(existing, beginMarker) {
		return existing
	}
	return strings.TrimRight(existing, "\n") + "\n\n" +
		"# ===== BEGIN CUSTOM CONFIGURATION =====\n" +
		"# Everything below this line will NOT be overwritten on deploy.\n" +
		"# Add any custom rathole service entries here.\n" +
		"# ===== END CUSTOM CONFIGURATION =====\n"
}

func systemctlStatus(service string) string {
	out, err := exec.Command("systemctl", "is-active", service).Output() // #nosec G204
	if err != nil {
		check, _ := exec.Command("systemctl", "status", service).CombinedOutput() // #nosec G204
		if strings.Contains(string(check), "could not be found") || strings.Contains(string(check), "not-found") {
			return "not-found"
		}
		s := strings.TrimSpace(string(out))
		if s == "" {
			return "inactive"
		}
		return s
	}
	return strings.TrimSpace(string(out))
}
