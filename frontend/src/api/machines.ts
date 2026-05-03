import client from './client'
import type { Machine, HealthCheck, ApiResponse } from '../types'

export const machinesApi = {
  list: () => client.get<ApiResponse<Machine[]>>('/machines/').then(r => r.data),
  get: (id: string) => client.get<ApiResponse<Machine>>(`/machines/${id}`).then(r => r.data),
  create: (data: Partial<Machine>) => client.post<ApiResponse<Machine>>('/machines/', data).then(r => r.data),
  update: (id: string, data: Partial<Machine>) => client.put<ApiResponse<Machine>>(`/machines/${id}`, data).then(r => r.data),
  delete: (id: string) => client.delete(`/machines/${id}`).then(r => r.data),
  deploy: (id: string) => client.post(`/machines/${id}/deploy`).then(r => r.data),
  status: (id: string) => client.get(`/machines/${id}/status`).then(r => r.data),
  networkInfo: (id: string) =>
    client.get<{ data: { id: string; public_ip: string; private_ip: string; is_nat: boolean; error?: string } }>(
      `/machines/${id}/network-info`
    ).then(r => r.data.data),
  reassignSSHKey: (id: string, sshKeyID: string) => client.put(`/machines/${id}/ssh-key`, { ssh_key_id: sshKeyID }).then(r => r.data),
  // gopher-agent / health endpoints
  pendingAgents: () => client.get<ApiResponse<Machine[]>>('/machines/agent/pending').then(r => r.data),
  installAgent: (id: string) => client.post<ApiResponse<Machine>>(`/machines/${id}/install-agent`).then(r => r.data),
  health: (id: string) =>
    client.get<{ data: { latest: HealthCheck | null; recent: HealthCheck[] } }>(`/machines/${id}/health`).then(r => r.data.data),
  runCheck: (id: string) =>
    client.post<{ data: { check: HealthCheck; now: string } }>(`/machines/${id}/health/check`).then(r => r.data.data),
}
