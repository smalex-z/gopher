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
}
