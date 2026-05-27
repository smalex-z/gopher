import client from './client'
import type { Machine, HealthCheck, HealthSummary, AgentStatus, ApiResponse } from '../types'

export const machinesApi = {
  list: () => client.get<ApiResponse<Machine[]>>('/machines/').then(r => r.data),
  get: (id: string) => client.get<ApiResponse<Machine>>(`/machines/${id}`).then(r => r.data),
  create: (data: Partial<Machine>) => client.post<ApiResponse<Machine>>('/machines/', data).then(r => r.data),
  update: (id: string, data: Partial<Machine>) => client.put<ApiResponse<Machine>>(`/machines/${id}`, data).then(r => r.data),
  delete: (id: string) =>
    client.delete<ApiResponse<{
      id: string
      client_cleanup_ok: boolean
      client_cleanup_path?: 'agent' | 'ssh' | 'skipped'
      client_cleanup_error?: string
    }>>(`/machines/${id}`).then(r => r.data),
  deploy: (id: string) => client.post(`/machines/${id}/deploy`).then(r => r.data),
  status: (id: string) => client.get(`/machines/${id}/status`).then(r => r.data),
  networkInfo: (id: string) =>
    client.get<{ data: { id: string; public_ip: string; private_ip: string; is_nat: boolean; error?: string } }>(
      `/machines/${id}/network-info`
    ).then(r => r.data.data),
  reassignSSHKey: (id: string, sshKeyID: string) => client.put(`/machines/${id}/ssh-key`, { ssh_key_id: sshKeyID }).then(r => r.data),
  // gopher-agent / health endpoints
  pendingAgents: () => client.get<ApiResponse<Machine[]>>('/machines/agent/pending').then(r => r.data),
  installAgent: (id: string) =>
    client.post<ApiResponse<{ command: string; instruction: string }>>(`/machines/${id}/install-agent`).then(r => r.data),
  health: (id: string) =>
    client.get<ApiResponse<HealthSummary>>(`/machines/${id}/health`).then(r => r.data.data),
  runCheck: (id: string) =>
    client.post<{ data: { check: HealthCheck; now: string } }>(`/machines/${id}/health/check`).then(r => r.data.data),
  agentStatus: (id: string) =>
    client.get<ApiResponse<AgentStatus>>(`/machines/${id}/agent-status`).then(r => r.data.data),
  // Server-side recovery: tries agent push, falls back to SSH-via-tunnel.
  // Resolves with a success message; rejects with the underlying push error
  // when both paths failed (typically the tunnel is fully down — operator
  // falls back to the manual script).
  recover: (id: string) => client.post<ApiResponse<{ message: string }>>(`/machines/${id}/recover`).then(r => r.data.data),
  // Canonical client.toml the machine should be running. The text variant is
  // fetched into the dashboard for display in the recovery modal; the script
  // variant is the .sh download (server-side recovery has already failed).
  ratholeConfig: (id: string) =>
    client.get<string>(`/machines/${id}/rathole-config`, { responseType: 'text', transformResponse: r => r }).then(r => r.data),
  ratholeConfigScript: (id: string) =>
    client.get<string>(`/machines/${id}/rathole-config?format=script`, { responseType: 'text', transformResponse: r => r }).then(r => r.data),
  ratholeConfigUrl: (id: string, format?: 'script') =>
    `/api/machines/${id}/rathole-config${format ? `?format=${format}` : ''}`,
}
