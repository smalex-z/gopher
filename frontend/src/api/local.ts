import client from './client'

export interface LocalServiceStatus {
  caddy_installed: boolean
  caddy_active: string
  rathole_installed: boolean
  rathole_active: string
  domain: string
  local_setup_done: boolean
  has_install_permission: boolean
}

export const localApi = {
  status: () => client.get<{ data: LocalServiceStatus }>('/local/status').then(r => r.data.data),
  install: (domain: string) => client.post('/local/install', { domain }).then(r => r.data),
  skip: () => client.post('/local/skip').then(r => r.data),
}
