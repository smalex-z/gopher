package db

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// randomID returns a 16-character hex string (8 bytes of entropy). Used as the
// primary key for rows where we don't have a natural ID handy (e.g. HealthCheck).
func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// BotSession records a browser that has passed the PoW challenge for a tunnel.
type BotSession struct {
	ID        string    `json:"id" gorm:"primaryKey"`
	TunnelID  string    `json:"tunnel_id" gorm:"index"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type VPSConfig struct {
	ID            string    `json:"id" gorm:"primaryKey"`
	Host          string    `json:"host"`
	Port          int       `json:"port"`
	Username      string    `json:"username"`
	PrivateKey    string    `json:"private_key"`
	Domain        string    `json:"domain"`
	SSHPublicKey  string    `json:"ssh_public_key"`
	SSHPrivateKey string    `json:"ssh_private_key,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Machine struct {
	ID              string     `json:"id" gorm:"primaryKey"`
	Name            string     `json:"name"`
	Host            string     `json:"host"`
	Port            int        `json:"port"`
	Username        string     `json:"username"`
	PrivateKey      string     `json:"private_key,omitempty"`
	TunnelPort      int        `json:"tunnel_port"`
	RatholeSSHToken string     `json:"rathole_ssh_token,omitempty"`
	SSHKeyID        string     `json:"ssh_key_id" gorm:"index"`
	PublicSSH       bool       `json:"public_ssh"`
	Status          string     `json:"status"`
	PublicIP        string     `json:"public_ip"`
	LastSeen        *time.Time `json:"last_seen"`
	// ConnectedSince is when the machine most recently transitioned to
	// "connected". Rendered as uptime while the machine is up; once it goes
	// offline the dashboard shows LastSeen instead.
	ConnectedSince *time.Time `json:"connected_since,omitempty"`
	// gopher-agent fields. AgentInstalled flips true once the machine has the
	// agent binary running and reachable; AgentLastSeen tracks the last
	// successful health poll; AgentInstallError stores the last failure reason
	// for the migration retry UI.
	AgentToken        string     `json:"-"`                             // bearer token shared with the agent
	AgentLocalPort    int        `json:"agent_local_port"`              // port the agent listens on (client side, default 4322)
	AgentRemotePort   int        `json:"agent_remote_port"`             // bind_addr port on the VPS for the agent rathole service
	AgentRatholeToken string     `json:"-"`                             // rathole token for the agent service
	AgentInstalled    bool       `json:"agent_installed"`               // true once install succeeded at least once
	AgentVersion      string     `json:"agent_version,omitempty"`       // version string returned by the agent's /version endpoint
	AgentLastSeen     *time.Time `json:"agent_last_seen,omitempty"`     // last successful agent poll
	AgentInstallError string     `json:"agent_install_error,omitempty"` // last install failure (cleared on success)
	// ConfigPushPending marks machines whose last attempted client.toml push
	// failed (offline, full disk, agent down). The health service retries the
	// push the next time the machine becomes reachable, then clears the flag.
	// Set by the noise migration's failure path; intended to be general — any
	// future push path that fails to land should set this rather than logging
	// and moving on.
	ConfigPushPending bool       `json:"config_push_pending,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	Tunnels           []Tunnel   `json:"tunnels,omitempty" gorm:"foreignKey:MachineID"`
}

// HealthCheck records the result of a single agent or tunnel probe. Used by
// the dashboard to surface recent failures and uptime.
type HealthCheck struct {
	ID        string    `json:"id" gorm:"primaryKey"`
	Subject   string    `json:"subject" gorm:"index"` // "machine:<id>" or "tunnel:<id>"
	CheckedAt time.Time `json:"checked_at" gorm:"index"`
	OK        bool      `json:"ok"`
	LatencyMS int       `json:"latency_ms"`
	ErrorMsg  string    `json:"error_msg,omitempty"`
	Recovered bool      `json:"recovered,omitempty"` // true when this check followed a successful auto-recovery
}

type Tunnel struct {
	ID           string    `json:"id" gorm:"primaryKey"`
	MachineID    string    `json:"machine_id"`
	Name         string    `json:"name"`
	Subdomain    string    `json:"subdomain"`
	LocalPort    int       `json:"local_port"`
	RatholePort  int       `json:"rathole_port"`
	RatholeToken string    `json:"rathole_token"`
	Protocol     string    `json:"protocol"`
	Transport    string    `json:"transport"`  // "tcp" (default) or "udp"
	NoTLS        bool      `json:"no_tls"`     // skip Caddy HTTPS; use plain http://
	Private      bool      `json:"private"`    // bind 127.0.0.1 (VPS-local only) instead of 0.0.0.0
	// Bot protection — opt-in per tunnel, HTTP subdomain tunnels only.
	BotProtectionEnabled bool   `json:"bot_protection_enabled"`
	BotProtectionTTL     int    `json:"bot_protection_ttl"`      // session TTL in seconds; 0 = default (86400)
	BotProtectionAllowIP string `json:"bot_protection_allow_ip"` // JSON array of CIDR/IP strings
	// TLSSkipVerify disables upstream TLS certificate verification in Caddy.
	// Use for backends with self-signed certs (e.g. Proxmox, some NAS devices).
	TLSSkipVerify bool `json:"tls_skip_verify"`
	Status       string    `json:"status"`
	Managed      bool      `json:"managed,omitempty" gorm:"-"`
	Kind         string    `json:"kind,omitempty" gorm:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type BootstrapToken struct {
	ID         string     `json:"id" gorm:"primaryKey"`
	Token      string     `json:"token" gorm:"uniqueIndex"`
	ExpiresAt  time.Time  `json:"expires_at"`
	UsedAt     *time.Time `json:"used_at"`
	MachineID  *string    `json:"machine_id"`
	TunnelPort int        `json:"tunnel_port"`
	SSHKeyID   string     `json:"ssh_key_id"`
	PublicSSH  bool       `json:"public_ssh"`
	CreatedAt  time.Time  `json:"created_at"`
}

// MigrationToken is the short ephemeral token used by the agent-install
// dashboard flow. The dashboard creates one when the operator clicks "Install
// Agent", embeds it in the curl-bash one-liner, and the operator pastes that
// command on the target machine. The /migrate/{token} endpoint resolves the
// token to a machine and renders migrate.sh with the per-machine secrets
// (agent token, port, rathole token) baked in — so secrets stay out of shell
// history and access logs.
type MigrationToken struct {
	Token     string    `gorm:"primaryKey"`
	MachineID string    `gorm:"index"`
	ExpiresAt time.Time
	CreatedAt time.Time
}

type AppSettings struct {
	ID             string    `json:"id" gorm:"primaryKey"`
	PasswordHash   string    `json:"-"`
	IsSetup        bool      `json:"is_setup"`
	Domain         string    `json:"domain"`
	// ServerHost is the hostname or IP used as the rathole remote_addr in client
	// configs. When Caddy is enabled this equals Domain. When Caddy is skipped it
	// holds the manually-provided VPS hostname/IP so client configs can still be
	// generated even though Domain is empty.
	ServerHost     string    `json:"server_host"`
	LocalSetupDone bool      `json:"local_setup_done"`
	// FirewallMode is one of "gopher" (Gopher manages iptables), "manual" (user manages),
	// or "none" (no firewall). Empty string means the wizard step has not run yet.
	FirewallMode      string    `json:"firewall_mode"`
	// DashboardPrivate restricts the dashboard port to localhost (VPS-only) when true.
	// Zero value (false) keeps it publicly reachable — safe migration default.
	DashboardPrivate  bool      `json:"dashboard_private"`
	// BindIP restricts the IP that public-facing rathole ports and Caddy listen
	// on. The dashboard's own HTTP listener is treated specially: when BindIP
	// is non-empty, the dashboard binds to 127.0.0.1 only and Caddy proxies
	// to it (see cmd/server/main.go) — the assumption is that operators who
	// set BindIP also use Caddy for TLS termination and don't want the
	// dashboard reachable on the public IP directly. Empty means 0.0.0.0
	// (all interfaces) for everything.
	BindIP            string    `json:"bind_ip" gorm:"default:''"`
	// CustomIPTables holds raw iptables rule specs (one per line, everything after
	// "iptables ") that are applied to the GOPHER_CUSTOM chain. Flushed and
	// re-applied whenever this field changes.
	CustomIPTables  string    `json:"custom_iptables"`
	// TOTP 2FA fields
	TOTPSecret      string    `json:"-"`            // base32-encoded TOTP secret; empty means not enrolled
	TOTPEnabled     bool      `json:"totp_enabled"` // true once confirmed via first successful code
	TOTPBackupCodes string    `json:"-"`            // JSON array of bcrypt-hashed one-time codes
	// Fail2ban configuration (written to /etc/fail2ban/jail.d/gopher.conf on save)
	Fail2banSetupDone bool   `json:"fail2ban_setup_done"` // true once fail2ban has been installed and configured
	Fail2banMaxRetry  int    `json:"fail2ban_max_retry"`  // default 5
	Fail2banFindTime  int    `json:"fail2ban_find_time"`  // seconds, default 300
	Fail2banBanTime   int    `json:"fail2ban_ban_time"`   // seconds, default 3600
	Fail2banIgnoreIPs string `json:"fail2ban_ignore_ips"` // JSON array of whitelisted CIDRs/IPs
	// UpdateChannel controls which release stream to track: "stable" (default), "beta", or "alpha".
	UpdateChannel string `json:"update_channel"`
	// Rathole noise-transport keypair. Generated lazily on first reconcile and
	// then frozen — rotating the private key would invalidate every machine's
	// client.toml until a fresh push lands. Empty values mean the upgrade
	// migration hasn't run yet (or this is a fresh install pre-wizard);
	// callers must treat empty as "skip noise emission" so config still parses.
	RatholeNoisePrivKey string `json:"-"`
	RatholeNoisePubKey  string `json:"-"`
	// RatholeCustomServicesWarning is a JSON array of service names that were
	// detected in /etc/rathole/server.toml's BEGIN/END CUSTOM CONFIGURATION
	// block at noise-migration time. Those clients are managed outside Gopher
	// (the operator added them by hand) and therefore weren't reachable for
	// the automatic client.toml push — they need to be updated manually with
	// the noise pubkey or they silently break the moment the server flips to
	// noise. Empty when nothing was detected. Set once during migration;
	// cleared only when the operator dismisses the dashboard banner.
	RatholeCustomServicesWarning          string `json:"-"`
	RatholeCustomServicesWarningDismissed bool   `json:"-"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// FirewallRule is a user-defined rule applied to GOPHER_CUSTOM.
// Either RawSpec is set (raw mode) or the structured fields are used.
type FirewallRule struct {
	ID          string    `json:"id" gorm:"primaryKey"`
	Description string    `json:"description"`
	Raw         bool      `json:"raw"`         // if true, RawSpec is used as-is
	RawSpec     string    `json:"raw_spec"`    // e.g. "-s 1.2.3.4 -p tcp --dport 80 -j ACCEPT"
	Protocol    string    `json:"protocol"`    // "tcp", "udp", "all", "icmp"
	PortRange   string    `json:"port_range"`  // "80", "8000:9000", "" = any
	Source      string    `json:"source"`      // CIDR, e.g. "0.0.0.0/0"
	Action      string    `json:"action"`      // "ACCEPT", "DROP", "REJECT"
	CreatedAt   time.Time `json:"created_at"`
}

// TOTPDevice represents one confirmed authenticator app/device. The user can
// enroll multiple; login accepts a code from any. AppSettings.TOTPSecret is
// still used as the *pending* enrollment slot until a new device is confirmed,
// at which point the secret is moved into a TOTPDevice row and TOTPSecret cleared.
type TOTPDevice struct {
	ID         string     `json:"id" gorm:"primaryKey"`
	Name       string     `json:"name"`
	Secret     string     `json:"-" gorm:"column:secret"` // base32-encoded TOTP secret
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

type SSHKey struct {
	ID         string    `json:"id" gorm:"primaryKey"`
	Name       string    `json:"name"`
	PublicKey  string    `json:"public_key"`
	PrivateKey string    `json:"-" gorm:"column:private_key"`
	IsDefault  bool      `json:"is_default"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Event is the unified record for everything worth surfacing on the dashboard
// or feeding to the (forthcoming) notifications subsystem: lifecycle changes,
// auth events, health-check transitions, firewall changes, etc.
//
// The dashboard "recent activity" widget and the security audit log both read
// from this single table — filtered by Source / Severity / time range.
type Event struct {
	ID        string    `json:"id" gorm:"primaryKey"`
	CreatedAt time.Time `json:"created_at" gorm:"index"`

	// Severity drives notification routing. info = log only; warn / error /
	// critical are candidates for alerts once the dispatcher exists.
	Severity string `json:"severity" gorm:"index"` // info | warn | error | critical

	// Source is the subsystem that produced the event. Indexed so the security
	// page can cheaply scope to source=auth without scanning the whole table.
	Source string `json:"source" gorm:"index"` // auth | machine | tunnel | health | firewall | system

	// Kind is a free-form dotted/underscored identifier (machine.connected,
	// auth.login.failed). Stable strings — used as the join key for kind→severity
	// defaults and eventually for notification filters.
	Kind string `json:"kind" gorm:"index"`

	// Actor: the user, service, or component that triggered the event.
	// "system" for background services, "agent" for events derived from agent
	// reports, an email address for operator-initiated actions.
	Actor string `json:"actor,omitempty"`

	// Resource fields are optional — present when the event targets a specific
	// machine, tunnel, ssh key, etc. ResourceName is denormalized so the UI
	// can render a deleted resource's name without a stale join.
	ResourceType string `json:"resource_type,omitempty"`
	ResourceID   string `json:"resource_id,omitempty"`
	ResourceName string `json:"resource_name,omitempty"`

	// IP is set for events with a remote origin (auth attempts, API calls).
	IP string `json:"ip,omitempty"`

	// Message is human-readable, rendered as-is in the UI. Producers should
	// fill this in even when Kind is descriptive — UIs aren't required to know
	// every Kind value.
	Message string `json:"message"`

	// Metadata is an opaque JSON blob for producer-specific extra context
	// (latency_ms, error details, recovery attempt count). Inspect-only —
	// don't query into this from the UI; promote a field if it matters.
	Metadata string `json:"metadata,omitempty"`
}

// TableName pins the table to "events". GORM's pluralizer would otherwise
// derive "events" from "Event" anyway, but pinning it makes the migration's
// rename target explicit.
func (Event) TableName() string { return "events" }

// ActivityEvent is a back-compat alias kept for any external callers still
// referring to the old type. New code should use Event.
//
// Deprecated: use Event.
type ActivityEvent = Event
