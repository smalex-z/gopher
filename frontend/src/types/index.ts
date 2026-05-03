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
  // gopher-agent fields
  agent_local_port?: number
  agent_remote_port?: number
  agent_installed?: boolean
  agent_version?: string
  agent_last_seen?: string | null
  agent_install_error?: string
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
