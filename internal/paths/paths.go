// Package paths is the single source of truth for where Gopher and the
// binaries and config it owns live on the edge.
//
// The v0.1.0 consolidation collapses the legacy scatter — Caddy as an apt
// package under /etc/caddy + /var/lib/caddy, rathole downloaded to
// /usr/local/bin with config in /etc/rathole, three separate systemd services —
// into a single self-contained layout that Gopher owns end to end:
//
//	/opt/gopher/            gopher binary + extracted child binaries
//	/opt/gopher/bin/        caddy, rathole (extracted from the embedded bundle)
//	/etc/gopher/            all generated config (caddy/, rathole/)
//	/var/lib/gopher/        all state (gopher.db, caddy/ cert+data storage)
//
// Everything lives under two trees — /etc/gopher and /var/lib/gopher — so
// backup is "tar two dirs" and uninstall is "stop one service, rm two trees".
package paths

// These are vars, not consts, ONLY so tests (layout migration, install) can
// redirect the trees to a temp dir. Production code must never assign to them.
var (
	// Root is the install dir for the gopher binary and the bin/ subdir that
	// holds the child binaries extracted from the embedded bundle at startup.
	Root   = "/opt/gopher"
	BinDir = Root + "/bin"

	// ConfigDir holds every config file Gopher generates. Children are pointed
	// here explicitly (caddy --config, rathole <path>) rather than at their
	// upstream defaults.
	ConfigDir = "/etc/gopher"

	// StateDir holds all persistent state: the sqlite DB and Caddy's
	// certificate/data storage (via XDG_DATA_HOME=CaddyData when we spawn it).
	StateDir = "/var/lib/gopher"
)

// Extracted child binaries.
var (
	CaddyBin   = BinDir + "/caddy"
	RatholeBin = BinDir + "/rathole"
)

// ManagedKeyMarker is the authorized_keys comment Gopher tags its single
// managed key with, so it can find and replace exactly its own key on an origin
// and never touch an operator-owned key. Shared here so the server (SSH
// fallback) and the agent (SetManagedKey RPC) can't drift. The bootstrap.sh and
// gopher-uninstall.sh templates hardcode the same string — they can't import
// Go — so keep those in sync by hand if this ever changes.
const ManagedKeyMarker = "gopher-managed"

// Generated config files and dirs.
var (
	CaddyfilePath = ConfigDir + "/caddy/Caddyfile"
	CaddyConfDir  = ConfigDir + "/caddy/conf.d"
	RatholeDir    = ConfigDir + "/rathole"
	RatholeConfig = RatholeDir + "/server.toml"
)

// Origin (client machine) layout. The same /etc/gopher consolidation applied to
// the edge, applied to the machines that tunnel into it: the rathole client
// config and the gopher-agent's env file live under /etc/gopher instead of the
// legacy /etc/rathole and /etc/gopher-agent. The client config shares the
// rathole/ dir with the edge's server.toml; the agent's env gets its own
// agent/ dir. The agent migrates an existing origin onto these on its first
// post-upgrade boot (see cmd/agent migrate).
var (
	RatholeClientConfig = RatholeDir + "/client.toml"
	RatholeVPSKey       = RatholeDir + "/vps_key.pub"
	AgentDir            = ConfigDir + "/agent"
	AgentConfigEnv      = AgentDir + "/config.env"
)

// Persistent state.
var (
	DBPath = StateDir + "/gopher.db"
	// CaddyData is set as Caddy's XDG_DATA_HOME so its ACME certs and data live
	// under the gopher state tree instead of /var/lib/caddy.
	CaddyData = StateDir + "/caddy"
)

// Legacy locations from the pre-consolidation layout, kept so the migration
// step can detect and move an existing install. Nothing should write to these.
var (
	LegacyRatholeConfig = "/etc/rathole/server.toml"
	LegacyRatholeBin    = "/usr/local/bin/rathole"
	LegacyCaddyfile     = "/etc/caddy/Caddyfile"
	LegacyCaddyConfDir  = "/etc/caddy/conf.d"

	// Legacy origin locations (pre-consolidation). The agent migrates off these;
	// the server probes new-then-legacy so un-migrated machines keep working.
	LegacyRatholeClientConfig = "/etc/rathole/client.toml"
	LegacyRatholeClientDir    = "/etc/rathole"
	LegacyRatholeVPSKey       = "/etc/rathole/vps_key.pub"
	LegacyAgentConfigEnv      = "/etc/gopher-agent/config.env"
	LegacyAgentDir            = "/etc/gopher-agent"
	// LegacyCaddyData is the apt Caddy's actual data root. That Caddy runs as
	// User=caddy with HOME=/var/lib/caddy and uses $HOME/.local/share/caddy (NOT
	// XDG_DATA_HOME), so the certs live one level deep. The supervised Caddy uses
	// XDG_DATA_HOME=/var/lib/gopher -> /var/lib/gopher/caddy (= CaddyData), so
	// migration copies this dir's contents there.
	LegacyCaddyData = "/var/lib/caddy/.local/share/caddy"
)
