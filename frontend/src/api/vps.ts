import client from './client'
import type { VPSConfig, ApiResponse } from '../types'

export interface GenerateTokenOpts {
  tunnelPort?: number
  sshKeyID?: string
  publicSSH?: boolean  // omit → server default (public); false → jumpbox-gated
  sshEnabled?: boolean // omit → server default (enabled); false → agent-only
}

export const vpsApi = {
  get: () => client.get<ApiResponse<VPSConfig>>('/vps/').then(r => r.data),
  generateToken: (opts: GenerateTokenOpts = {}) =>
    client.post<ApiResponse<{ token: string; bootstrap_command: string; expires_at: string }>>('/bootstrap/token', {
      ...(opts.tunnelPort ? { tunnel_port: opts.tunnelPort } : {}),
      ...(opts.sshKeyID ? { ssh_key_id: opts.sshKeyID } : {}),
      ...(opts.publicSSH !== undefined ? { public_ssh: opts.publicSSH } : {}),
      ...(opts.sshEnabled !== undefined ? { ssh_enabled: opts.sshEnabled } : {}),
    }).then(r => r.data),
}
