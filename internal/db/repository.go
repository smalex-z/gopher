package db

import (
	"fmt"
	"strings"
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
	updates := map[string]any{
		"status":     status,
		"updated_at": time.Now(),
	}
	if lastSeen != nil {
		updates["last_seen"] = *lastSeen
	}
	return DB.Model(&Machine{}).Where("id = ?", id).Updates(updates).Error
}

// SetMachineAgentDegraded records the "agent up, rathole down" state: the
// agent answered but reports rathole-client is not active. We bump
// AgentLastSeen so the dashboard's agent badge stays green (the
// control-plane back-channel still works) and flip machine.Status to
// "offline" so the tunnels list / network map don't keep claiming the
// machine can serve traffic.
func SetMachineAgentDegraded(id, version string, when time.Time) error {
	updates := map[string]any{
		"agent_installed":     true,
		"agent_version":       version,
		"agent_last_seen":     when,
		"agent_install_error": "",
		"status":              "offline",
		"updated_at":          when,
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

// SetTunnelStatus updates only the Status / UpdatedAt columns. Used by the
// monitor's per-tunnel probe so a concurrent operator edit (rename, port
// change, subdomain) on the same row can't be clobbered by the monitor's
// stale full-record DB.Save 30 seconds later. Same pattern as
// SetMachineStatus / SetMachineAgentSeen.
func SetTunnelStatus(id, status string) error {
	return DB.Model(&Tunnel{}).Where("id = ?", id).Updates(map[string]any{
		"status":     status,
		"updated_at": time.Now(),
	}).Error
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
// `excluding` lets the caller mark additional ports as in-use that aren't
// in the DB yet — needed when allocating multiple ports in one transaction
// (bootstrap allocates an SSH tunnel port and an agent port together; the
// first allocation isn't persisted by the time the second one queries the
// DB, so without this both calls would return the same port).
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
func MutateSettings(fn func(*AppSettings) error) error {
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
		if err := fn(&s); err != nil {
			return err
		}
		return tx.Save(&s).Error
	})
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
	"machine_registered":   {"info", "machine", "Machine %s registered"},
	"machine_deleted":      {"info", "machine", "Machine %s deleted"},
	"machine_connected":    {"info", "machine", "Machine %s connected"},
	"machine_disconnected": {"warn", "machine", "Machine %s disconnected"},
	"machine_degraded":     {"warn", "machine", "Machine %s rathole inactive"},
	"machine_recovered":    {"info", "machine", "Machine %s auto-recovered"},
	"recovery_failed":      {"error", "machine", "Auto-recovery failed for machine %s"},
	"agent_unreachable":    {"warn", "machine", "Agent unreachable on machine %s"},
	"tunnel_created":       {"info", "tunnel", "Tunnel %s created"},
	"tunnel_deleted":       {"info", "tunnel", "Tunnel %s deleted"},
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
	Sources      []string  // any of these (OR'd). Empty = all sources.
	Severity     string    // exact match: info | warn | error | critical
	MinSeverity  string    // returns events at or above this severity
	ResourceID   string
	Search       string    // case-insensitive substring match on message, resource_name, kind
	Since        time.Time // CreatedAt >=
	Until        time.Time // CreatedAt <=
	Before       time.Time // cursor pagination — strictly < (use the oldest CreatedAt from the previous page)
	Limit        int       // 0 means default (200)

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
