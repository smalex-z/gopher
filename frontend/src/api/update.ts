import client from './client'

export type UpdateChannel = 'stable' | 'beta' | 'alpha'

export interface UpdateInfo {
  current_version: string
  latest_version: string
  update_available: boolean
  channel: UpdateChannel
}

export const updateApi = {
  check: () =>
    client.get<{ data: UpdateInfo }>('/update/check').then(r => r.data.data),
  apply: () =>
    client.post('/update/apply').then(r => r.data),
  setChannel: (channel: UpdateChannel) =>
    client.post('/update/channel', { channel }).then(r => r.data),
}
