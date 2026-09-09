package db

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"gorm.io/gorm"
)

// randomID returns a 16-character hex string (8 bytes of entropy). Used as the
// primary key for rows where we don't have a natural ID handy (e.g. HealthCheck).
func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// DashboardSession is a persisted operator login session. Stored hashed
// (SHA-256 of the bearer token) so a leaked DB doesn't yield usable tokens.
// Persisted rather than in-memory because gopher restarts itself as part of
// normal operation — the post-install supervisor kick and self-updates — and
// in-memory sessions logged the operator out mid-setup-wizard.
type DashboardSession struct {
	TokenHash string    `json:"-" gorm:"primaryKey"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
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

// VPSConfig is the edge's public identity (host + domain), derived from
// settings by GetVPS for GET /api/vps. It no longer carries any SSH
// credentials: the old flow stored a VPS keypair here, but the server never
// holds VPS-side private keys now.
type VPSConfig struct {
	ID           string    `json:"id" gorm:"primaryKey"`
	Host         string    `json:"host"`
	Port         int       `json:"port"`
	Username     string    `json:"username"`
	Domain       string    `json:"domain"`
	SSHPublicKey string    `json:"ssh_public_key"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Machine struct {
	ID              string     `json:"id" gorm:"primaryKey"`
	Name            string     `json:"name"`
	Host            string     `json:"host"`
	Port            int        `json:"port"`
	Username        string     `json:"username"`
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
	// AgentOutdated is true when the agent is reachable but older than the
	// server's target version, or is a pre-gRPC agent the server can't talk to
	// (protocol skew). The dashboard surfaces the same Install/Upgrade one-liner
	// for it. Cleared once a current agent is seen.
	AgentOutdated bool `json:"agent_outdated"`
	// AgentManualUpgradeRequired is true when an auto-upgrade determined the
	// agent is too old to self-update (predates the /self-update endpoint) and
	// needs a one-time manual reinstall. Distinguishes "self-update in flight"
	// (dashboard shows "Updating…") from "reinstall required" (dashboard shows a
	// "Reinstall agent" action). Cleared once the server can gRPC-poll the agent
	// again — proof it's a modern agent.
	AgentManualUpgradeRequired bool `json:"agent_manual_upgrade_required"`
	// ConfigPushPending marks machines whose last attempted client.toml push
	// failed (offline, full disk, agent down). The health service retries the
	// push the next time the machine becomes reachable, then clears the flag.
	// Set by the noise migration's failure path; intended to be general — any
	// future push path that fails to land should set this rather than logging
	// and moving on.
	ConfigPushPending bool      `json:"config_push_pending,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	Tunnels           []Tunnel  `json:"tunnels,omitempty" gorm:"foreignKey:MachineID"`
	// SSHTunnelStatus/AgentTunnelStatus are the same active/inactive/pending
	// vocabulary the Tunnels page shows for these machines' synthetic
	// machine-ssh/machine-agent rows (see machineTunnelStatus/agentTunnelStatus
	// in tunnel.go), populated here too so the Machines page's expanded SSH/
	// Agent rows can't drift from the Tunnels page's labels for the identical
	// underlying tunnel. Status remains the machine's own reachability
	// ("connected"/"offline"/"pending") — a distinct concept, deliberately
	// left alone.
	SSHTunnelStatus   string `json:"ssh_tunnel_status,omitempty" gorm:"-"`
	AgentTunnelStatus string `json:"agent_tunnel_status,omitempty" gorm:"-"`
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
	ID           string `json:"id" gorm:"primaryKey"`
	MachineID    string `json:"machine_id"`
	Name         string `json:"name"`
	Subdomain    string `json:"subdomain"`
	LocalPort    int    `json:"local_port"`
	RatholePort  int    `json:"rathole_port"`
	RatholeToken string `json:"rathole_token"`
	Protocol     string `json:"protocol"`
	Transport    string `json:"transport"` // "tcp" (default) or "udp"
	NoTLS        bool   `json:"no_tls"`    // skip Caddy HTTPS; use plain http://
	Private      bool   `json:"private"`   // bind 127.0.0.1 (VPS-local only) instead of 0.0.0.0
	// Bot protection — opt-in per tunnel, HTTP subdomain tunnels only.
	BotProtectionEnabled bool   `json:"bot_protection_enabled"`
	BotProtectionTTL     int    `json:"bot_protection_ttl"`      // session TTL in seconds; 0 = default (86400)
	BotProtectionAllowIP string `json:"bot_protection_allow_ip"` // JSON array of CIDR/IP strings
	// Password auth — opt-in per tunnel, HTTP subdomain tunnels only, requires a
	// private tunnel (same guards as bot protection). A separate, distinct gate
	// from the gopher dashboard login. AuthPasswordHash is bcrypt and is never
	// serialized; AuthPasswordSet is the computed flag the UI reads instead.
	AuthEnabled      bool   `json:"auth_enabled"`
	AuthPasswordHash string `json:"-" gorm:"column:auth_password_hash"`
	AuthPasswordSet  bool   `json:"auth_password_set" gorm:"-"`
	AuthTTL          int    `json:"auth_ttl"`      // session TTL in seconds; 0 = default (86400)
	AuthAllowIP      string `json:"auth_allow_ip"` // JSON array of CIDR/IP strings that bypass the gate
	// TLSSkipVerify disables upstream TLS certificate verification in Caddy.
	// Use for backends with self-signed certs (e.g. Proxmox, some NAS devices).
	TLSSkipVerify bool   `json:"tls_skip_verify"`
	Status        string `json:"status"`
	// CaddyPending is true from tunnel create / subdomain change until a
	// local probe confirms Caddy is actually serving the route (config
	// applied AND a certificate is available for the SNI). While set, the
	// API presents status "provisioning" instead of the rathole-path status:
	// the rathole port binds seconds before the public URL stops throwing
	// TLS alerts (issue #93), and "active but TLS-broken" reads as a lie.
	CaddyPending bool      `json:"caddy_pending"`
	Managed      bool      `json:"managed,omitempty" gorm:"-"`
	Kind         string    `json:"kind,omitempty" gorm:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// AfterFind computes AuthPasswordSet on every DB read so the UI can tell whether
// a password is configured without ever seeing the hash.
func (t *Tunnel) AfterFind(*gorm.DB) error {
	t.AuthPasswordSet = t.AuthPasswordHash != ""
	return nil
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
	// SSHEnabled controls whether this bootstrap provisions an SSH back-tunnel +
	// authorized_keys entry at all. False = agent-only machine (no SSH exposure,
	// control via the agent only). Set from the UI/API; the client can still
	// force it off via the bootstrap script's --no-ssh flag.
	SSHEnabled bool      `json:"ssh_enabled"`
	CreatedAt  time.Time `json:"created_at"`
}

// MigrationToken is the short ephemeral token used by the agent-install
// dashboard flow. The dashboard creates one when the operator clicks "Install
// Agent", embeds it in the curl-bash one-liner, and the operator pastes that
// command on the target machine. The /migrate/{token} endpoint resolves the
// token to a machine and renders migrate.sh with the per-machine secrets
// (agent token, port, rathole token) baked in — so secrets stay out of shell
// history and access logs.
type MigrationToken struct {
	Token     string `gorm:"primaryKey"`
	MachineID string `gorm:"index"`
	ExpiresAt time.Time
	CreatedAt time.Time
	// UsedAt marks the token consumed: POST /api/migrate hands out the
	// machine's agent + rathole credentials, so a token must not be
	// replayable within its TTL. Set atomically by ClaimMigrationToken.
	UsedAt *time.Time
}

type AppSettings struct {
	ID           string `json:"id" gorm:"primaryKey"`
	PasswordHash string `json:"-"`
	IsSetup      bool   `json:"is_setup"`
	Domain       string `json:"domain"`
	// ServerHost is the hostname or IP used as the rathole remote_addr in client
	// configs. When Caddy is enabled this equals Domain. When Caddy is skipped it
	// holds the manually-provided VPS hostname/IP so client configs can still be
	// generated even though Domain is empty.
	ServerHost     string `json:"server_host"`
	LocalSetupDone bool   `json:"local_setup_done"`
	// FirewallMode is one of "gopher" (Gopher manages iptables), "manual" (user manages),
	// or "none" (no firewall). Empty string means the wizard step has not run yet.
	FirewallMode string `json:"firewall_mode"`
	// DashboardPrivate restricts the dashboard port to localhost (VPS-only) when true.
	// Zero value (false) keeps it publicly reachable — safe migration default.
	DashboardPrivate bool `json:"dashboard_private"`
	// BindIP restricts the IP that public-facing rathole ports and Caddy listen
	// on. The dashboard's own HTTP listener is treated specially: when BindIP
	// is non-empty, the dashboard binds to 127.0.0.1 only and Caddy proxies
	// to it (see cmd/server/main.go) — the assumption is that operators who
	// set BindIP also use Caddy for TLS termination and don't want the
	// dashboard reachable on the public IP directly. Empty means 0.0.0.0
	// (all interfaces) for everything.
	BindIP string `json:"bind_ip" gorm:"default:''"`
	// CustomIPTables holds raw iptables rule specs (one per line, everything after
	// "iptables ") that are applied to the GOPHER_CUSTOM chain. Flushed and
	// re-applied whenever this field changes.
	CustomIPTables string `json:"custom_iptables"`
	// TOTP 2FA fields
	TOTPSecret      string `json:"-"`            // base32-encoded TOTP secret; empty means not enrolled
	TOTPEnabled     bool   `json:"totp_enabled"` // true once confirmed via first successful code
	TOTPBackupCodes string `json:"-"`            // JSON array of bcrypt-hashed one-time codes
	// Fail2ban configuration (written to /etc/fail2ban/jail.d/gopher.conf on save)
	Fail2banSetupDone bool   `json:"fail2ban_setup_done"` // true once fail2ban has been installed and configured
	Fail2banSkipped   bool   `json:"fail2ban_skipped"`    // operator declined the wizard's fail2ban step; suppresses it without claiming fail2ban is installed
	Fail2banMaxRetry  int    `json:"fail2ban_max_retry"`  // default 5
	Fail2banFindTime  int    `json:"fail2ban_find_time"`  // seconds, default 300
	Fail2banBanTime   int    `json:"fail2ban_ban_time"`   // seconds, default 3600
	Fail2banIgnoreIPs string `json:"fail2ban_ignore_ips"` // JSON array of whitelisted CIDRs/IPs
	// UpdateChannel controls which release stream to track: "stable" (default), "beta", or "alpha".
	UpdateChannel string `json:"update_channel"`
	// ExternalAPIKey is the bearer token for the /api/v1/* external REST API.
	// If the GOPHER_API_KEY environment variable is set it takes precedence over this field.
	ExternalAPIKey string `json:"-"` // never serialised — returned only via dedicated endpoints
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
	// RatholeNoiseBlockedMachines is a JSON array of machine names that were
	// unreachable when the noise migration last tried to run. Non-empty means
	// the migration DEFERRED itself: nothing was changed, the install is still
	// on plaintext transport with every tunnel working, and it will retry on
	// the next restart. The dashboard surfaces this so the operator knows
	// which origins to bring back rather than discovering a silent
	// half-upgrade later. Cleared on a successful migration.
	RatholeNoiseBlockedMachines string    `json:"-"`
	CreatedAt                   time.Time `json:"created_at"`
	UpdatedAt                   time.Time `json:"updated_at"`
}

// FirewallRule is a user-defined rule applied to GOPHER_CUSTOM.
// Either RawSpec is set (raw mode) or the structured fields are used.
type FirewallRule struct {
	ID          string    `json:"id" gorm:"primaryKey"`
	Description string    `json:"description"`
	Raw         bool      `json:"raw"`        // if true, RawSpec is used as-is
	RawSpec     string    `json:"raw_spec"`   // e.g. "-s 1.2.3.4 -p tcp --dport 80 -j ACCEPT"
	Protocol    string    `json:"protocol"`   // "tcp", "udp", "all", "icmp"
	PortRange   string    `json:"port_range"` // "80", "8000:9000", "" = any
	Source      string    `json:"source"`     // CIDR, e.g. "0.0.0.0/0"
	Action      string    `json:"action"`     // "ACCEPT", "DROP", "REJECT"
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

	// HasPrivateKey tells the frontend whether the private half is still stored
	// server-side (it never sees the key itself — that's json:"-"). Set by the
	// AfterFind hook. When false, the key is public-only: usable for
	// authorized_keys / the jumpbox, but the server can no longer SSH with it.
	HasPrivateKey bool `json:"has_private_key" gorm:"-"`
}

// AfterFind populates the computed HasPrivateKey on every read.
func (k *SSHKey) AfterFind(*gorm.DB) error {
	k.HasPrivateKey = k.PrivateKey != ""
	return nil
}

// ExternalMachine tracks machines bootstrapped via the external REST API.
// A record is created when POST /api/v1/machines is called. Once the VM runs
// the bootstrap script, MachineID is set and the underlying Machine record's
// Status field reflects connectivity.
type ExternalMachine struct {
	ID        string    `json:"id" gorm:"primaryKey"`
	TokenID   string    `json:"token_id"`   // BootstrapToken.ID
	MachineID *string   `json:"machine_id"` // set once the VM registers
	PublicSSH bool      `json:"public_ssh"`
	SSHKeyID  string    `json:"ssh_key_id"`
	ErrorMsg  string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ExternalTunnel tracks service tunnels created via the external REST API.
// A record is created when POST /api/v1/tunnels is called against an already-connected
// machine. Creation is synchronous — the tunnel is active or failed immediately.
type ExternalTunnel struct {
	ID         string    `json:"id" gorm:"primaryKey"`
	MachineID  string    `json:"machine_id"` // must be a connected ExternalMachine's machine_id
	TunnelID   string    `json:"tunnel_id"`  // Tunnel.ID created for this record
	Subdomain  string    `json:"subdomain"`
	TargetIP   string    `json:"target_ip"`
	TargetPort int       `json:"target_port"`
	Status     string    `json:"status"` // active | failed
	TunnelURL  string    `json:"tunnel_url"`
	ErrorMsg   string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Event is the unified record for everything worth surfacing on the dashboard
// or feeding to the (forthcoming) notifications subsystem: lifecycle changes,
// auth events, health-check transitions, firewall changes, etc.
//
// The dashboard "recent activity" widget and the security audit log both read
// from this single table — filtered by Source / Severity / time range.
//
// Replaces the older single-purpose ActivityEvent struct (kept as an alias
// below for any external import that hasn't been updated yet).
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

// ActivityEvent is a back-compat alias for the old simpler activity-feed
// struct. New code should use Event directly. The alias keeps existing
// AutoMigrate / repository call sites compiling during the unified-events
// rollout.
//
// Deprecated: use Event.
type ActivityEvent = Event
