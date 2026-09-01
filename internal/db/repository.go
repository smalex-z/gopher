package db

import (
	"crypto/subtle"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	apperrors "github.com/smalex-z/gopher/internal/errors"
	"gorm.io/gorm"
)

// VPS Repository

// GetVPS returns the edge's identity (host + domain) synthesized from
// AppSettings. The old vps_configs table is no longer written (the remote-VPS
// management flow was removed), so reading it directly always 404'd — which the
// dashboard's jumpbox-command builder turned into a crash on installs without a
// jumpbox user. Deriving from settings keeps GET /api/vps and /api/status
// working. Returns NotFound only when the edge genuinely isn't configured yet.
func GetVPS() (*VPSConfig, error) {
	settings, err := GetSettings()
	if err != nil {
		return nil, err
	}
	host := settings.ServerHost
	if host == "" {
		host = settings.Domain
	}
	if host == "" {
		return nil, &apperrors.NotFoundError{Resource: "vps_config", ID: "singleton"}
	}
	return &VPSConfig{Host: host, Domain: settings.Domain}, nil
}

// Machine Repository

func GetMachines() ([]Machine, error) {
	var machines []Machine
	if err := DB.Preload("Tunnels").Find(&machines).Error; err != nil {
		return nil, err
	}
	return machines, nil
}

func GetMachine(id string) (*Machine, error) {
	var machine Machine
	if err := DB.Preload("Tunnels").First(&machine, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, &apperrors.NotFoundError{Resource: "machine", ID: id}
		}
		return nil, err
	}
	return &machine, nil
}

func CreateMachine(machine *Machine) error {
	err := DB.Create(machine).Error
	if err == nil {
		notifyStatusChange("machine", machine.ID, "", machine.Status)
	}
	return err
}

// GetMachineByAgentToken resolves a machine by its per-machine agent bearer
// token. Used by the self-delete endpoint, where the dying client
// authenticates with the same token its agent uses for the back-channel.
func GetMachineByAgentToken(token string) (*Machine, error) {
	if token == "" {
		return nil, &apperrors.NotFoundError{Resource: "machine", ID: "(empty token)"}
	}
	var m Machine
	if err := DB.Where("agent_token = ?", token).First(&m).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, &apperrors.NotFoundError{Resource: "machine", ID: "(by agent token)"}
		}
		return nil, err
	}
	// Re-check in constant time: the SQL lookup is what found the row, but a
	// bearer credential should never be accepted on a comparison whose timing
	// (or collation) we don't control.
	if subtle.ConstantTimeCompare([]byte(m.AgentToken), []byte(token)) != 1 {
		return nil, &apperrors.NotFoundError{Resource: "machine", ID: "(by agent token)"}
	}
	return &m, nil
}

func UpdateMachine(machine *Machine) error {
	return DB.Save(machine).Error
}

// SetMachineStatus updates only Status / LastSeen / UpdatedAt — used by the
// monitor and the TCP-fallback health probe so concurrent writes from the
// agent path can't be clobbered by a stale full-record Save.
// SetMachineConfigPushPending sets/clears the ConfigPushPending flag without
// touching any other field. Partial Update (vs. a full GORM Save) so we don't
// race the health/monitor writers that may have just updated agent fields.
func SetMachineConfigPushPending(id string, pending bool) error {
	return DB.Model(&Machine{}).Where("id = ?", id).Updates(map[string]any{
		"config_push_pending": pending,
		"updated_at":          time.Now(),
	}).Error
}

func SetMachineStatus(id, status string, lastSeen *time.Time) error {
	old := currentMachineStatus(id)
	updates := map[string]any{
		"status":     status,
		"updated_at": time.Now(),
	}
	if lastSeen != nil {
		updates["last_seen"] = *lastSeen
	}
	if status == "connected" {
		when := time.Now()
		if lastSeen != nil {
			when = *lastSeen
		}
		updates["connected_since"] = connectedSinceExpr(when)
	}
	err := DB.Model(&Machine{}).Where("id = ?", id).Updates(updates).Error
	if err == nil {
		notifyStatusChange("machine", id, old, status)
	}
	return err
}

// connectedSinceExpr stamps connected_since on the up-transition (or the first
// observation where it's still NULL) but preserves it across consecutive
// connected polls, so uptime is continuous rather than resetting every tick.
// SQLite evaluates the CASE against the pre-update row, so `status` here is the
// machine's status *before* this Update sets it to "connected".
func connectedSinceExpr(when time.Time) any {
	return gorm.Expr(
		"CASE WHEN status <> ? OR connected_since IS NULL THEN ? ELSE connected_since END",
		"connected", when)
}

// SetMachineAgentDegraded records the "agent up, rathole down" state: the
// agent answered but reports rathole-client is not active. We bump
// AgentLastSeen so the dashboard's agent badge stays green (the
// control-plane back-channel still works) and flip machine.Status to
// "offline" so the tunnels list / network map don't keep claiming the
// machine can serve traffic.
func SetMachineAgentDegraded(id, version string, when time.Time) error {
	old := currentMachineStatus(id)
	updates := map[string]any{
		"agent_installed":     true,
		"agent_version":       version,
		"agent_last_seen":     when,
		"agent_install_error": "",
		"status":              "offline",
		"updated_at":          when,
	}
	err := DB.Model(&Machine{}).Where("id = ?", id).Updates(updates).Error
	if err == nil {
		notifyStatusChange("machine", id, old, "offline")
	}
	return err
}

// SetMachineAgentSeen marks the machine as having a healthy, reachable agent.
// Flips AgentInstalled true (so machines that bootstrapped with the agent
// inline are detected without a separate callback) and records the version.
// Status is also set to "connected" since reaching the agent proves end-to-end
// connectivity through the rathole back-channel.
func SetMachineAgentSeen(id, version string, when time.Time) error {
	old := currentMachineStatus(id)
	updates := map[string]any{
		"agent_installed":     true,
		"agent_version":       version,
		"agent_last_seen":     when,
		"agent_install_error": "",
		"status":              "connected",
		"last_seen":           when,
		"connected_since":     connectedSinceExpr(when),
		"updated_at":          when,
	}
	err := DB.Model(&Machine{}).Where("id = ?", id).Updates(updates).Error
	if err == nil {
		notifyStatusChange("machine", id, old, "connected")
	}
	return err
}

// SetMachineAgentOutdated flags (or clears) a machine whose agent needs an
// upgrade — reachable-but-older or pre-gRPC skew. Partial update so it doesn't
// clobber concurrent status writes.
func SetMachineAgentOutdated(id string, outdated bool) error {
	return DB.Model(&Machine{}).Where("id = ?", id).Updates(map[string]any{
		"agent_outdated": outdated,
		"updated_at":     time.Now(),
	}).Error
}

func DeleteMachine(id string) error {
	err := DB.Delete(&Machine{}, "id = ?", id).Error
	if err == nil {
		notifyStatusChange("machine", id, "", "deleted")
	}
	return err
}

// Dashboard Session Repository — see the DashboardSession model comment for
// why these are persisted (self-restarts must not log the operator out).

func CreateDashboardSession(tokenHash string, expiresAt time.Time) error {
	// Opportunistic sweep: expired rows are dead weight and this is the only
	// low-frequency write path, so no background reaper is needed.
	_ = DB.Where("expires_at < ?", time.Now()).Delete(&DashboardSession{}).Error
	return DB.Create(&DashboardSession{TokenHash: tokenHash, ExpiresAt: expiresAt}).Error
}

// GetDashboardSession returns nil (no error) when the session doesn't exist.
func GetDashboardSession(tokenHash string) (*DashboardSession, error) {
	var s DashboardSession
	if err := DB.First(&s, "token_hash = ?", tokenHash).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

func TouchDashboardSession(tokenHash string, expiresAt time.Time) error {
	return DB.Model(&DashboardSession{}).Where("token_hash = ?", tokenHash).
		Update("expires_at", expiresAt).Error
}

func DeleteDashboardSession(tokenHash string) error {
	return DB.Delete(&DashboardSession{}, "token_hash = ?", tokenHash).Error
}

// Tunnel Repository

func GetTunnels() ([]Tunnel, error) {
	var tunnels []Tunnel
	if err := DB.Find(&tunnels).Error; err != nil {
		return nil, err
	}
	return tunnels, nil
}

func GetTunnelsByMachine(machineID string) ([]Tunnel, error) {
	var tunnels []Tunnel
	if err := DB.Where("machine_id = ?", machineID).Find(&tunnels).Error; err != nil {
		return nil, err
	}
	return tunnels, nil
}

func GetTunnel(id string) (*Tunnel, error) {
	var tunnel Tunnel
	if err := DB.First(&tunnel, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, &apperrors.NotFoundError{Resource: "tunnel", ID: id}
		}
		return nil, err
	}
	return &tunnel, nil
}

func CreateTunnel(tunnel *Tunnel) error {
	err := DB.Create(tunnel).Error
	if err == nil {
		notifyStatusChange("tunnel", tunnel.ID, "", tunnel.Status)
	}
	return err
}

func UpdateTunnel(tunnel *Tunnel) error {
	return DB.Save(tunnel).Error
}

// SetTunnelStatus updates only the Status / UpdatedAt columns. Used by the
// monitor's per-tunnel probe so a concurrent operator edit (rename, port
// change, subdomain) on the same row can't be clobbered by the monitor's
// stale full-record DB.Save 30 seconds later. Same pattern as
// SetMachineStatus / SetMachineAgentSeen.
func SetTunnelStatus(id, status string) error {
	old := currentTunnelStatus(id)
	err := DB.Model(&Tunnel{}).Where("id = ?", id).Updates(map[string]any{
		"status":     status,
		"updated_at": time.Now(),
	}).Error
	if err == nil {
		notifyStatusChange("tunnel", id, old, status)
	}
	return err
}

// SetTunnelCaddyPending flips the provisioning flag without touching the
// rest of the row (same partial-update rationale as SetTunnelStatus). The
// clear fires a status notification so dashboards drop the "provisioning"
// presentation promptly instead of waiting out their poll interval.
func SetTunnelCaddyPending(id string, pending bool) error {
	err := DB.Model(&Tunnel{}).Where("id = ?", id).Updates(map[string]any{
		"caddy_pending": pending,
		"updated_at":    time.Now(),
	}).Error
	if err == nil && !pending {
		notifyStatusChange("tunnel", id, "provisioning", currentTunnelStatus(id))
	}
	return err
}

func DeleteTunnel(id string) error {
	err := DB.Delete(&Tunnel{}, "id = ?", id).Error
	if err == nil {
		notifyStatusChange("tunnel", id, "", "deleted")
	}
	return err
}

// allUsedPorts returns every port currently assigned across service tunnels,
// machine SSH tunnels, and gopher-agent rathole back-channels. Used by
// port-assignment and conflict-check functions so no table is blind to another.
func allUsedPorts() (map[int]bool, error) {
	used := make(map[int]bool)
	var tunnels []Tunnel
	if err := DB.Select("rathole_port").Find(&tunnels).Error; err != nil {
		return nil, err
	}
	for _, t := range tunnels {
		if t.RatholePort > 0 {
			used[t.RatholePort] = true
		}
	}
	var machines []Machine
	if err := DB.Select("tunnel_port", "agent_remote_port").Find(&machines).Error; err != nil {
		return nil, err
	}
	for _, m := range machines {
		if m.TunnelPort > 0 {
			used[m.TunnelPort] = true
		}
		if m.AgentRemotePort > 0 {
			used[m.AgentRemotePort] = true
		}
	}
	return used, nil
}

// NextRatholePort returns the next available port across all existing port
// assignments (service tunnels, machine SSH tunnels, agent back-channels).
// Starts from 1024 (first non-privileged port) and finds the first gap.
//
// `excluding` lets the caller mark additional ports as in-use that aren't
// in the DB yet — needed when allocating multiple ports in one transaction
// (bootstrap allocates an SSH tunnel port and an agent port together; the
// first allocation isn't persisted by the time the second one queries the
// DB, so without this both calls would return the same port).
// portAvailable reports whether a port is free to bind. It's a package var so
// tests can stub it for deterministic allocation.
var portAvailable = osPortAvailable

// PortAvailable reports whether a port is free to bind on the edge right now.
// Exposed so the explicit-rathole-port create path can apply the same live
// OS check the auto-allocator uses, rather than trusting the DB view alone —
// this is what catches a user-supplied port that's occupied by a core listener
// (rathole's own 2333 control channel, Caddy on 80/443, the dashboard, sshd) or
// any other process, without gopher hardcoding which ports those are.
func PortAvailable(port int) bool { return portAvailable(port) }

// osPortAvailable checks the OS, not just Gopher's DB: it tries to bind the port
// (TCP and UDP) on all interfaces. This catches ports occupied by anything on
// the edge that the DB view can't see — including Gopher's own services
// (22/80/443/2333/4321) and whatever else the operator happens to run. It's
// best-effort: a small TOCTOU window remains between this check and rathole's
// actual bind, but it turns "something's already listening there" from a silent
// rathole bind failure into a skipped port.
func osPortAvailable(port int) bool {
	addr := net.JoinHostPort("", strconv.Itoa(port))
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	_ = l.Close()
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return false
	}
	_ = pc.Close()
	return true
}

// NextRatholePort returns the lowest free edge port for a new tunnel. It starts
// at 1024 on purpose: the edge is a dedicated box and short, low port numbers
// are far easier to remember/type than 5-digit ones on the rare occasion an
// operator touches the raw port (a TCP tunnel, or `ssh localhost:<port>`). A
// port is "free" only if it's unused in Gopher's DB AND not currently bound by
// any process on the host.
func NextRatholePort(excluding ...int) (int, error) {
	used, err := allUsedPorts()
	if err != nil {
		return 0, err
	}
	for _, p := range excluding {
		if p > 0 {
			used[p] = true
		}
	}
	for port := 1024; port <= 65535; port++ {
		if used[port] || !portAvailable(port) {
			continue
		}
		return port, nil
	}
	return 0, fmt.Errorf("no free rathole port available in 1024-65535")
}

func CheckSubdomainExists(subdomain string) (bool, error) {
	var count int64
	if err := DB.Model(&Tunnel{}).Where("subdomain = ?", subdomain).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// CheckRatholePortExists returns true if port is already in use by any service
// tunnel or machine SSH tunnel.
func CheckRatholePortExists(port int) (bool, error) {
	var count int64
	if err := DB.Model(&Tunnel{}).Where("rathole_port = ?", port).Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	if err := DB.Model(&Machine{}).Where("tunnel_port = ?", port).Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	// Also reserve agent back-channel ports — allUsedPorts() (the auto-allocator)
	// excludes them, so the user-supplied-port path must too, or an explicit
	// rathole_port can collide with a machine's agent port → duplicate bind_addr.
	if err := DB.Model(&Machine{}).Where("agent_remote_port = ?", port).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func GetAllTunnelsForVPS() ([]Tunnel, error) {
	var tunnels []Tunnel
	if err := DB.Find(&tunnels).Error; err != nil {
		return nil, fmt.Errorf("failed to get tunnels: %w", err)
	}
	return tunnels, nil
}

// SSH Key Repository

func GetSSHKeys() ([]SSHKey, error) {
	var keys []SSHKey
	if err := DB.Order("created_at ASC").Find(&keys).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

func GetSSHKey(id string) (*SSHKey, error) {
	var key SSHKey
	if err := DB.First(&key, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, &apperrors.NotFoundError{Resource: "ssh_key", ID: id}
		}
		return nil, err
	}
	return &key, nil
}

func GetDefaultSSHKey() (*SSHKey, error) {
	var key SSHKey
	if err := DB.Where("is_default = ?", true).First(&key).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, &apperrors.NotFoundError{Resource: "ssh_key", ID: "default"}
		}
		return nil, err
	}
	return &key, nil
}

func CreateSSHKey(key *SSHKey) error {
	return DB.Create(key).Error
}

func UpdateSSHKey(key *SSHKey) error {
	return DB.Save(key).Error
}

func DeleteSSHKeyByID(id string) error {
	return DB.Delete(&SSHKey{}, "id = ?", id).Error
}

// BlankSSHPrivateKey clears the stored private key for a key while keeping the
// public key (and the row) intact. Column-level update so it can't touch other
// fields. Used by "delete private key" — the server can still hand the public
// key to authorized_keys / the jumpbox, it just no longer holds a secret that
// could SSH into origins.
func BlankSSHPrivateKey(id string) error {
	return DB.Model(&SSHKey{}).Where("id = ?", id).Update("private_key", "").Error
}

// SetSSHPrivateKey stores (or restores) the private half of an existing
// public-only key. Column-level update. The caller must have verified the
// private key matches the stored public key first.
func SetSSHPrivateKey(id, privateKey string) error {
	return DB.Model(&SSHKey{}).Where("id = ?", id).Update("private_key", privateKey).Error
}

func SetDefaultSSHKey(id string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&SSHKey{}).Where("is_default = ?", true).Update("is_default", false).Error; err != nil {
			return err
		}
		return tx.Model(&SSHKey{}).Where("id = ?", id).Update("is_default", true).Error
	})
}

func CountSSHKeys() (int64, error) {
	var count int64
	return count, DB.Model(&SSHKey{}).Count(&count).Error
}

func CountMachinesUsingKey(keyID string) (int64, error) {
	var count int64
	return count, DB.Model(&Machine{}).Where("ssh_key_id = ?", keyID).Count(&count).Error
}

func GetSSHKeyForMachine(machine *Machine) (*SSHKey, error) {
	if machine.SSHKeyID != "" {
		key, err := GetSSHKey(machine.SSHKeyID)
		if err != nil {
			// No silent fallback to the default key: dialing with a different
			// identity than the machine was provisioned with can't succeed and
			// masks the real problem (the assigned key row is gone). Surface it
			// so the operator reassigns a key instead.
			return nil, fmt.Errorf("machine's assigned SSH key %q not found — reassign a key to this machine: %w", machine.SSHKeyID, err)
		}
		return key, nil
	}
	return GetDefaultSSHKey()
}

// Bootstrap Token Repository

func CreateBootstrapToken(t *BootstrapToken) error {
	return DB.Create(t).Error
}

func GetBootstrapToken(token string) (*BootstrapToken, error) {
	var bt BootstrapToken
	if err := DB.Where("token = ?", token).First(&bt).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, &apperrors.NotFoundError{Resource: "bootstrap_token", ID: token}
		}
		return nil, err
	}
	return &bt, nil
}

// ClaimBootstrapToken atomically marks the token used and returns the row
// for the caller to read TunnelPort / SSHKeyID / PublicSSH from. The single
// conditional UPDATE collapses the prior read-then-mark sequence so two
// parallel Register requests with the same token can't both pass validation
// before either consumes the token — only the request whose UPDATE actually
// changes the row continues; the other gets NotFoundError.
//
// `expires_at > now` is part of the predicate so a token expiring between
// generation and use is rejected without a separate timestamp check.
func ClaimBootstrapToken(token string) (*BootstrapToken, error) {
	now := time.Now()
	res := DB.Model(&BootstrapToken{}).
		Where("token = ? AND used_at IS NULL AND expires_at > ?", token, now).
		Update("used_at", now)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, &apperrors.NotFoundError{Resource: "bootstrap_token", ID: token}
	}
	var bt BootstrapToken
	if err := DB.Where("token = ?", token).First(&bt).Error; err != nil {
		return nil, err
	}
	return &bt, nil
}

// BindBootstrapTokenToMachine fills in the machine_id on a token whose
// used_at was already set by ClaimBootstrapToken. Split from the claim so
// the claim is atomic without needing the machine row to exist yet.
func BindBootstrapTokenToMachine(tokenID, machineID string) error {
	return DB.Model(&BootstrapToken{}).Where("id = ?", tokenID).
		Update("machine_id", machineID).Error
}

// PurgeExpiredBootstrapTokens deletes rows whose ExpiresAt is in the past.
// Mirrors PurgeExpiredMigrationTokens so the table doesn't grow forever.
func PurgeExpiredBootstrapTokens() (int64, error) {
	res := DB.Where("expires_at < ?", time.Now()).Delete(&BootstrapToken{})
	return res.RowsAffected, res.Error
}

// CreateMigrationToken stores a new ephemeral token for an agent-install
// migration. Token is returned to the caller; caller embeds it in the
// curl-bash one-liner shown in the dashboard.
func CreateMigrationToken(token, machineID string, ttl time.Duration) error {
	mt := &MigrationToken{
		Token:     token,
		MachineID: machineID,
		ExpiresAt: time.Now().Add(ttl),
		CreatedAt: time.Now(),
	}
	return DB.Create(mt).Error
}

// ClaimMigrationToken atomically consumes a migration token and returns its
// target machine ID. Same single-conditional-UPDATE pattern as
// ClaimBootstrapToken: only the request whose UPDATE changes the row wins.
//
// Tokens used to be replayable for their whole TTL so migrate.sh re-runs
// were free — but POST /api/migrate returns the machine's agent and rathole
// credentials, so a leaked token (shell history, pasted command) was a 1-hour
// credential-disclosure window. A failed migrate.sh run now needs a fresh
// token from the dashboard (one click on Install agent).
func ClaimMigrationToken(token string) (*MigrationToken, error) {
	now := time.Now()
	res := DB.Model(&MigrationToken{}).
		Where("token = ? AND used_at IS NULL AND expires_at > ?", token, now).
		Update("used_at", now)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, &apperrors.NotFoundError{Resource: "migration_token", ID: token}
	}
	var mt MigrationToken
	if err := DB.First(&mt, "token = ?", token).Error; err != nil {
		return nil, err
	}
	return &mt, nil
}

// PurgeExpiredMigrationTokens deletes rows whose ExpiresAt is in the past.
// Called periodically so the table doesn't grow forever.
func PurgeExpiredMigrationTokens() (int64, error) {
	res := DB.Where("expires_at < ?", time.Now()).Delete(&MigrationToken{})
	return res.RowsAffected, res.Error
}

// App Settings Repository

func GetSettings() (*AppSettings, error) {
	var s AppSettings
	if err := DB.First(&s, "id = 'singleton'").Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return &AppSettings{ID: "singleton", IsSetup: false}, nil
		}
		return nil, err
	}
	return &s, nil
}

func SaveSettings(s *AppSettings) error {
	s.ID = "singleton"
	return DB.Save(s).Error
}

// MutateSettings runs fn against a freshly-loaded AppSettings inside a single
// SQLite transaction, then saves the (possibly-mutated) row. SQLite's
// BEGIN IMMEDIATE semantics serialize writers, so two concurrent callers
// can't both load v1, mutate disjoint fields, and stomp on each other's
// changes — the second caller waits, sees v1+A, applies its own change,
// and writes v1+A+B.
//
// Replaces the bare GetSettings → mutate → SaveSettings pattern that was
// used in 19 production sites and was racy whenever a background goroutine
// (Install, FirewallConfigure, SetupFail2ban) wrote a flag while an
// operator request edited a different field.
//
// fn must not call MutateSettings recursively (deadlock); pass everything
// it needs in via captured variables.
// MutateSettingsTx mutates AppSettings inside a transaction and exposes that
// transaction to the closure.
//
// CRITICAL: the connection pool is capped at ONE connection
// (SetMaxOpenConns(1), see db.go — the app relies on it for pragma
// persistence). That means any nested DB call the closure makes MUST go
// through the passed `tx`, never the global DB pool. A nested global-pool
// call blocks waiting for the single connection that this very transaction is
// already holding → permanent self-deadlock that freezes ALL database access,
// and therefore the entire server. This exact bug froze a production box: the
// 2FA-confirm path called db.CreateTOTPDevice (global pool) inside a
// MutateSettings transaction. Use the *Tx db helpers (GetTOTPDevicesTx,
// CreateTOTPDeviceTx, …) for any read or write inside fn.
func MutateSettingsTx(fn func(tx *gorm.DB, s *AppSettings) error) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var s AppSettings
		if err := tx.First(&s, "id = 'singleton'").Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				s = AppSettings{ID: "singleton"}
			} else {
				return err
			}
		}
		s.ID = "singleton"
		if err := fn(tx, &s); err != nil {
			return err
		}
		return tx.Save(&s).Error
	})
}

// MutateSettings mutates AppSettings in a transaction. The closure must make
// NO nested DB call on the global pool — with SetMaxOpenConns(1) that
// self-deadlocks (see MutateSettingsTx). If the closure needs a nested read or
// write, use MutateSettingsTx and route it through the tx.
func MutateSettings(fn func(*AppSettings) error) error {
	return MutateSettingsTx(func(_ *gorm.DB, s *AppSettings) error { return fn(s) })
}

// Firewall Rules Repository

func GetFirewallRules() ([]FirewallRule, error) {
	var rules []FirewallRule
	if err := DB.Order("created_at ASC").Find(&rules).Error; err != nil {
		return nil, err
	}
	return rules, nil
}

func GetFirewallRule(id string) (*FirewallRule, error) {
	var rule FirewallRule
	if err := DB.First(&rule, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, &apperrors.NotFoundError{Resource: "firewall_rule", ID: id}
		}
		return nil, err
	}
	return &rule, nil
}

func CreateFirewallRule(rule *FirewallRule) error {
	return DB.Create(rule).Error
}

func DeleteFirewallRule(id string) error {
	return DB.Delete(&FirewallRule{}, "id = ?", id).Error
}

// Bot Session Repository

// GetTunnelBySubdomain returns the tunnel with the given subdomain value.
func GetTunnelBySubdomain(subdomain string) (*Tunnel, error) {
	var t Tunnel
	if err := DB.Where("subdomain = ?", subdomain).First(&t).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, &apperrors.NotFoundError{Resource: "tunnel", ID: subdomain}
		}
		return nil, err
	}
	return &t, nil
}

// CreateBotSession persists a newly issued bot challenge session.
func CreateBotSession(s *BotSession) error {
	return DB.Create(s).Error
}

// ── Event Repository ─────────────────────────────────────────────────────────
//
// Producers should call RecordEvent for full control. LogEvent stays as a
// back-compat shim for the original create/delete activity feed — it derives
// severity/source/message from the kind so older call sites don't all need
// to be updated at once.

// KindDefault maps a known event kind to its default severity, source, and a
// message template (with one %s slot for ResourceName). Unknown kinds get
// info / system / kind-as-message.
type KindDefault struct {
	Severity        string
	Source          string
	MessageTemplate string
}

var kindDefaults = map[string]KindDefault{
	"machine_registered":     {"info", "machine", "Machine %s registered"},
	"machine_deleted":        {"info", "machine", "Machine %s deleted"},
	"machine_connected":      {"info", "machine", "Machine %s connected"},
	"machine_disconnected":   {"warn", "machine", "Machine %s disconnected"},
	"machine_degraded":       {"warn", "machine", "Machine %s rathole inactive"},
	"machine_recovered":      {"info", "machine", "Machine %s auto-recovered"},
	"agent_config_recovered": {"warn", "machine", "Machine %s recovered client config via dial-home"},
	"recovery_failed":        {"error", "machine", "Auto-recovery failed for machine %s"},
	"agent_unreachable":      {"warn", "machine", "Agent unreachable on machine %s"},
	"tunnel_created":         {"info", "tunnel", "Tunnel %s created"},
	"tunnel_deleted":         {"info", "tunnel", "Tunnel %s deleted"},
}

// LookupKindDefault returns the registered defaults for a kind, or a fallback
// (info / system / kind-as-message) for unknown kinds.
func LookupKindDefault(kind string) KindDefault {
	if d, ok := kindDefaults[kind]; ok {
		return d
	}
	return KindDefault{Severity: "info", Source: "system", MessageTemplate: kind}
}

func RecordEvent(e *Event) {
	if e.ID == "" {
		e.ID = randomID()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	if e.Severity == "" {
		e.Severity = "info"
	}
	if e.Source == "" {
		e.Source = "system"
	}
	if e.Actor == "" {
		e.Actor = "system"
	}
	_ = DB.Create(e).Error
}

// LogEvent is the simple shim for the original activity feed. New code should
// prefer RecordEvent so it can pass actor/IP/message/metadata explicitly.
func LogEvent(kind, resourceID, name string) {
	def := LookupKindDefault(kind)
	msg := def.MessageTemplate
	if name != "" && strings.Contains(def.MessageTemplate, "%s") {
		msg = fmt.Sprintf(def.MessageTemplate, name)
	}
	resourceType := def.Source
	if resourceType == "system" {
		resourceType = ""
	}
	RecordEvent(&Event{
		Severity:     def.Severity,
		Source:       def.Source,
		Kind:         kind,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		ResourceName: name,
		Message:      msg,
	})
}

// GetRecentEvents returns the most recent events (any source/severity).
// Used by the dashboard "recent activity" widget.
func GetRecentEvents(limit int) ([]Event, error) {
	var events []Event
	if err := DB.Order("created_at DESC").Limit(limit).Find(&events).Error; err != nil {
		return nil, err
	}
	return events, nil
}

// EventFilter narrows GetEvents queries. Empty fields mean "no filter on this
// dimension".
type EventFilter struct {
	Sources     []string // any of these (OR'd). Empty = all sources.
	Severity    string   // exact match: info | warn | error | critical
	MinSeverity string   // returns events at or above this severity
	ResourceID  string
	Search      string    // case-insensitive substring match on message, resource_name, kind
	Since       time.Time // CreatedAt >=
	Until       time.Time // CreatedAt <=
	Before      time.Time // cursor pagination — strictly < (use the oldest CreatedAt from the previous page)
	Limit       int       // 0 means default (200)

	// Source is a back-compat single-value form. Prefer Sources for new code.
	Source string
}

var severityOrder = map[string]int{
	"info":     0,
	"warn":     1,
	"error":    2,
	"critical": 3,
}

func GetEvents(f EventFilter) ([]Event, error) {
	q := DB.Model(&Event{})
	switch {
	case len(f.Sources) > 0:
		q = q.Where("source IN ?", f.Sources)
	case f.Source != "":
		q = q.Where("source = ?", f.Source)
	}
	if f.Severity != "" {
		q = q.Where("severity = ?", f.Severity)
	}
	if f.MinSeverity != "" {
		// Translate the threshold into the set of severities at or above it.
		// Cleaner than a stored numeric since we keep severity as a label.
		var allowed []string
		threshold := severityOrder[f.MinSeverity]
		for sev, ord := range severityOrder {
			if ord >= threshold {
				allowed = append(allowed, sev)
			}
		}
		q = q.Where("severity IN ?", allowed)
	}
	if f.ResourceID != "" {
		q = q.Where("resource_id = ?", f.ResourceID)
	}
	if f.Search != "" {
		// Case-insensitive substring search across the user-facing fields.
		// SQLite's LIKE is case-insensitive for ASCII by default.
		needle := "%" + strings.ToLower(f.Search) + "%"
		q = q.Where(
			"LOWER(message) LIKE ? OR LOWER(resource_name) LIKE ? OR LOWER(kind) LIKE ?",
			needle, needle, needle,
		)
	}
	if !f.Since.IsZero() {
		q = q.Where("created_at >= ?", f.Since)
	}
	if !f.Until.IsZero() {
		q = q.Where("created_at <= ?", f.Until)
	}
	if !f.Before.IsZero() {
		q = q.Where("created_at < ?", f.Before)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	q = q.Order("created_at DESC").Limit(limit)
	var events []Event
	if err := q.Find(&events).Error; err != nil {
		return nil, err
	}
	return events, nil
}

// CountEvents returns the total number of events matching the filter,
// ignoring Limit/Before. Used for pagination headers.
func CountEvents(f EventFilter) (int64, error) {
	q := DB.Model(&Event{})
	switch {
	case len(f.Sources) > 0:
		q = q.Where("source IN ?", f.Sources)
	case f.Source != "":
		q = q.Where("source = ?", f.Source)
	}
	if f.Severity != "" {
		q = q.Where("severity = ?", f.Severity)
	}
	if f.MinSeverity != "" {
		var allowed []string
		threshold := severityOrder[f.MinSeverity]
		for sev, ord := range severityOrder {
			if ord >= threshold {
				allowed = append(allowed, sev)
			}
		}
		q = q.Where("severity IN ?", allowed)
	}
	if f.ResourceID != "" {
		q = q.Where("resource_id = ?", f.ResourceID)
	}
	if f.Search != "" {
		needle := "%" + strings.ToLower(f.Search) + "%"
		q = q.Where(
			"LOWER(message) LIKE ? OR LOWER(resource_name) LIKE ? OR LOWER(kind) LIKE ?",
			needle, needle, needle,
		)
	}
	if !f.Since.IsZero() {
		q = q.Where("created_at >= ?", f.Since)
	}
	if !f.Until.IsZero() {
		q = q.Where("created_at <= ?", f.Until)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// PurgeBotSessions deletes all expired bot sessions.
func PurgeBotSessions() error {
	return DB.Where("expires_at < ?", gorm.Expr("datetime('now')")).Delete(&BotSession{}).Error
}

// ── TOTP Devices ─────────────────────────────────────────────────────────────

func GetTOTPDevices() ([]TOTPDevice, error) {
	return GetTOTPDevicesTx(DB)
}

func GetTOTPDevice(id string) (*TOTPDevice, error) {
	return GetTOTPDeviceTx(DB, id)
}

func CreateTOTPDevice(d *TOTPDevice) error {
	return CreateTOTPDeviceTx(DB, d)
}

func DeleteTOTPDevice(id string) error {
	return DeleteTOTPDeviceTx(DB, id)
}

func DeleteAllTOTPDevices() error {
	return DeleteAllTOTPDevicesTx(DB)
}

func CountTOTPDevices() (int64, error) {
	return CountTOTPDevicesTx(DB)
}

// *Tx variants operate on a caller-supplied *gorm.DB — either the global DB
// (the plain wrappers above) or an open transaction. Callers inside a
// MutateSettingsTx closure MUST use these with the tx; see MutateSettingsTx
// for why a global-pool call there deadlocks.

func GetTOTPDevicesTx(tx *gorm.DB) ([]TOTPDevice, error) {
	var devices []TOTPDevice
	if err := tx.Order("created_at ASC").Find(&devices).Error; err != nil {
		return nil, err
	}
	return devices, nil
}

func GetTOTPDeviceTx(tx *gorm.DB, id string) (*TOTPDevice, error) {
	var d TOTPDevice
	if err := tx.First(&d, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, &apperrors.NotFoundError{Resource: "totp_device", ID: id}
		}
		return nil, err
	}
	return &d, nil
}

func CreateTOTPDeviceTx(tx *gorm.DB, d *TOTPDevice) error {
	return tx.Create(d).Error
}

func DeleteTOTPDeviceTx(tx *gorm.DB, id string) error {
	return tx.Delete(&TOTPDevice{}, "id = ?", id).Error
}

func DeleteAllTOTPDevicesTx(tx *gorm.DB) error {
	return tx.Exec("DELETE FROM totp_devices").Error
}

func CountTOTPDevicesTx(tx *gorm.DB) (int64, error) {
	var count int64
	return count, tx.Model(&TOTPDevice{}).Count(&count).Error
}

func TouchTOTPDevice(id string) error {
	now := time.Now()
	return DB.Model(&TOTPDevice{}).Where("id = ?", id).Update("last_used_at", &now).Error
}

// ── Health checks ────────────────────────────────────────────────────────────

func RecordHealthCheck(c *HealthCheck) error {
	if c.ID == "" {
		c.ID = randomID()
	}
	if c.CheckedAt.IsZero() {
		c.CheckedAt = time.Now()
	}
	return DB.Create(c).Error
}

// GetRecentHealthChecks returns the newest N entries for the given subject.
func GetRecentHealthChecks(subject string, limit int) ([]HealthCheck, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows []HealthCheck
	if err := DB.Where("subject = ?", subject).Order("checked_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// PurgeHealthChecksBefore deletes rows older than `before`. Called by a janitor
// goroutine to keep the table from unbounded growth.
func PurgeHealthChecksBefore(before time.Time) (int64, error) {
	res := DB.Where("checked_at < ?", before).Delete(&HealthCheck{})
	return res.RowsAffected, res.Error
}

// HealthSummary aggregates the rolling window for the per-tunnel uptime % and
// the sparkline pulse-of-life on the dashboard. Window defaults to 24h when
// `since` is the zero value. UptimePercent is OK-count / total-count * 100,
// rounded to one decimal; nil when no checks exist yet (caller can render
// "—" instead of misleading "0%").
type HealthSummary struct {
	UptimePercent *float64      `json:"uptime_percent"`
	TotalChecks   int           `json:"total_checks"`
	OKChecks      int           `json:"ok_checks"`
	Recent        []HealthCheck `json:"recent"` // newest-first, capped at 30
	Latest        *HealthCheck  `json:"latest"`
}

// GetHealthSummary computes UptimePercent + a recent-checks slice for the
// given subject in one trip. Used by both /machines/{id}/health and the
// per-tunnel uptime column.
func GetHealthSummary(subject string, since time.Time, recentLimit int) (*HealthSummary, error) {
	if since.IsZero() {
		since = time.Now().Add(-24 * time.Hour)
	}
	if recentLimit <= 0 {
		recentLimit = 30
	}

	var totals struct {
		Total int64
		OK    int64
	}
	row := DB.Model(&HealthCheck{}).
		Select("COUNT(*) as total, SUM(CASE WHEN ok THEN 1 ELSE 0 END) as ok").
		Where("subject = ? AND checked_at >= ?", subject, since).
		Row()
	if err := row.Scan(&totals.Total, &totals.OK); err != nil {
		return nil, err
	}

	summary := &HealthSummary{
		TotalChecks: int(totals.Total),
		OKChecks:    int(totals.OK),
	}
	if totals.Total > 0 {
		pct := float64(totals.OK) / float64(totals.Total) * 100
		// One decimal place — enough resolution to spot 99.x% without
		// looking spuriously precise.
		pct = float64(int(pct*10+0.5)) / 10
		summary.UptimePercent = &pct
	}

	var recent []HealthCheck
	if err := DB.Where("subject = ?", subject).Order("checked_at DESC").Limit(recentLimit).Find(&recent).Error; err != nil {
		return nil, err
	}
	summary.Recent = recent
	if len(recent) > 0 {
		latest := recent[0]
		summary.Latest = &latest
	}
	return summary, nil
}

// LatestHealthCheck returns the most recent check for a subject, or nil if none.
func LatestHealthCheck(subject string) (*HealthCheck, error) {
	var row HealthCheck
	err := DB.Where("subject = ?", subject).Order("checked_at DESC").First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// MachinesWithoutAgent returns machines that haven't completed the agent install.
// Used by the dashboard banner to nudge users into the migration flow.
//
// Excludes machines that are <10 minutes old: bootstrap.sh installs the agent
// inline and the health service polls every 60s, so a brand-new machine
// legitimately has agent_installed=false for ~1-2 minutes. Showing the
// "needs migration" banner during that window misled operators into thinking
// the agent had failed when it was just still starting up. After 10 minutes
// without a successful agent poll, something's actually wrong and the
// banner is correct to surface it.
func MachinesWithoutAgent() ([]Machine, error) {
	const installGrace = 10 * time.Minute
	cutoff := time.Now().Add(-installGrace)

	var rows []Machine
	if err := DB.
		Where("(agent_installed = ? OR agent_installed IS NULL) AND created_at < ?", false, cutoff).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
