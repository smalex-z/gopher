import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import {
  Server, Globe, Shield, Key, ArrowRight,
  ShieldCheck, ShieldOff, Lock, Wifi, WifiOff, Network, Monitor,
} from 'lucide-react'
import { machinesApi } from '../api/machines'
import { tunnelsApi } from '../api/tunnels'
import { localApi, type ActivityEvent } from '../api/local'
import { securityApi } from '../api/security'
import { updateApi } from '../api/update'
import client from '../api/client'
import type { Machine, Tunnel, SSHKey, FirewallEntry } from '../types'

// ─── Tiny helpers ─────────────────────────────────────────────────────────────

function Dot({ status }: { status: string }) {
  const s = status.toLowerCase()
  const c =
    s === 'active' || s === 'connected' ? 'bg-green-500' :
    s === 'pending' || s === 'connecting' ? 'bg-yellow-400' :
    s === 'failed' || s === 'error' ? 'bg-red-500' :
    'bg-gray-300'
  return <span className={`inline-block w-1.5 h-1.5 rounded-full shrink-0 ${c}`} />
}

function Chip({ label, color }: { label: string; color: 'green' | 'red' | 'yellow' | 'gray' | 'blue' }) {
  const styles = {
    green:  'bg-green-50 text-green-700 border-green-200',
    red:    'bg-red-50 text-red-600 border-red-200',
    yellow: 'bg-yellow-50 text-yellow-700 border-yellow-200',
    gray:   'bg-gray-100 text-gray-500 border-gray-200',
    blue:   'bg-blue-50 text-blue-700 border-blue-200',
  }
  return (
    <span className={`inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium border ${styles[color]}`}>
      {label}
    </span>
  )
}

interface CardProps {
  title: string
  icon: React.ReactNode
  count?: number | string
  href: string
  children: React.ReactNode
}

function Card({ title, icon, count, href, children }: CardProps) {
  const navigate = useNavigate()
  return (
    <div className="bg-white rounded-xl border border-gray-200 flex flex-col overflow-hidden">
      <div className="flex items-center justify-between px-4 pt-4 pb-3 border-b border-gray-100">
        <div className="flex items-center gap-2">
          <span className="text-gray-400">{icon}</span>
          <span className="text-sm font-semibold text-gray-800">{title}</span>
          {count !== undefined && (
            <span className="text-xs font-medium bg-gray-100 text-gray-500 px-1.5 py-0.5 rounded-full">{count}</span>
          )}
        </div>
        <button
          onClick={() => navigate(href)}
          className="text-xs text-gray-400 hover:text-blue-600 flex items-center gap-0.5 transition-colors"
        >
          Manage <ArrowRight size={11} />
        </button>
      </div>
      <div className="px-4 py-3 flex-1 space-y-2 text-sm">
        {children}
      </div>
    </div>
  )
}

function EmptyState({ label }: { label: string }) {
  return <p className="text-xs text-gray-400 italic">{label}</p>
}

// ─── Dashboard ────────────────────────────────────────────────────────────────

export default function DashboardPage() {
  const navigate = useNavigate()

  const { data: localStatus } = useQuery({
    queryKey: ['local-status'],
    queryFn: () => localApi.status(),
    refetchInterval: 15000,
  })
  const { data: machinesData } = useQuery({
    queryKey: ['machines'],
    queryFn: () => machinesApi.list(),
    refetchInterval: 20000,
  })
  const { data: tunnelsData } = useQuery({
    queryKey: ['tunnels'],
    queryFn: () => tunnelsApi.list(),
    refetchInterval: 20000,
  })
  const { data: keysData } = useQuery({
    queryKey: ['ssh-keys'],
    queryFn: () => localApi.listSSHKeys(),
  })
  const { data: firewallData } = useQuery({
    queryKey: ['firewall-overview'],
    queryFn: () => localApi.firewallOverview(),
  })
  const { data: fail2ban } = useQuery({
    queryKey: ['fail2ban-status'],
    queryFn: () => securityApi.fail2banStatus(),
  })
  const { data: totpData } = useQuery({
    queryKey: ['totp-status'],
    queryFn: () => client.get<{ data: { enabled: boolean } }>('/auth/2fa/status').then(r => r.data.data),
  })
  const { data: updateInfo } = useQuery({
    queryKey: ['update-check'],
    queryFn: () => updateApi.check(),
    staleTime: 60_000,
  })

  const { data: activityData } = useQuery({
    queryKey: ['activity'],
    queryFn: () => localApi.activity(),
    refetchInterval: 30_000,
  })

  const machines: Machine[]       = machinesData?.data ?? []
  const tunnels: Tunnel[]         = tunnelsData?.data ?? []
  const keys: SSHKey[]            = keysData?.data ?? []
  const fwEntries: FirewallEntry[] = firewallData?.data ?? []

  const caddyOk    = localStatus?.caddy_active === 'active'
  const ratholeOk  = localStatus?.rathole_active === 'active'
  const domain     = localStatus?.domain ?? ''
  const fwMode     = localStatus?.firewall_mode ?? ''

  const connected   = machines.filter(m => m.status === 'active' || m.status === 'connected').length
  const activeTuns  = tunnels.filter(t => t.status === 'active').length
  const defaultKey  = keys.find(k => k.is_default)
  const bannedCount = (fail2ban?.jails ?? []).reduce((n, j) => n + j.currently_banned, 0)
  const customRules = fwEntries.filter(e => e.type === 'custom').length
  const openPorts   = fwEntries.filter(e => e.action === 'ACCEPT').length

  const fwModeColor = fwMode === 'gopher' ? 'green' : fwMode === 'manual' ? 'yellow' : 'gray'
  const fwModeLabel = fwMode === 'gopher' ? 'Gopher managed' : fwMode === 'manual' ? 'Manual' : fwMode === 'none' ? 'Disabled' : 'Not configured'

  const tunnelURL = (t: Tunnel) => {
    if (t.subdomain && domain) return `${t.subdomain}.${domain}`
    return `:${t.rathole_port}`
  }

  return (
    <div className="space-y-4">

      {/* ── Top bar: system health ──────────────────────────────────────── */}
      <div
        className="bg-white rounded-xl border border-gray-200 px-5 py-3.5 flex items-center gap-4 flex-wrap cursor-pointer hover:border-gray-300 transition-colors"
        onClick={() => navigate('/vps')}
      >
        <span className="text-xs font-semibold text-gray-400 uppercase tracking-wider">System</span>

        <div className="flex items-center gap-1.5">
          {caddyOk ? <Wifi size={13} className="text-green-500" /> : <WifiOff size={13} className="text-red-400" />}
          <span className={`text-xs font-medium ${caddyOk ? 'text-green-700' : 'text-red-600'}`}>Caddy</span>
        </div>

        <div className="flex items-center gap-1.5">
          {ratholeOk ? <Wifi size={13} className="text-green-500" /> : <WifiOff size={13} className="text-red-400" />}
          <span className={`text-xs font-medium ${ratholeOk ? 'text-green-700' : 'text-red-600'}`}>Rathole</span>
        </div>

        {domain && (
          <>
            <div className="w-px h-4 bg-gray-200" />
            <div className="flex items-center gap-1.5">
              <Globe size={12} className="text-gray-400" />
              <code className="text-xs text-gray-700">{domain}</code>
            </div>
          </>
        )}

        {updateInfo?.update_available && (
          <>
            <div className="w-px h-4 bg-gray-200" />
            <Chip label={`Update available: ${updateInfo.latest_version}`} color="blue" />
          </>
        )}

        <ArrowRight size={13} className="text-gray-300 ml-auto" />
      </div>

      {/* ── Main grid ──────────────────────────────────────────────────── */}
      <div className="grid grid-cols-2 gap-4">

        {/* Machines */}
        <Card title="Machines" icon={<Server size={15} />} count={`${connected}/${machines.length}`} href="/machines">
          {machines.length === 0
            ? <EmptyState label="No machines registered yet" />
            : machines.slice(0, 5).map(m => (
              <div key={m.id} className="flex items-center gap-2">
                <Dot status={m.status} />
                <span className="text-gray-700 text-xs font-medium flex-1 truncate">{m.name}</span>
                <span className="text-xs text-gray-400 capitalize shrink-0">{m.status}</span>
              </div>
            ))
          }
          {machines.length > 5 && (
            <p className="text-xs text-gray-400">+{machines.length - 5} more</p>
          )}
        </Card>

        {/* Tunnels */}
        <Card title="Service Tunnels" icon={<Globe size={15} />} count={`${activeTuns}/${tunnels.length} active`} href="/tunnels">
          {tunnels.length === 0
            ? <EmptyState label="No service tunnels configured yet" />
            : tunnels.slice(0, 5).map(t => (
              <div key={t.id} className="flex items-center gap-2">
                <Dot status={t.status} />
                {t.private && <Lock size={10} className="text-gray-300 shrink-0" />}
                <span className="text-gray-700 text-xs font-medium flex-1 truncate">{t.name}</span>
                <code className="text-xs text-gray-400 truncate shrink-0 max-w-[140px]">{tunnelURL(t)}</code>
              </div>
            ))
          }
          {tunnels.length > 5 && (
            <p className="text-xs text-gray-400">+{tunnels.length - 5} more</p>
          )}
        </Card>

      </div>

      {/* ── Bottom grid ────────────────────────────────────────────────── */}
      <div className="grid grid-cols-3 gap-4">

        {/* SSH Keys */}
        <Card title="SSH Keys" icon={<Key size={15} />} count={keys.length} href="/keys">
          {keys.length === 0
            ? <EmptyState label="No keys stored" />
            : <>
              {defaultKey && (
                <div className="flex items-center gap-2">
                  <Key size={11} className="text-blue-500 shrink-0" />
                  <span className="text-xs text-gray-700 font-medium flex-1 truncate">{defaultKey.name}</span>
                  <Chip label="Default" color="blue" />
                </div>
              )}
              {keys.filter(k => !k.is_default).slice(0, 3).map(k => (
                <div key={k.id} className="flex items-center gap-2">
                  <Key size={11} className="text-gray-300 shrink-0" />
                  <span className="text-xs text-gray-500 truncate">{k.name}</span>
                  {(k.machine_count ?? 0) > 0 && (
                    <span className="text-xs text-gray-400 shrink-0">{k.machine_count}m</span>
                  )}
                </div>
              ))}
              {keys.length > 4 && <p className="text-xs text-gray-400">+{keys.length - 4} more</p>}
            </>
          }
        </Card>

        {/* Firewall */}
        <Card title="Firewall" icon={<Shield size={15} />} href="/firewall">
          <div className="flex items-center gap-2">
            <Chip label={fwModeLabel} color={fwModeColor as 'green' | 'yellow' | 'gray'} />
          </div>
          {fwMode === 'gopher' && (
            <>
              <div className="flex items-center justify-between text-xs">
                <span className="text-gray-400">Open ports</span>
                <span className="font-medium text-gray-700">{openPorts}</span>
              </div>
              <div className="flex items-center justify-between text-xs">
                <span className="text-gray-400">Custom rules</span>
                <span className="font-medium text-gray-700">{customRules}</span>
              </div>
            </>
          )}
          {fwMode !== 'gopher' && fwMode !== '' && (
            <p className="text-xs text-gray-400">Gopher is not managing the firewall</p>
          )}
        </Card>

        {/* Access Control */}
        <Card title="Access Control" icon={<ShieldCheck size={15} />} href="/security">
          {/* 2FA */}
          <div className="flex items-center justify-between">
            <span className="text-xs text-gray-500">Two-factor auth</span>
            {totpData?.enabled
              ? <Chip label="Enabled" color="green" />
              : <Chip label="Disabled" color="red" />
            }
          </div>

          {/* Fail2ban */}
          <div className="flex items-center justify-between">
            <span className="text-xs text-gray-500">Fail2ban</span>
            {fail2ban?.running
              ? <Chip label="Active" color="green" />
              : <Chip label="Inactive" color="gray" />
            }
          </div>

          {/* Banned IPs */}
          {fail2ban?.running && (
            <div className="flex items-center justify-between">
              <span className="text-xs text-gray-500">Banned IPs</span>
              <span className={`text-xs font-semibold ${bannedCount > 0 ? 'text-red-500' : 'text-gray-400'}`}>
                {bannedCount > 0 ? bannedCount : 'None'}
              </span>
            </div>
          )}

          {!fail2ban?.available && (
            <div className="flex items-center gap-1.5 text-xs text-amber-600">
              <ShieldOff size={11} />
              Fail2ban not installed
            </div>
          )}
        </Card>

      </div>
      {/* Network map link */}
      <button
        onClick={() => navigate('/network')}
        className="w-full bg-white rounded-xl border border-gray-200 px-5 py-3 flex items-center justify-between hover:border-blue-200 hover:shadow-sm transition-all group"
      >
        <div className="flex items-center gap-2">
          <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" className="text-gray-400">
            <circle cx="3" cy="8" r="2"/>
            <circle cx="13" cy="3" r="2"/>
            <circle cx="13" cy="13" r="2"/>
            <line x1="5" y1="7.3" x2="11" y2="3.7"/>
            <line x1="5" y1="8.7" x2="11" y2="12.3"/>
          </svg>
          <span className="text-sm font-medium text-gray-600">Network Map</span>
          <span className="text-xs text-gray-400">— visual overview of all tunnels and machines</span>
        </div>
        <ArrowRight size={14} className="text-gray-300 group-hover:text-blue-500 transition-colors" />
      </button>
      {/* ── Activity Timeline ──────────────────────────────────────────── */}
      {activityData && activityData.length > 0 && (
        <div className="bg-white rounded-xl border border-gray-200 px-5 py-4">
          <div className="mb-3">
            <span className="text-sm font-semibold text-gray-700">Recent Activity</span>
          </div>
          <div className="space-y-2.5 max-h-48 overflow-y-auto">
            {activityData.slice(0, 8).map((ev: ActivityEvent) => {
              const isMachine = ev.kind === 'machine_registered' || ev.kind === 'machine_deleted'
              const isDelete = ev.kind === 'machine_deleted' || ev.kind === 'tunnel_deleted'
              const label =
                ev.kind === 'machine_registered' ? 'registered' :
                ev.kind === 'machine_deleted'    ? 'deleted' :
                ev.kind === 'tunnel_created'     ? 'created' : 'deleted'
              return (
                <div key={ev.id} className="flex items-center gap-3">
                  <div className={`w-1.5 h-1.5 rounded-full shrink-0 ${isDelete ? 'bg-red-400' : isMachine ? 'bg-blue-400' : 'bg-green-400'}`} />
                  <div className="flex items-center gap-1.5 flex-1 min-w-0">
                    {isMachine ? <Monitor size={11} className="text-gray-400 shrink-0" /> : <Network size={11} className="text-gray-400 shrink-0" />}
                    <span className="text-xs text-gray-700 truncate">
                      {isMachine ? 'Machine' : 'Tunnel'} <span className="font-medium">{ev.name}</span> {label}
                    </span>
                  </div>
                  <span className="text-xs text-gray-400 shrink-0">{new Date(ev.created_at).toLocaleDateString()}</span>
                </div>
              )
            })}
          </div>
        </div>
      )}

    </div>
  )
}
