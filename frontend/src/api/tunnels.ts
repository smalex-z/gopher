import client from './client'
import type { Tunnel, HealthSummary, ApiResponse } from '../types'

export const tunnelsApi = {
  list: () => client.get<ApiResponse<Tunnel[]>>('/tunnels/').then(r => r.data),
  nextPort: () => client.get<ApiResponse<{ port: number }>>('/tunnels/next-port').then(r => r.data.data?.port ?? 1024),
  checkPort: (port: number) =>
    client.get<ApiResponse<{ available: boolean; reason: string }>>(`/tunnels/port-check?port=${port}`)
      .then(r => r.data.data ?? { available: true, reason: '' }),
  get: (id: string) => client.get<ApiResponse<Tunnel>>(`/tunnels/${id}`).then(r => r.data),
  create: (data: Partial<Tunnel>) => client.post<ApiResponse<Tunnel>>('/tunnels/', data).then(r => r.data),
  update: (id: string, data: Partial<Tunnel>) => client.put<ApiResponse<Tunnel>>(`/tunnels/${id}`, data).then(r => r.data),
  delete: (id: string) => client.delete(`/tunnels/${id}`).then(r => r.data),
  test: (id: string) => client.post(`/tunnels/${id}/test`).then(r => r.data),
  health: (id: string) =>
    client.get<ApiResponse<HealthSummary>>(`/tunnels/${id}/health`).then(r => r.data.data),
}
