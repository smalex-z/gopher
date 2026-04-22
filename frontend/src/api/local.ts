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
  /** OS username Gopher runs as (e.g. "ubuntu") — used to pre-fill VPS jump-host user */
  os_user: string
  /** true once fail2ban has been installed and configured by Gopher */
  fail2ban_setup_done: boolean
  /** IP address Gopher binds public listeners to. Empty string = 0.0.0.0 (all interfaces). */
  bind_ip: string
  /** All non-loopback IPv4 addresses on the host. More than one means multi-homed. */
  host_ips: string[]
}

export interface FirewallStatus {
  ufw: { installed: boolean; active: boolean }
  firewalld: { installed: boolean; active: boolean }
  nftables: { installed: boolean; active: boolean; has_config: boolean }
  iptables: { available: boolean }
  any_active: boolean
}

export type FirewallMode = 'gopher' | 'manual' | 'none'

export interface DNSCheckResult {
  ok: boolean
  message?: string
  resolved_to?: string
  host?: string
}

export const localApi = {
  status: () => client.get<{ data: LocalServiceStatus }>('/local/status').then(r => r.data.data),
  install: (domain: string, serverHost: string, skipCaddy?: boolean) =>
    client.post('/local/install', { domain, server_host: serverHost, skip_caddy: Boolean(skipCaddy) }).then(r => r.data),
  skip: (domain?: string) => client.post('/local/skip', { domain }).then(r => r.data),
  detectIP: () =>
    client.get<{ data: { ip: string } }>('/local/detect-ip').then(r => r.data.data),
  checkDNS: (domain: string) =>
    client.get<{ data: DNSCheckResult }>(`/local/check-dns?domain=${encodeURIComponent(domain)}`).then(r => r.data.data),
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
  downloadSSHKey: (id: string) =>
    client.get(`/local/ssh-keys/${id}/download`, { responseType: 'blob' }).then(r => r.data as Blob),
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
}
