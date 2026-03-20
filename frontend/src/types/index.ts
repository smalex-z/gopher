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
  status: string
  last_seen: string | null
  created_at: string
  updated_at: string
  tunnels?: Tunnel[]
}

export interface Tunnel {
  id: string
  machine_id: string
  name: string
  subdomain: string
  local_port: number
  rathole_port: number
  protocol: string
  status: string
  managed?: boolean
  kind?: string
  created_at: string
  updated_at: string
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
