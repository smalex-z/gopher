package service

import (
	"fmt"
	"io"
	"log"
	"net"
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

// TunnelDialHost returns the host Gopher should dial to reach a machine's
// rathole tunnel port. Private SSH tunnels always bind to 127.0.0.1 so
// "localhost" is always correct. Public SSH tunnels bind to bind_ip (when set),
// so Gopher must dial bind_ip to reach them.
func TunnelDialHost(m *db.Machine) string {
	if m.PublicSSH {
		if settings, err := db.GetSettings(); err == nil && settings.BindIP != "" {
			return settings.BindIP
		}
	}
	return "localhost"
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
	// OSUser is the OS username Gopher runs as (e.g. "gopher"). Used for
	// path/ownership checks and as a fallback for jumpbox commands when
	// the dedicated jumpbox user isn't installed yet (legacy deployments).
	OSUser               string `json:"os_user"`
	// JumpboxUser is the dedicated, privilege-free user whose authorized_keys
	// holds Gopher-managed keys. Frontend pre-fills SSH jumpbox commands
	// with this. Empty string means the user hasn't been created yet — the
	// frontend should warn the operator to re-run `gopher install`.
	JumpboxUser          string `json:"jumpbox_user"`
	// Fail2banSetupDone is true once fail2ban has been installed and configured
	// by Gopher. Used to prompt existing installs to run the fail2ban setup step.
	Fail2banSetupDone    bool     `json:"fail2ban_setup_done"`
	// BindIP is the IP address Gopher binds public listeners to. Empty = 0.0.0.0.
	BindIP               string   `json:"bind_ip"`
	// HostIPs lists all non-loopback IPs detected on the host's network interfaces.
	// Used by the frontend to warn when the host has multiple IPs and BindIP is unset.
	HostIPs              []string `json:"host_ips"`
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
		JumpboxUser:          s.JumpboxUser(),
		Fail2banSetupDone:    settings.Fail2banSetupDone,
		BindIP:               settings.BindIP,
		HostIPs:              detectHostIPs(),
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

// jumpboxUsername is the dedicated, privilege-free system user whose
// ~/.ssh/authorized_keys holds Gopher-managed keys. Lookup-fail returns
// an empty string + false; callers should fall back loudly rather than
// silently writing to the dashboard's OS user (the historical, unsafe
// behaviour). Re-running the install path creates the user.
const jumpboxUsername = "gopher-jump"

// jumpboxKeyOptions wraps each authorized_keys line with OpenSSH options
// that lock the key to TCP-forwarding into local ports — exactly what the
// jumpbox flow needs (`ssh -J gopher-jump@vps -p <rathole_port> ...`) and
// nothing else.
//
// Layered semantics:
//   - `restrict` disables shell, X11, agent forwarding, env, ~/.ssh/rc,
//     PTY, AND port forwarding. It's the safe-by-default base.
//   - `port-forwarding` re-enables port forwarding specifically. This
//     IS required: `restrict,permitopen=...` alone leaves
//     permit_port_forwarding_flag at 0 and sshd denies the channel
//     before consulting the permitopen list (auth-options.c). The
//     symptom is "channel 0: open failed: administratively prohibited"
//     even though sshd accepts the publickey.
//   - `permitopen="127.0.0.1:*"` / `permitopen="localhost:*"` narrow
//     the now-enabled port forwarding down to localhost-only targets,
//     i.e. the rathole bind addresses operators legitimately reach via
//     the jumpbox flow.
//
// Net result: shell off, agent forwarding off, X11 off — but jumpbox
// forwards to localhost ports work.
const jumpboxKeyOptions = `restrict,port-forwarding,permitopen="127.0.0.1:*",permitopen="localhost:*"`

// jumpboxUserExists reports whether the dedicated jumpbox user is set up.
// During upgrades from pre-v0.1.0 deployments this returns false until the
// operator re-runs the installer; ReconcileAuthorizedKeys then loudly
// surfaces the gap so they know to upgrade.
func jumpboxUserExists() bool {
	_, err := user.Lookup(jumpboxUsername)
	return err == nil
}

// JumpboxUser returns the username SSH-jumpbox commands should target on
// the VPS, or an empty string when no jumpbox user is configured.
// Surfaced via /api/local/status so the frontend can build the correct
// `ssh -J <user>@<host>` command.
func (s *LocalSetupService) JumpboxUser() string {
	if jumpboxUserExists() {
		return jumpboxUsername
	}
	return ""
}

// jumpboxHomeDir matches the path the install command provisions the user
// with. Kept in sync with cmd/server/install.go's defaultJumpboxHomeDir.
const jumpboxHomeDir = "/var/lib/gopher-jump"

// EnsureJumpboxUser self-heals upgrades: when an existing dashboard binary
// is replaced (via the in-app updater, manual binary swap, or scripts/reinstall
// where the jumpbox-user step was skipped), the gopher-jump system user may
// not exist yet. The legacy fallback in ReconcileAuthorizedKeys keeps writing
// keys to the dashboard's OS user, which silently regresses the security
// posture and surprises operators ("why does the dashboard show
// gopher@router.example.com instead of gopher-jump@..."). This creates the
// user on startup if missing, so the next reconcile cycle picks it up
// automatically.
//
// Best-effort: failures are logged but don't block startup. The most likely
// failure mode is sudoers not granting useradd — patchSudoers in
// internal/service/update.go appends that line on every Apply(), so the next
// in-app update unblocks this path on legacy installs too.
func (s *LocalSetupService) EnsureJumpboxUser() {
	if jumpboxUserExists() {
		return
	}
	useraddPath, err := exec.LookPath("useradd")
	if err != nil {
		log.Printf("WARN: jumpbox self-heal: useradd not found in PATH; gopher-jump user not created — re-run `gopher install` to migrate")
		return
	}
	args := append(privilegedCmdPrefix(), useraddPath, "--system", "--shell", "/usr/sbin/nologin", "--home-dir", jumpboxHomeDir, "--create-home", jumpboxUsername)
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		log.Printf("WARN: jumpbox self-heal: could not create %s user via sudo — re-run `gopher install` to migrate (%v: %s)", jumpboxUsername, err, strings.TrimSpace(string(out)))
		return
	}
	log.Printf("Created jumpbox system user %s (home %s) — authorized_keys will reconcile on next sweep", jumpboxUsername, jumpboxHomeDir)
}

// ReconcileAuthorizedKeys ensures every Gopher-managed key is present in
// the jumpbox user's ~/.ssh/authorized_keys (with restrict + permitopen
// options) and removed from the dashboard OS user's authorized_keys —
// auto-migrating pre-v0.1.0 deployments where the keys were dangerously
// installed into the dashboard user's account.
//
// On installs that haven't created the jumpbox user yet (legacy deployments
// that haven't re-run install/reinstall after upgrading) this falls back
// to the historical behaviour with a loud warning. Operators who see the
// warning should re-run the install command — that creates the user and
// the next reconcile auto-migrates.
func (s *LocalSetupService) ReconcileAuthorizedKeys() {
	keys, err := db.GetSSHKeys()
	if err != nil {
		fmt.Printf("WARN: reconcile authorized_keys: could not list keys: %v\n", err)
		return
	}

	if !jumpboxUserExists() {
		fmt.Printf("WARN: jumpbox user %q not present; falling back to dashboard user's authorized_keys (insecure). Re-run `gopher install` to create the jumpbox user and migrate keys.\n", jumpboxUsername)
		// Fall back to the historical (unsafe) path so existing operator
		// workflows don't break before they upgrade.
		dashboardUser, err := user.Current()
		if err != nil {
			fmt.Printf("WARN: reconcile authorized_keys (fallback): %v\n", err)
			return
		}
		for _, k := range keys {
			if err := addToAuthorizedKeysFor(dashboardUser.Username, k.PublicKey, ""); err != nil {
				fmt.Printf("WARN: reconcile authorized_keys: %q: %v\n", k.Name, err)
			}
		}
		return
	}

	// Write to gopher-jump's authorized_keys with the restrict line.
	for _, k := range keys {
		if err := addToAuthorizedKeysFor(jumpboxUsername, k.PublicKey, jumpboxKeyOptions); err != nil {
			fmt.Printf("WARN: reconcile authorized_keys (jumpbox): %q: %v\n", k.Name, err)
		}
	}

	// Migrate: remove every Gopher-managed key from the dashboard user's
	// authorized_keys. Matching is on type+keydata, so any pre-existing
	// non-Gopher keys (operator-added) survive untouched.
	if dashboardUser, err := user.Current(); err == nil && dashboardUser.Username != jumpboxUsername {
		for _, k := range keys {
			if err := removeFromAuthorizedKeysFor(dashboardUser.Username, k.PublicKey); err != nil {
				fmt.Printf("WARN: scrub dashboard authorized_keys: %q: %v\n", k.Name, err)
			}
		}
	}
}

// addToAuthorizedKeysFor idempotently appends pubKey to the named user's
// ~/.ssh/authorized_keys. Matching is on type+keydata only (options +
// comment fields are ignored), so re-running with different options
// updates the line without duplicating it. Falls back to sudo for
// directory/file operations when the dashboard user can't write to the
// target homedir directly.
func addToAuthorizedKeysFor(username, pubKey, options string) error {
	path, err := authorizedKeysPathFor(username)
	if err != nil {
		return err
	}
	sshDir := filepath.Dir(path)

	// Ensure ~/.ssh exists with correct permissions.
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		if err2 := exec.Command("sudo", "mkdir", "-p", sshDir).Run(); err2 != nil { // #nosec G204
			return fmt.Errorf("mkdir %s: %w", sshDir, err2)
		}
		_ = exec.Command("sudo", "chmod", "700", sshDir).Run()                       // #nosec G204
		_ = exec.Command("sudo", "chown", username+":"+username, sshDir).Run()       // #nosec G204
	}

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

	// authorized_keys lines may have option prefixes; locate the keydata
	// inside each line so we match on type+blob and don't duplicate.
	keydataToken, err := keydataToken(trimmed)
	if err != nil {
		return err
	}

	// If a line for the same key already exists, replace it in place — that
	// way upgrading from no-options to restrict/permitopen rewrites the
	// existing line without leaving an unrestricted duplicate behind.
	var rebuilt []string
	updated := false
	wantLine := authorizedKeysLine(trimmed, options)
	for _, line := range strings.Split(string(existing), "\n") {
		if line == "" {
			continue
		}
		if tok, ok := keydataTokenFromLine(line); ok && tok == keydataToken {
			rebuilt = append(rebuilt, wantLine)
			updated = true
			continue
		}
		rebuilt = append(rebuilt, line)
	}
	if !updated {
		rebuilt = append(rebuilt, wantLine)
	}
	content := strings.Join(rebuilt, "\n") + "\n"

	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		cmd := exec.Command("sudo", "tee", path) // #nosec G204
		cmd.Stdin = strings.NewReader(content)
		cmd.Stdout = io.Discard
		if err2 := cmd.Run(); err2 != nil {
			return err2
		}
		_ = exec.Command("sudo", "chmod", "600", path).Run()                       // #nosec G204
		_ = exec.Command("sudo", "chown", username+":"+username, path).Run()       // #nosec G204
	}
	return nil
}

// removeFromAuthorizedKeysFor removes the line matching pubKey (by
// type+keydata, ignoring options and comment) from the named user's
// authorized_keys. Used during migration to scrub the dashboard user's
// file after writing the same key under the jumpbox user.
func removeFromAuthorizedKeysFor(username, pubKey string) error {
	path, err := authorizedKeysPathFor(username)
	if err != nil {
		return err
	}

	var existing []byte
	if data, rerr := os.ReadFile(path); rerr == nil {
		existing = data
	} else if out, rerr2 := exec.Command("sudo", "cat", path).Output(); rerr2 == nil { // #nosec G204
		existing = out
	} else {
		return nil
	}

	keydataToken, err := keydataToken(pubKey)
	if err != nil {
		return nil
	}

	var kept []string
	for _, line := range strings.Split(string(existing), "\n") {
		if tok, ok := keydataTokenFromLine(line); ok && tok == keydataToken {
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
		_ = exec.Command("sudo", "chmod", "600", path).Run()                       // #nosec G204
		_ = exec.Command("sudo", "chown", username+":"+username, path).Run()       // #nosec G204
	}
	return nil
}

// addToAuthorizedKeys is the legacy helper kept so existing call sites in
// bootstrap.go / CreateSSHKey continue to compile. It now routes to the
// jumpbox user when configured and falls back to the historical
// dashboard-user path with a loud warning otherwise.
func addToAuthorizedKeys(pubKey string) error {
	if jumpboxUserExists() {
		return addToAuthorizedKeysFor(jumpboxUsername, pubKey, jumpboxKeyOptions)
	}
	fmt.Printf("WARN: jumpbox user %q not present; adding key to dashboard user's authorized_keys (insecure). Re-run `gopher install`.\n", jumpboxUsername)
	u, err := user.Current()
	if err != nil {
		return err
	}
	return addToAuthorizedKeysFor(u.Username, pubKey, "")
}

func removeFromAuthorizedKeys(pubKey string) error {
	if jumpboxUserExists() {
		_ = removeFromAuthorizedKeysFor(jumpboxUsername, pubKey)
	}
	if u, err := user.Current(); err == nil {
		_ = removeFromAuthorizedKeysFor(u.Username, pubKey)
	}
	return nil
}

// authorizedKeysPathFor resolves the absolute path of the named user's
// authorized_keys file via the system passwd database. We don't synthesise
// "/home/<u>" because system users (gopher-jump) commonly live elsewhere
// (/var/lib/gopher-jump per install.go).
func authorizedKeysPathFor(username string) (string, error) {
	u, err := user.Lookup(username)
	if err != nil {
		return "", fmt.Errorf("lookup user %q: %w", username, err)
	}
	if u.HomeDir == "" {
		return "", fmt.Errorf("user %q has no home directory in passwd", username)
	}
	return filepath.Join(u.HomeDir, ".ssh", "authorized_keys"), nil
}

// keydataToken returns the "type keydata" pair from a public key line —
// the portion that identifies a unique key regardless of options or
// comment. Returns ("", error) on malformed input.
func keydataToken(line string) (string, error) {
	parts := strings.Fields(strings.TrimSpace(line))
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid public key format")
	}
	return parts[0] + " " + parts[1], nil
}

// keydataTokenFromLine extracts type+keydata from an authorized_keys line
// that may begin with options. Options are a comma-separated list with
// optional `key="quoted value"` pairs that may themselves contain commas;
// we walk the string honouring quotes and stop at the first whitespace
// outside quotes. Whatever follows is the standard `type keydata [comment]`
// format. Returns ("", false) for blank or comment-only lines.
func keydataTokenFromLine(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", false
	}
	// If the line starts with a known key type, there are no options.
	if isKnownKeyType(firstField(trimmed)) {
		tok, err := keydataToken(trimmed)
		return tok, err == nil
	}
	// Otherwise parse past an options list.
	rest := skipOptionsList(trimmed)
	if rest == "" {
		return "", false
	}
	tok, err := keydataToken(rest)
	return tok, err == nil
}

// authorizedKeysLine assembles the final line we'll write — `options key
// comment` when options is non-empty, else just `key comment`.
func authorizedKeysLine(pubKey, options string) string {
	trimmed := strings.TrimSpace(pubKey)
	if options == "" {
		return trimmed
	}
	return options + " " + trimmed
}

// isKnownKeyType returns true when s is one of the public-key algorithm
// identifiers OpenSSH writes at the start of an authorized_keys line.
// The minimal set covers everything modern Gopher generates — extend as
// needed if we ever support more.
func isKnownKeyType(s string) bool {
	switch s {
	case "ssh-rsa", "ssh-dss", "ssh-ed25519", "ssh-ed25519-cert-v01@openssh.com",
		"ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521",
		"sk-ecdsa-sha2-nistp256@openssh.com", "sk-ssh-ed25519@openssh.com":
		return true
	}
	return false
}

// firstField returns s up to the first whitespace, or all of s if none.
func firstField(s string) string {
	for i, r := range s {
		if r == ' ' || r == '\t' {
			return s[:i]
		}
	}
	return s
}

// skipOptionsList returns the remainder of an authorized_keys line after
// the leading options list. Whitespace inside quoted values doesn't
// terminate the list — `permitopen="127.0.0.1:*",restrict` is valid.
func skipOptionsList(line string) string {
	inQuotes := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch c {
		case '"':
			inQuotes = !inQuotes
		case ' ', '\t':
			if !inQuotes {
				return strings.TrimLeft(line[i:], " \t")
			}
		}
	}
	return ""
}

// writeLocalFile atomically writes content to path. Writes to a sibling
// `<path>.tmp.<nano>` first, then renames into place — readers (Caddy reload,
// fail2ban-client reload) only ever see a fully-written file. A power loss
// or kill mid-write leaves the temp file behind for the next run to
// clobber, never a truncated config that fails to parse.
//
// IMPORTANT: do NOT use this for files watched by an inotify-based hot
// reloader (rathole's notify watcher in particular). The rename(2) replaces
// the target's inode, which silently invalidates the watcher's IN_MODIFY
// subscription. Use writeLocalFileInPlace for those — see the rathole
// server.toml call site in local_rathole.go.
//
// Two backends: direct os.WriteFile + os.Rename when the process owns the
// directory, sudo-tee + sudo-mv when it doesn't.
func writeLocalFile(path, content string) error {
	// Try direct atomic write first.
	if err := os.MkdirAll(filepath.Dir(path), 0755); err == nil {
		tmp := fmt.Sprintf("%s.tmp.%d", path, time.Now().UnixNano())
		if err2 := os.WriteFile(tmp, []byte(content), 0644); err2 == nil {
			if rerr := os.Rename(tmp, path); rerr == nil {
				return nil
			}
			// Rename can fail across filesystems or when the dest exists
			// with different ownership. Clean up the temp before falling
			// through to sudo so we don't leak it.
			_ = os.Remove(tmp)
		}
	}
	// Fall back to sudo. tee writes to a temp path we own, mv installs
	// atomically. mv inside the same directory is rename(2) on Linux.
	dir := filepath.Dir(path)
	if err := exec.Command("sudo", "mkdir", "-p", dir).Run(); err != nil { // #nosec G204
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, time.Now().UnixNano())
	cmd := exec.Command("sudo", "tee", tmp) // #nosec G204
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = io.Discard
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	if err := exec.Command("sudo", "mv", tmp, path).Run(); err != nil { // #nosec G204
		_ = exec.Command("sudo", "rm", "-f", tmp).Run() // #nosec G204
		return fmt.Errorf("atomic install of %s failed: %w", path, err)
	}
	return nil
}

// writeLocalFileInPlace truncates and rewrites path, preserving the inode.
// Use for files watched by inotify hot-reloaders (rathole 0.5's notify
// watcher) — `writeLocalFile`'s rename(2) silently invalidates the
// watcher's subscription, so a reload via systemctl is the only way the
// new config takes effect, and that drops every active rathole tunnel.
//
// The trade-off is a narrow torn-write window: a kill between truncate
// and the final fsync leaves a partial file. For rathole's small server
// config (a few KB) this is sub-millisecond and the pre-migrate DB
// backup gives a recovery path; for client.toml on remote machines the
// agent / SSH push lands the full content in a single write syscall.
//
// `tee /path` (without redirect to a temp) opens with O_TRUNC|O_WRONLY|
// O_CREATE which is exactly what we want: same inode, new content,
// inotify fires IN_MODIFY, rathole hot-reloads without dropping clients.
func writeLocalFileInPlace(path, content string) error {
	// Direct write first (process owns the file).
	if err := os.MkdirAll(filepath.Dir(path), 0755); err == nil {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err == nil {
			if _, werr := f.WriteString(content); werr == nil {
				if serr := f.Sync(); serr == nil {
					_ = f.Close()
					return nil
				}
			}
			_ = f.Close()
		}
	}
	// Sudo fallback. `sudo tee <path>` truncates and writes in place,
	// preserving the inode. Discard stdout (tee echoes the input).
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

// detectHostIPs returns all non-loopback unicast IPv4 addresses on the host's
// network interfaces. Used to warn when the host has multiple public IPs and
// BindIP is unset (Gopher would then listen on all of them).
func detectHostIPs() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var ips []string
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			ips = append(ips, ip4.String())
		}
	}
	return ips
}

// SetBindIP persists the bind IP, immediately reconciles rathole + Caddy, and
// schedules a self-restart so the HTTP server rebinds (0.0.0.0 ↔ 127.0.0.1).
func (s *LocalSetupService) SetBindIP(bindIP string) error {
	if bindIP != "" {
		if net.ParseIP(bindIP) == nil {
			return fmt.Errorf("invalid IP address: %q", bindIP)
		}
	}
	if err := db.MutateSettings(func(s *db.AppSettings) error {
		s.BindIP = bindIP
		return nil
	}); err != nil {
		return err
	}
	settings, err := db.GetSettings()
	if err != nil {
		return err
	}
	_ = s.ReconcileServerConfig()
	s.ReconcileMainCaddyfile()
	s.ReconcileRouterCaddyBlock()
	_ = s.reconcileAllTunnelCaddyBlocks(settings)
	go func() {
		time.Sleep(500 * time.Millisecond)
		_ = exec.Command("sudo", "systemctl", "restart", "gopher").Run() // #nosec G204
	}()
	return nil
}

// reconcileAllTunnelCaddyBlocks rewrites every managed tunnel Caddy file with
// the current BindIP and reloads Caddy once.
func (s *LocalSetupService) reconcileAllTunnelCaddyBlocks(settings *db.AppSettings) error {
	if settings.Domain == "" {
		return nil
	}
	tunnels, err := db.GetTunnels()
	if err != nil {
		return err
	}
	reloaded := false
	for _, t := range tunnels {
		if t.Subdomain == "" || t.Private || t.Transport == "udp" {
			continue
		}
		block := buildTunnelCaddyBlock(t.Subdomain, settings.Domain, t.RatholePort, t.NoTLS, t.BotProtectionEnabled, settings.BindIP, t.TLSSkipVerify)
		path := managedTunnelCaddyPath(t.ID)
		if err := writeLocalFile(path, block); err != nil {
			return fmt.Errorf("failed to rewrite Caddy block for tunnel %s: %w", t.ID, err)
		}
		reloaded = true
	}
	if reloaded {
		if err := systemctlReload("caddy"); err != nil {
			log.Printf("reconcile all tunnel caddy: caddy reload failed: %v", err)
		}
	}
	return nil
}

// ReconcileAllTunnelCaddyBlocks is the public entry point used by startup
// and by /api/local/reconcile. Regenerates every tunnel's Caddy file from
// the DB so missing files (deleted out-of-band, or wrongly cleaned up by
// the orphan sweep on a previous boot) get recreated. Idempotent: writes
// are no-ops when the on-disk content already matches.
//
// Returns nil silently when there's no Domain configured (skipCaddy
// installs) — there's nothing to reconcile in that case.
func (s *LocalSetupService) ReconcileAllTunnelCaddyBlocks() error {
	settings, err := db.GetSettings()
	if err != nil {
		return err
	}
	return s.reconcileAllTunnelCaddyBlocks(settings)
}

// SetDashboardPrivate persists the dashboard port visibility setting and applies
// the iptables rule for dashboardPort when in Gopher-managed firewall mode.
func (s *LocalSetupService) SetDashboardPrivate(private bool) error {
	if err := db.MutateSettings(func(s *db.AppSettings) error {
		s.DashboardPrivate = private
		return nil
	}); err != nil {
		return err
	}
	ApplyDashboardPort(private)
	return nil
}
