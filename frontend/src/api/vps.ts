import client from './client'
import type { VPSConfig, ApiResponse } from '../types'

export const vpsApi = {
  get: () => client.get<ApiResponse<VPSConfig>>('/vps/').then(r => r.data),
  create: (data: Partial<VPSConfig>) => client.post<ApiResponse<VPSConfig>>('/vps/setup', data).then(r => r.data),
  update: (data: Partial<VPSConfig>) => client.put<ApiResponse<VPSConfig>>('/vps/', data).then(r => r.data),
  delete: () => client.delete('/vps/').then(r => r.data),
  bootstrap: () => client.post('/vps/bootstrap').then(r => r.data),
  deploy: () => client.post('/vps/deploy').then(r => r.data),
  status: () => client.get('/vps/status').then(r => r.data),
  generateToken: (tunnelPort?: number, sshKeyID?: string, publicSSH?: boolean) => client.post<ApiResponse<{ token: string; bootstrap_command: string; expires_at: string }>>('/bootstrap/token', {
    ...(tunnelPort ? { tunnel_port: tunnelPort } : {}),
    ...(sshKeyID ? { ssh_key_id: sshKeyID } : {}),
    ...(publicSSH ? { public_ssh: true } : {}),
  }).then(r => r.data),
}
