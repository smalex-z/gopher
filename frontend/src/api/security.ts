import client from './client'

export interface AuditEvent {
  type: string
  ip: string
  message: string
  time: string
}

export interface JailStatus {
  name: string
  currently_failed: number
  total_failed: number
  currently_banned: number
  total_banned: number
  banned_ips: string[]
}

export interface Fail2banStatus {
  available: boolean
  running: boolean
  jails: JailStatus[]
}

export interface Fail2banConfig {
  max_retry: number
  find_time: number
  ban_time: number
  ignore_ips: string[]
}

export const securityApi = {
  auditLog: () =>
    client.get<{ data: AuditEvent[] }>('/security/logs').then(r => r.data.data),
  fail2banStatus: () =>
    client.get<{ data: Fail2banStatus }>('/security/fail2ban').then(r => r.data.data),
  unbanIP: (jail: string, ip: string) =>
    client.post('/security/fail2ban/unban', { jail, ip }).then(r => r.data),
  getFail2banConfig: () =>
    client.get<{ data: Fail2banConfig }>('/security/fail2ban/config').then(r => r.data.data),
  saveFail2banConfig: (cfg: { max_retry: number; find_time: number; ban_time: number }) =>
    client.put('/security/fail2ban/config', cfg).then(r => r.data),
  addWhitelistIP: (ip: string) =>
    client.post('/security/fail2ban/whitelist', { ip }).then(r => r.data),
  removeWhitelistIP: (ip: string) =>
    client.delete(`/security/fail2ban/whitelist/${encodeURIComponent(ip)}`).then(r => r.data),
}
