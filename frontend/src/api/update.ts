import client from './client'

export interface UpdateInfo {
  current_version: string
  latest_version: string
  update_available: boolean
}

export const updateApi = {
  check: () =>
    client.get<{ data: UpdateInfo }>('/update/check').then(r => r.data.data),
  apply: () =>
    client.post('/update/apply').then(r => r.data),
}
