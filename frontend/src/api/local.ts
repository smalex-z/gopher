import client from './client'
import type { SSHKey, ApiResponse } from '../types'

export interface LocalServiceStatus {
  caddy_installed: boolean
  caddy_active: string
  rathole_installed: boolean
  rathole_active: string
  domain: string
  local_setup_done: boolean
  has_install_permission: boolean
  ssh_public_key: string
}

export interface DNSCheckResult {
  ok: boolean
  message?: string
  resolved_to?: string
  host?: string
}

export const localApi = {
  status: () => client.get<{ data: LocalServiceStatus }>('/local/status').then(r => r.data.data),
  install: (domain: string, skipCaddy?: boolean) =>
    client.post('/local/install', { domain, skip_caddy: Boolean(skipCaddy) }).then(r => r.data),
  skip: (domain?: string) => client.post('/local/skip', { domain }).then(r => r.data),
  checkDNS: (domain: string) =>
    client.get<{ data: DNSCheckResult }>(`/local/check-dns?domain=${encodeURIComponent(domain)}`).then(r => r.data.data),
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
}
