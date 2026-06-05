export interface VPSConfig {
  id: string
  host: string
  port: number
  username: string
  private_key: string
  domain: string
  ssh_public_key: string
  created_at: string
  updated_at: string
}

export interface Machine {
  id: string
  name: string
  host?: string
  port?: number
  username: string
  private_key?: string
  tunnel_port: number
  rathole_ssh_token?: string
  ssh_key_id?: string
  public_ssh: boolean
  status: string
  public_ip?: string
  last_seen: string | null
  // When the machine most recently became connected — used to render uptime
  // while up; last_seen is shown once it's offline.
  connected_since?: string | null
  // gopher-agent fields
  agent_local_port?: number
  agent_remote_port?: number
  agent_installed?: boolean
  agent_version?: string
  agent_last_seen?: string | null
  agent_install_error?: string
  // agent reachable but older than the server target, or pre-gRPC skew — the
  // dashboard shows the same Install one-liner, relabeled "Upgrade".
  agent_outdated?: boolean
  // config_push_pending — set when an earlier config push (typically the
  // noise migration) couldn't land. The health loop retries on reconnect;
  // the dashboard surfaces a Recover button until cleared.
  config_push_pending?: boolean
  created_at: string
  updated_at: string
  tunnels?: Tunnel[]
}

export interface HealthCheck {
  id: string
  subject: string
  checked_at: string
  ok: boolean
  latency_ms: number
  error_msg?: string
  recovered?: boolean
}

// HealthSummary aggregates the rolling-window stats the dashboard renders
// (uptime % + sparkline). Returned by /machines/{id}/health and
// /tunnels/{id}/health. UptimePercent is null until at least one check
// has been recorded — the UI shows "—" rather than "0%" in that case.
export interface HealthSummary {
  uptime_percent: number | null
  total_checks: number
  ok_checks: number
  recent: HealthCheck[]
  latest: HealthCheck | null
}

// AgentStatus mirrors the agent's /status JSON (cmd/agent/main.go).
// Fetched on demand when the operator expands a machine row.
export interface AgentStatus {
  agent_version: string
  agent_uptime_seconds: number
  restarts_served: number
  rathole: {
    active: boolean
    state: string
    substate: string
  }
  system: {
    load_avg_1: number
    load_avg_5: number
    load_avg_15: number
    mem_total_kb: number
    mem_avail_kb: number
    disk_free_bytes: number
    disk_total_bytes: number
    hostname: string
    kernel: string
  }
  now: string
}

export interface Tunnel {
  id: string
  machine_id: string
  name: string
  subdomain: string
  local_port: number
  rathole_port: number
  protocol: string
  transport?: string   // "tcp" (default) | "udp"
  no_tls?: boolean     // skip Caddy TLS; use plain http://
  private?: boolean    // bind 127.0.0.1 (VPS-local only)
  bot_protection_enabled?: boolean
  bot_protection_ttl?: number      // seconds; 0 = default (86400)
  bot_protection_allow_ip?: string // JSON array of CIDR/IP strings
  tls_skip_verify?: boolean        // skip upstream TLS cert verification (e.g. Proxmox)
  status: string
  managed?: boolean
  kind?: string
  created_at: string
  updated_at: string
}

export interface SSHKey {
  id: string
  name: string
  public_key: string
  is_default: boolean
  machine_count?: number
  created_at: string
  updated_at: string
}

export interface FirewallRule {
  id: string
  description: string
  raw: boolean
  raw_spec: string
  protocol: string
  port_range: string
  source: string
  action: string
  created_at: string
}

export interface FirewallEntry {
  type: 'system' | 'tunnel' | 'machine-ssh' | 'custom'
  id?: string
  description: string
  protocol: string
  port_range: string
  source: string
  action: string
  raw?: boolean
  raw_spec?: string
}

export interface ApiResponse<T> {
  success: boolean
  data?: T
  error?: string
  message?: string
}

export interface StatusData {
  machines: number
  tunnels: number
  vps: boolean
}
