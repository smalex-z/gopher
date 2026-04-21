package service

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/db"
	sshpkg "github.com/smalex-z/gopher/internal/ssh"
)

// dashboardPort is the port Gopher's HTTP server is listening on.
// Set once at startup via SetDashboardPort; defaults to 4321.
var dashboardPort = 4321

// SetDashboardPort stores the runtime listen port so firewall and Caddy config
// functions can reference it without hardcoding.
func SetDashboardPort(port int) {
	dashboardPort = port
}


// LocalServiceStatus is returned by GET /api/local/status.
type LocalServiceStatus struct {
	CaddyInstalled       bool   `json:"caddy_installed"`
	CaddyActive          string `json:"caddy_active"`
	RatholeInstalled     bool   `json:"rathole_installed"`
	RatholeActive        string `json:"rathole_active"`
	Domain               string `json:"domain"`
	ServerHost           string `json:"server_host"`
	LocalSetupDone       bool   `json:"local_setup_done"`
	HasInstallPermission bool   `json:"has_install_permission"`
	SSHPublicKey         string `json:"ssh_public_key"`
	// FirewallMode is the persisted firewall strategy: "gopher", "manual", "none", or "" (not configured).
	FirewallMode         string `json:"firewall_mode"`
	// DashboardPrivate is true when the dashboard port is restricted to localhost (Caddy-only access).
	DashboardPrivate     bool   `json:"dashboard_private"`
	// DashboardPort is the port Gopher's HTTP server listens on.
	DashboardPort        int    `json:"dashboard_port"`
	// OSUser is the OS username Gopher runs as (e.g. "ubuntu"). Used to pre-fill
	// the VPS jump-host username in SSH jumpbox commands.
	OSUser               string `json:"os_user"`
	// Fail2banSetupDone is true once fail2ban has been installed and configured
	// by Gopher. Used to prompt existing installs to run the fail2ban setup step.
	Fail2banSetupDone    bool   `json:"fail2ban_setup_done"`
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
	osUser := ""
	if u, err := user.Current(); err == nil {
		osUser = u.Username
	}
	status := &LocalServiceStatus{
		CaddyInstalled:       isCommandAvailable("caddy"),
		CaddyActive:          systemctlStatus("caddy"),
		RatholeInstalled:     isCommandAvailable("rathole"),
		RatholeActive:        systemctlStatus("rathole-server"),
		Domain:               settings.Domain,
		ServerHost:           settings.ServerHost,
		LocalSetupDone:       settings.LocalSetupDone,
		HasInstallPermission: hasInstallPermission(),
		FirewallMode:         settings.FirewallMode,
		DashboardPrivate:     settings.DashboardPrivate,
		DashboardPort:        dashboardPort,
		OSUser:               osUser,
		Fail2banSetupDone:    settings.Fail2banSetupDone,
	}
	if key, kerr := db.GetDefaultSSHKey(); kerr == nil {
		status.SSHPublicKey = key.PublicKey
	}
	return status, nil
}

// ListSSHKeys returns all stored SSH key records (private keys excluded).
func (s *LocalSetupService) ListSSHKeys() ([]db.SSHKey, error) {
	return db.GetSSHKeys()
}

// GenerateSSHKey generates a new RSA 4096-bit key pair, stores it, and optionally sets it as default.
func (s *LocalSetupService) GenerateSSHKey(name string, setDefault bool) (*db.SSHKey, error) {
	privKey, pubKey, err := sshpkg.GenerateRSAKeypair()
	if err != nil {
		return nil, fmt.Errorf("failed to generate SSH keypair: %w", err)
	}
	return s.storeSSHKey(name, privKey, pubKey, setDefault)
}

// AddSSHKey validates an uploaded key pair and stores it.
func (s *LocalSetupService) AddSSHKey(name, privateKey, publicKey string, setDefault bool) (*db.SSHKey, error) {
	if err := sshpkg.ValidateKeyPair(privateKey, publicKey); err != nil {
		return nil, err
	}
	return s.storeSSHKey(name, privateKey, publicKey, setDefault)
}

func (s *LocalSetupService) storeSSHKey(name, privKey, pubKey string, setDefault bool) (*db.SSHKey, error) {
	key := &db.SSHKey{
		ID:         shortToken(),
		Name:       name,
		PublicKey:  pubKey,
		PrivateKey: privKey,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := db.CreateSSHKey(key); err != nil {
		return nil, err
	}
	// First key ever, or explicitly requested — set as default.
	count, _ := db.CountSSHKeys()
	if setDefault || count == 1 {
		if err := db.SetDefaultSSHKey(key.ID); err != nil {
			return nil, err
		}
		key.IsDefault = true
	}
	if err := addToAuthorizedKeys(pubKey); err != nil {
		fmt.Printf("WARN: could not add key to authorized_keys: %v\n", err)
	}
	return key, nil
}

// DeleteSSHKey refuses if machines still reference the key.
func (s *LocalSetupService) DeleteSSHKey(id string) error {
	n, err := db.CountMachinesUsingKey(id)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%d machine(s) still use this key; reassign them first", n)
	}
	key, err := db.GetSSHKey(id)
	if err != nil {
		return err
	}
	if err := db.DeleteSSHKeyByID(id); err != nil {
		return err
	}
	if err := removeFromAuthorizedKeys(key.PublicKey); err != nil {
		fmt.Printf("WARN: could not remove key from authorized_keys: %v\n", err)
	}
	return nil
}

// SetDefaultSSHKey marks the given key as the default for new bootstraps.
func (s *LocalSetupService) SetDefaultSSHKey(id string) error {
	if _, err := db.GetSSHKey(id); err != nil {
		return err
	}
	return db.SetDefaultSSHKey(id)
}

// DownloadSSHKey returns the private key PEM for the given key ID.
func (s *LocalSetupService) DownloadSSHKey(id string) (string, error) {
	key, err := db.GetSSHKey(id)
	if err != nil {
		return "", err
	}
	return key.PrivateKey, nil
}

// ReconcileAuthorizedKeys ensures all current Gopher public keys are present
// in ~/.ssh/authorized_keys so any Gopher key works as -i for both the
// jump-host hop and the destination machine.
func (s *LocalSetupService) ReconcileAuthorizedKeys() {
	keys, err := db.GetSSHKeys()
	if err != nil {
		fmt.Printf("WARN: reconcile authorized_keys: could not list keys: %v\n", err)
		return
	}
	for _, k := range keys {
		if err := addToAuthorizedKeys(k.PublicKey); err != nil {
			fmt.Printf("WARN: reconcile authorized_keys: could not add key %q: %v\n", k.Name, err)
		}
	}
}

// addToAuthorizedKeys idempotently appends pubKey to ~/.ssh/authorized_keys.
// Matching is on type+keydata only (comment field is ignored).
// Falls back to sudo for directory/file operations when running as a system user.
func addToAuthorizedKeys(pubKey string) error {
	path, err := authorizedKeysPath()
	if err != nil {
		return err
	}
	u, err := user.Current()
	if err != nil {
		return err
	}
	sshDir := filepath.Dir(path)

	// Ensure ~/.ssh exists with correct permissions.
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		if err2 := exec.Command("sudo", "mkdir", "-p", sshDir).Run(); err2 != nil { // #nosec G204
			return fmt.Errorf("mkdir %s: %w", sshDir, err2)
		}
		_ = exec.Command("sudo", "chmod", "700", sshDir).Run()                              // #nosec G204
		_ = exec.Command("sudo", "chown", u.Username+":"+u.Username, sshDir).Run()          // #nosec G204
	}

	// Read existing content.
	var existing []byte
	if data, rerr := os.ReadFile(path); rerr == nil {
		existing = data
	} else if out, rerr2 := exec.Command("sudo", "cat", path).Output(); rerr2 == nil { // #nosec G204
		existing = out
	}

	trimmed := strings.TrimSpace(pubKey)
	parts := strings.Fields(trimmed)
	if len(parts) < 2 {
		return fmt.Errorf("invalid public key format")
	}
	token := parts[0] + " " + parts[1]
	for _, line := range strings.Split(string(existing), "\n") {
		lp := strings.Fields(line)
		if len(lp) >= 2 && lp[0]+" "+lp[1] == token {
			return nil // already present
		}
	}

	content := string(existing)
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += trimmed + "\n"

	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		cmd := exec.Command("sudo", "tee", path) // #nosec G204
		cmd.Stdin = strings.NewReader(content)
		cmd.Stdout = io.Discard
		if err2 := cmd.Run(); err2 != nil {
			return err2
		}
		_ = exec.Command("sudo", "chmod", "600", path).Run()                             // #nosec G204
		_ = exec.Command("sudo", "chown", u.Username+":"+u.Username, path).Run()         // #nosec G204
	}
	return nil
}

// removeFromAuthorizedKeys removes the line matching pubKey from authorized_keys.
func removeFromAuthorizedKeys(pubKey string) error {
	path, err := authorizedKeysPath()
	if err != nil {
		return err
	}
	u, err := user.Current()
	if err != nil {
		return err
	}

	var existing []byte
	if data, rerr := os.ReadFile(path); rerr == nil {
		existing = data
	} else if out, rerr2 := exec.Command("sudo", "cat", path).Output(); rerr2 == nil { // #nosec G204
		existing = out
	} else {
		return nil // file doesn't exist, nothing to do
	}

	trimmed := strings.TrimSpace(pubKey)
	parts := strings.Fields(trimmed)
	if len(parts) < 2 {
		return nil
	}
	token := parts[0] + " " + parts[1]
	var kept []string
	for _, line := range strings.Split(string(existing), "\n") {
		lp := strings.Fields(line)
		if len(lp) >= 2 && lp[0]+" "+lp[1] == token {
			continue
		}
		kept = append(kept, line)
	}
	result := strings.Join(kept, "\n")
	if result != "" && !strings.HasSuffix(result, "\n") {
		result += "\n"
	}

	if err := os.WriteFile(path, []byte(result), 0600); err != nil {
		cmd := exec.Command("sudo", "tee", path) // #nosec G204
		cmd.Stdin = strings.NewReader(result)
		cmd.Stdout = io.Discard
		if err2 := cmd.Run(); err2 != nil {
			return err2
		}
		_ = exec.Command("sudo", "chmod", "600", path).Run()                             // #nosec G204
		_ = exec.Command("sudo", "chown", u.Username+":"+u.Username, path).Run()         // #nosec G204
	}
	return nil
}

func authorizedKeysPath() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return filepath.Join(u.HomeDir, ".ssh", "authorized_keys"), nil
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
	// Fall back to sudo: ensure directory exists, then write with tee.
	dir := filepath.Dir(path)
	if err := exec.Command("sudo", "mkdir", "-p", dir).Run(); err != nil { // #nosec G204
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}
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

// runLocalCmd executes a command, streaming stdout+stderr to logWriter and connecting
// stdin so sudo password prompts work correctly.
// For sudo commands, we connect to the real terminal directly to allow password prompts.
// Args are all hardcoded constants — no user input reaches this function.
func runLocalCmd(logWriter io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...) // #nosec G204
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
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

// migrateRatholeConfig restructures an existing rathole config for Gopher:
// the [server] base is preserved, all service entries are moved into the
// user-owned custom section so Gopher can safely write its managed entries
// above it on the first ReconcileServerConfig call.
func migrateRatholeConfig(existing string) string {
	const beginMarker = "# ===== BEGIN CUSTOM CONFIGURATION ====="
	const endMarker = "# ===== END CUSTOM CONFIGURATION ====="

	if strings.Contains(existing, beginMarker) {
		return existing
	}

	// Separate the [server] base table (direct keys only) from sub-tables / other sections.
	var baseLines, serviceLines []string
	inServerBase := false
	for _, line := range strings.Split(existing, "\n") {
		stripped := strings.TrimSpace(line)
		if stripped == "[server]" {
			inServerBase = true
			baseLines = append(baseLines, line)
			continue
		}
		if stripped != "" && stripped[0] == '[' {
			inServerBase = false
		}
		if inServerBase {
			baseLines = append(baseLines, line)
		} else {
			serviceLines = append(serviceLines, line)
		}
	}

	base := strings.TrimRight(strings.Join(baseLines, "\n"), "\n")
	services := strings.TrimSpace(strings.Join(serviceLines, "\n"))

	custom := beginMarker + "\n" +
		"# Your existing rathole service entries have been preserved here.\n" +
		"# Gopher will not modify this section.\n"
	if services != "" {
		custom += services + "\n"
	}
	custom += endMarker + "\n"
	return base + "\n\n" + custom
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

// SetDashboardPrivate persists the dashboard port visibility setting and applies
// the iptables rule for dashboardPort when in Gopher-managed firewall mode.
func (s *LocalSetupService) SetDashboardPrivate(private bool) error {
	settings, err := db.GetSettings()
	if err != nil {
		return err
	}
	settings.DashboardPrivate = private
	if err := db.SaveSettings(settings); err != nil {
		return err
	}
	ApplyDashboardPort(private)
	return nil
}
