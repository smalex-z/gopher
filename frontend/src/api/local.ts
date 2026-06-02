import client from './client'
import type { SSHKey, ApiResponse, FirewallRule, FirewallEntry } from '../types'

export interface LocalServiceStatus {
  caddy_installed: boolean
  caddy_active: string
  rathole_installed: boolean
  rathole_active: string
  domain: string
  server_host: string
  local_setup_done: boolean
  has_install_permission: boolean
  ssh_public_key: string
  /** "gopher" | "manual" | "none" | "" (wizard not yet completed) */
  firewall_mode: string
  /** true when the dashboard port is restricted to localhost (use router.domain instead) */
  dashboard_private: boolean
  /** the port the Gopher HTTP server listens on */
  dashboard_port: number
  /** OS username Gopher runs as (e.g. "gopher"). Used for ownership / fallback only. */
  os_user: string
  /** Dedicated, privilege-free user whose authorized_keys holds Gopher-managed
   *  keys. This is the username SSH jumpbox commands should target. Empty
   *  string means the user hasn't been created yet (legacy install needs
   *  `gopher install` re-run); commands fall back to os_user with a warning. */
  jumpbox_user: string
  /** true once fail2ban has been installed and configured by Gopher */
  fail2ban_setup_done: boolean
  /** IP address Gopher binds public listeners to. Empty string = 0.0.0.0 (all interfaces). */
  bind_ip: string
  /** All non-loopback IPv4 addresses on the host. More than one means multi-homed. */
  host_ips: string[]
  /** Base64 X25519 public key for rathole's noise transport. Operators copy
   *  this into hand-rolled rathole-client configs for user-managed services. */
  rathole_noise_pubkey: string
  /** Names of user-managed services from server.toml's custom block that
   *  need a manual noise pubkey update on their client side. Set during
   *  noise migration. Empty when nothing needs attention or after dismissal. */
  rathole_custom_services_warning: string[]
}

export interface FirewallStatus {
  ufw: { installed: boolean; active: boolean }
  firewalld: { installed: boolean; active: boolean }
  nftables: { installed: boolean; active: boolean; has_config: boolean }
  iptables: { available: boolean }
  any_active: boolean
}

export type FirewallMode = 'gopher' | 'manual' | 'none'

export type DNSCheckStatus = 'pass' | 'warn' | 'fail' | 'skip'

export interface DNSCheck {
  name: string
  label: string
  status: DNSCheckStatus
  message: string
}

export interface DNSCheckResult {
  ok: boolean
  message?: string
  resolved_to?: string
  host?: string
  expected_ip?: string
  checks?: DNSCheck[]
}

export const localApi = {
  status: () => client.get<{ data: LocalServiceStatus }>('/local/status').then(r => r.data.data),
  dismissCustomServicesWarning: () =>
    client.post('/local/dismiss-custom-services-warning').then(r => r.data),
  install: (domain: string, serverHost: string, skipCaddy?: boolean) =>
    client.post('/local/install', { domain, server_host: serverHost, skip_caddy: Boolean(skipCaddy) }).then(r => r.data),
  skip: (domain?: string) => client.post('/local/skip', { domain }).then(r => r.data),
  detectIP: () =>
    client.get<{ data: { ip: string } }>('/local/detect-ip').then(r => r.data.data),
  checkDNS: (domain: string, expectedIP?: string) => {
    const params = new URLSearchParams({ domain })
    if (expectedIP) params.set('expected_ip', expectedIP)
    return client.get<{ data: DNSCheckResult }>(`/local/check-dns?${params.toString()}`).then(r => r.data.data)
  },
  resolveIP: (host: string) =>
    client.get<{ data: { ip: string } }>(`/local/resolve-ip?host=${encodeURIComponent(host)}`).then(r => r.data.data),
  listSSHKeys: () =>
    client.get<ApiResponse<SSHKey[]>>('/local/ssh-keys').then(r => r.data),
  generateSSHKey: (name: string, setDefault: boolean) =>
    client.post<ApiResponse<SSHKey>>('/local/ssh-keys/generate', { name, set_default: setDefault }).then(r => r.data),
  uploadSSHKey: (name: string, privateKey: string, publicKey: string, setDefault: boolean) =>
    client.post<ApiResponse<SSHKey>>('/local/ssh-keys/upload', { name, private_key: privateKey, public_key: publicKey, set_default: setDefault }).then(r => r.data),
  deleteSSHKey: (id: string) =>
    client.delete(`/local/ssh-keys/${id}`).then(r => r.data),
  setDefaultSSHKey: (id: string) =>
    client.put(`/local/ssh-keys/${id}/default`).then(r => r.data),
  // Re-auth required: prompts for TOTP code (if 2FA enrolled) or login
  // password (otherwise). Caller fetches challenge-info first to know which
  // credential to ask the operator for, then submits it here.
  sshKeyChallengeInfo: () =>
    client.get<ApiResponse<{ requires: 'totp' | 'password' }>>('/local/ssh-keys/challenge-info').then(r => r.data.data),
  downloadSSHKey: (id: string, challenge: { totp_code?: string; password?: string }) =>
    client.post(`/local/ssh-keys/${id}/download`, challenge, { responseType: 'blob' }).then(r => r.data as Blob),
  detectFirewall: () =>
    client.get<{ data: FirewallStatus }>('/local/firewall/detect').then(r => r.data.data),
  configureFirewall: (mode: FirewallMode) =>
    client.post('/local/firewall/configure', { mode }).then(r => r.data),
  firewallOverview: () =>
    client.get<ApiResponse<FirewallEntry[]>>('/local/firewall/overview').then(r => r.data),
  createFirewallRule: (rule: { description?: string; raw?: boolean; raw_spec?: string; protocol?: string; port_range?: string; source?: string; action?: string }) =>
    client.post<ApiResponse<FirewallRule>>('/local/firewall/rules', rule).then(r => r.data),
  deleteFirewallRule: (id: string) =>
    client.delete(`/local/firewall/rules/${id}`).then(r => r.data),
  getLiveRules: () =>
    client.get<ApiResponse<Record<string, string>>>('/local/firewall/live').then(r => r.data),
  reloadFirewall: () =>
    client.post('/local/firewall/reload').then(r => r.data),
  setServerPorts: (dashboardPrivate: boolean) =>
    client.put('/local/server-ports', { dashboard_private: dashboardPrivate }).then(r => r.data),
  setBindIP: (bindIP: string) =>
    client.put('/local/bind-ip', { bind_ip: bindIP }).then(r => r.data),
  setupFail2ban: () =>
    client.post('/local/setup-fail2ban').then(r => r.data),
  activity: () =>
    client.get<{ data: ActivityEvent[] }>('/local/activity').then(r => r.data.data),
}

export type EventSeverity = 'info' | 'warn' | 'error' | 'critical'
export type EventSource = 'auth' | 'machine' | 'tunnel' | 'health' | 'firewall' | 'system'

export interface ActivityEvent {
  id: string
  created_at: string
  severity: EventSeverity
  source: EventSource
  kind: string
  actor?: string
  resource_type?: string
  resource_id?: string
  resource_name?: string
  ip?: string
  message: string
  metadata?: string
}
