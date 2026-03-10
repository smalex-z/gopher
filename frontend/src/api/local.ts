import client from './client'

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

export const localApi = {
  status: () => client.get<{ data: LocalServiceStatus }>('/local/status').then(r => r.data.data),
  install: (domain: string) => client.post('/local/install', { domain }).then(r => r.data),
  skip: (domain?: string) => client.post('/local/skip', { domain }).then(r => r.data),
  downloadSSHKey: () => client.get('/local/ssh-key', { responseType: 'blob' }).then(r => r.data as Blob),
  generateSSHKey: () =>
    client.post<{ data: { public_key: string } }>('/local/generate-ssh-key').then(r => r.data.data.public_key),
  uploadSSHKey: (privateKey: string, publicKey: string) =>
    client.put<{ data: { public_key: string } }>('/local/ssh-key', { private_key: privateKey, public_key: publicKey }).then(r => r.data.data),
}
