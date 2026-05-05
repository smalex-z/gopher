package db

import (
	"fmt"
	"time"

	apperrors "github.com/smalex-z/gopher/internal/errors"
	"gorm.io/gorm"
)

// VPS Repository

func GetVPS() (*VPSConfig, error) {
	var vps VPSConfig
	if err := DB.First(&vps).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, &apperrors.NotFoundError{Resource: "vps_config", ID: "singleton"}
		}
		return nil, err
	}
	return &vps, nil
}

func CreateVPS(vps *VPSConfig) error {
	return DB.Create(vps).Error
}

func UpdateVPS(vps *VPSConfig) error {
	return DB.Save(vps).Error
}

func DeleteVPS(id string) error {
	return DB.Delete(&VPSConfig{}, "id = ?", id).Error
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
	return DB.Create(machine).Error
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
	return &m, nil
}

func UpdateMachine(machine *Machine) error {
	return DB.Save(machine).Error
}

// SetMachineStatus updates only Status / LastSeen / UpdatedAt — used by the
// monitor and the TCP-fallback health probe so concurrent writes from the
// agent path can't be clobbered by a stale full-record Save.
func SetMachineStatus(id, status string, lastSeen *time.Time) error {
	updates := map[string]any{
		"status":     status,
		"updated_at": time.Now(),
	}
	if lastSeen != nil {
		updates["last_seen"] = *lastSeen
	}
	return DB.Model(&Machine{}).Where("id = ?", id).Updates(updates).Error
}

// SetMachineAgentSeen marks the machine as having a healthy, reachable agent.
// Flips AgentInstalled true (so machines that bootstrapped with the agent
// inline are detected without a separate callback) and records the version.
// Status is also set to "connected" since reaching the agent proves end-to-end
// connectivity through the rathole back-channel.
func SetMachineAgentSeen(id, version string, when time.Time) error {
	updates := map[string]any{
		"agent_installed":     true,
		"agent_version":       version,
		"agent_last_seen":     when,
		"agent_install_error": "",
		"status":              "connected",
		"last_seen":           when,
		"updated_at":          when,
	}
	return DB.Model(&Machine{}).Where("id = ?", id).Updates(updates).Error
}

func DeleteMachine(id string) error {
	return DB.Delete(&Machine{}, "id = ?", id).Error
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
	return DB.Create(tunnel).Error
}

func UpdateTunnel(tunnel *Tunnel) error {
	return DB.Save(tunnel).Error
}

func DeleteTunnel(id string) error {
	return DB.Delete(&Tunnel{}, "id = ?", id).Error
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
// All callers — service tunnels, machine SSH tunnels, agent back-channels —
// allocate from the same pool, so they can never collide.
func NextRatholePort() (int, error) {
	used, err := allUsedPorts()
	if err != nil {
		return 0, err
	}
	port := 1024
	for used[port] {
		port++
	}
	return port, nil
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
		if err == nil {
			return key, nil
		}
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

func MarkTokenUsed(tokenID, machineID string) error {
	return DB.Model(&BootstrapToken{}).Where("id = ?", tokenID).Updates(map[string]interface{}{
		"used_at":    DB.NowFunc(),
		"machine_id": machineID,
	}).Error
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

// GetMigrationToken resolves a migration token to its target machine ID.
// Returns an error if the token is unknown or expired.
//
// The token is NOT consumed on first read — migrate.sh is idempotent and the
// operator may need to re-run it within the TTL window (rate-limited
// connection, retry after fixing a one-off network issue, etc.). After the
// TTL elapses the dashboard generates a new token.
func GetMigrationToken(token string) (*MigrationToken, error) {
	var mt MigrationToken
	if err := DB.First(&mt, "token = ?", token).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, &apperrors.NotFoundError{Resource: "migration_token", ID: token}
		}
		return nil, err
	}
	if time.Now().After(mt.ExpiresAt) {
		return nil, fmt.Errorf("migration token expired")
	}
	return &mt, nil
}

// PurgeExpiredMigrationTokens deletes rows whose ExpiresAt is in the past.
// Called periodically so the table doesn't grow forever.
func PurgeExpiredMigrationTokens() (int64, error) {
	res := DB.Where("expires_at < ?", time.Now()).Delete(&MigrationToken{})
	return res.RowsAffected, res.Error
}

// NextSSHTunnelPort returns the next available port for a machine SSH tunnel,
// guaranteed free across both machine SSH tunnels and service tunnels.
// Starts from 1024 (first non-privileged port) and finds the first gap.
func NextSSHTunnelPort() (int, error) {
	used, err := allUsedPorts()
	if err != nil {
		return 0, err
	}
	port := 1024
	for used[port] {
		port++
	}
	return port, nil
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

// PurgeBotSessions deletes all expired bot sessions.
func PurgeBotSessions() error {
	return DB.Where("expires_at < ?", gorm.Expr("datetime('now')")).Delete(&BotSession{}).Error
}

// ── TOTP Devices ─────────────────────────────────────────────────────────────

func GetTOTPDevices() ([]TOTPDevice, error) {
	var devices []TOTPDevice
	if err := DB.Order("created_at ASC").Find(&devices).Error; err != nil {
		return nil, err
	}
	return devices, nil
}

func GetTOTPDevice(id string) (*TOTPDevice, error) {
	var d TOTPDevice
	if err := DB.First(&d, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, &apperrors.NotFoundError{Resource: "totp_device", ID: id}
		}
		return nil, err
	}
	return &d, nil
}

func CreateTOTPDevice(d *TOTPDevice) error {
	return DB.Create(d).Error
}

func DeleteTOTPDevice(id string) error {
	return DB.Delete(&TOTPDevice{}, "id = ?", id).Error
}

func DeleteAllTOTPDevices() error {
	return DB.Exec("DELETE FROM totp_devices").Error
}

func CountTOTPDevices() (int64, error) {
	var count int64
	return count, DB.Model(&TOTPDevice{}).Count(&count).Error
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
func MachinesWithoutAgent() ([]Machine, error) {
	var rows []Machine
	if err := DB.Where("agent_installed = ? OR agent_installed IS NULL", false).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
