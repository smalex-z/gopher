import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Copy, Check, ExternalLink, RefreshCw, Globe, Lock, ShieldAlert, Server, Network, Activity, AlertTriangle } from 'lucide-react'
import { localApi } from '../api/local'
import { updateApi, type UpdateChannel } from '../api/update'
import { machinesApi } from '../api/machines'
import { tunnelsApi } from '../api/tunnels'
import StatusBadge from '../components/StatusBadge'
import { toast } from '../lib/toast'

function ServiceRow({ label, active }: { label: string; active: string }) {
  const color =
    active === 'active' ? 'bg-green-100 text-green-700' :
    active === 'activating' ? 'bg-yellow-100 text-yellow-700' :
    active === 'not-found' ? 'bg-gray-100 text-gray-500' :
    'bg-red-100 text-red-700'
  return (
    <div className="flex items-center justify-between py-2">
      <span className="text-sm text-gray-700">{label}</span>
      <span className={`text-xs font-medium px-2 py-0.5 rounded-full ${color}`}>{active}</span>
    </div>
  )
}

export default function ServerPage() {
  const qc = useQueryClient()
  const [keyCopied, setKeyCopied] = useState(false)
  const [bindIPInput, setBindIPInput] = useState<string | null>(null)

  const { data: status, refetch, isLoading } = useQuery({
    queryKey: ['local-status'],
    queryFn: () => localApi.status(),
    refetchInterval: 15000,
  })
  const { data: updateInfo } = useQuery({
    queryKey: ['update-check'],
    queryFn: () => updateApi.check(),
    staleTime: 60_000,
  })
  const { data: machinesData } = useQuery({
    queryKey: ['machines'],
    queryFn: () => machinesApi.list(),
    refetchInterval: 30000,
  })
  const { data: tunnelsData } = useQuery({
    queryKey: ['tunnels'],
    queryFn: () => tunnelsApi.list(),
    refetchInterval: 30000,
  })

  const dashboardMutation = useMutation({
    mutationFn: (dashboardPrivate: boolean) => localApi.setServerPorts(dashboardPrivate),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['local-status'] }),
    onError: (e: Error) => toast.error(e.message),
  })

  const bindIPMutation = useMutation({
    mutationFn: (ip: string) => localApi.setBindIP(ip),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['local-status'] })
      setBindIPInput(null)
      toast.success('Bind IP updated. Restart Gopher for the dashboard port to rebind.')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const channelMutation = useMutation({
    mutationFn: (channel: UpdateChannel) => updateApi.setChannel(channel),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['update-check'] }),
    onError: (e: Error) => toast.error(e.message),
  })

  const copyKey = () => {
    if (!status?.ssh_public_key) return
    navigator.clipboard.writeText(status.ssh_public_key).then(() => {
      setKeyCopied(true)
      setTimeout(() => setKeyCopied(false), 2000)
    })
  }

  if (isLoading) return <div className="text-gray-400 text-center py-12">Loading…</div>

  const machines = machinesData?.data ?? []
  const tunnels = tunnelsData?.data ?? []
  const bothActive = status?.caddy_active === 'active' && status?.rathole_active === 'active'
  const connectedMachines = machines.filter(m => m.status === 'active' || m.status === 'connected').length
  const activeTunnels = tunnels.filter(t => t.status === 'active').length

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-gray-900">This Server</h1>
          <p className="text-gray-500 mt-1">Gopher is running here — local services and SSH access</p>
        </div>
        <button onClick={() => refetch()} className="p-2 text-gray-400 hover:text-gray-600 rounded-lg hover:bg-gray-100" title="Refresh">
          <RefreshCw size={16} />
        </button>
      </div>

      <div className="grid grid-cols-3 gap-6">
        {/* Left column — main panels */}
        <div className="col-span-2 space-y-6">

          {/* Domain + overall status */}
          <div className="bg-white rounded-xl shadow-sm border p-6">
            <div className="flex items-center gap-3 mb-4">
              <StatusBadge status={bothActive ? 'active' : 'inactive'} />
              <span className="font-semibold text-gray-900">{status?.domain || 'No domain configured'}</span>
            </div>
            {status?.domain && (
              <a
                href={`https://router.${status.domain}`}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-1.5 text-sm text-blue-600 hover:underline"
              >
                <ExternalLink size={14} />
                https://router.{status.domain}
              </a>
            )}
            {!status?.domain && (
              <p className="text-sm text-amber-600">
                Domain not configured. Go through the setup wizard to set one.
              </p>
            )}
          </div>

          {/* Service status */}
          <div className="bg-white rounded-xl shadow-sm border p-6">
            <h2 className="text-base font-semibold text-gray-900 mb-3">Systemd Services</h2>
            <div className="divide-y">
              <ServiceRow label="caddy.service (reverse proxy)" active={status?.caddy_installed ? (status.caddy_active || 'unknown') : 'not-installed'} />
              <ServiceRow label="rathole-server.service (tunnel server)" active={status?.rathole_installed ? (status.rathole_active || 'unknown') : 'not-installed'} />
            </div>
          </div>

          {/* Port access */}
          <div className="bg-white rounded-xl shadow-sm border p-6">
            <h2 className="text-base font-semibold text-gray-900 mb-4">Port Access</h2>
            <div className="space-y-4">
              <div className="flex items-start justify-between gap-4">
                <div>
                  <div className="text-sm font-medium text-gray-800">Port 22 — VPS SSH</div>
                  <div className="text-xs text-gray-500 mt-0.5">
                    Always open. Required for server management — closing it via iptables risks permanent lockout.
                    To restrict by IP, use the Firewall tab custom rules (e.g. <code className="bg-gray-100 px-1 rounded">-s 1.2.3.4 -p tcp --dport 22 -j ACCEPT</code> then drop all others).
                  </div>
                </div>
                <span className="shrink-0 text-xs font-medium px-2 py-1 rounded-full bg-green-100 text-green-700 flex items-center gap-1">
                  <Globe size={11} /> Public
                </span>
              </div>

              <div className="flex items-start justify-between gap-4">
                <div>
                  <div className="text-sm font-medium text-gray-800">Ports 80 / 443 — HTTP/HTTPS</div>
                  <div className="text-xs text-gray-500 mt-0.5">Always open when Caddy is configured.</div>
                </div>
                <span className="shrink-0 text-xs font-medium px-2 py-1 rounded-full bg-green-100 text-green-700 flex items-center gap-1">
                  <Globe size={11} /> Public
                </span>
              </div>

              <div className="flex items-start justify-between gap-4">
                <div>
                  <div className="text-sm font-medium text-gray-800">Port {status?.dashboard_port ?? 4321} — Gopher dashboard</div>
                  <div className="text-xs text-gray-500 mt-0.5">
                    {status?.dashboard_private
                      ? <>Restricted to localhost. Access via <code className="bg-gray-100 px-1 rounded">router.{status.domain}</code>.</>
                      : 'Publicly accessible. Anyone who knows the port can reach the dashboard login.'}
                  </div>
                  {!status?.domain && (
                    <div className="text-xs text-amber-600 mt-0.5 flex items-center gap-1">
                      <ShieldAlert size={11} /> No domain configured — dashboard port must stay public.
                    </div>
                  )}
                </div>
                <div className="shrink-0 flex gap-1.5">
                  <button
                    onClick={() => dashboardMutation.mutate(false)}
                    disabled={dashboardMutation.isPending || !status?.dashboard_private}
                    className={`text-xs font-medium px-2.5 py-1 rounded-lg border transition-colors flex items-center gap-1 ${
                      !status?.dashboard_private
                        ? 'bg-green-600 text-white border-green-600'
                        : 'bg-white text-gray-600 border-gray-300 hover:bg-gray-50 disabled:opacity-50'
                    }`}
                  >
                    <Globe size={11} /> Public
                  </button>
                  <button
                    onClick={() => dashboardMutation.mutate(true)}
                    disabled={dashboardMutation.isPending || status?.dashboard_private || !status?.domain || status?.firewall_mode !== 'gopher'}
                    title={!status?.domain ? 'Requires domain + Caddy' : status?.firewall_mode !== 'gopher' ? 'Requires Gopher-managed firewall' : undefined}
                    className={`text-xs font-medium px-2.5 py-1 rounded-lg border transition-colors flex items-center gap-1 ${
                      status?.dashboard_private
                        ? 'bg-slate-700 text-white border-slate-700'
                        : 'bg-white text-gray-600 border-gray-300 hover:bg-gray-50 disabled:opacity-50 disabled:cursor-not-allowed'
                    }`}
                  >
                    <Lock size={11} /> Caddy only
                  </button>
                </div>
              </div>
            </div>

            {/* Bind IP */}
            <div className="mt-5 pt-4 border-t">
              <div className="flex items-start justify-between gap-4 mb-3">
                <div>
                  <div className="text-sm font-medium text-gray-800">Bind IP</div>
                  <div className="text-xs text-gray-500 mt-0.5">
                    Restrict all public listeners (rathole ports, Caddy, dashboard) to a single IP.
                    Useful on multi-homed hosts so other services can share the same ports on a different IP.
                  </div>
                  {!status?.bind_ip && (status?.host_ips?.length ?? 0) > 1 && (
                    <div className="flex items-center gap-1 text-xs text-amber-600 mt-1">
                      <AlertTriangle size={11} />
                      Multiple IPs detected ({status!.host_ips.join(', ')}) — Gopher is listening on all of them (0.0.0.0).
                    </div>
                  )}
                </div>
                <span className="shrink-0 text-xs font-mono font-medium px-2 py-1 rounded-full bg-gray-100 text-gray-600">
                  {status?.bind_ip || '0.0.0.0'}
                </span>
              </div>
              <div className="flex gap-2 items-center">
                <input
                  type="text"
                  placeholder={status?.bind_ip || '0.0.0.0 (all interfaces)'}
                  value={bindIPInput ?? (status?.bind_ip || '')}
                  onChange={e => setBindIPInput(e.target.value)}
                  className="flex-1 text-sm border border-gray-300 rounded-lg px-3 py-1.5 font-mono focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
                <button
                  onClick={() => bindIPMutation.mutate(bindIPInput ?? '')}
                  disabled={bindIPMutation.isPending || bindIPInput === null}
                  className="text-xs font-medium px-3 py-1.5 rounded-lg bg-blue-600 text-white hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
                >
                  {bindIPMutation.isPending ? 'Saving…' : 'Save'}
                </button>
                {(status?.bind_ip || bindIPInput) && (
                  <button
                    onClick={() => { setBindIPInput(''); bindIPMutation.mutate('') }}
                    disabled={bindIPMutation.isPending}
                    className="text-xs font-medium px-3 py-1.5 rounded-lg border border-gray-300 text-gray-600 hover:bg-gray-50 disabled:opacity-50 transition-colors"
                  >
                    Clear
                  </button>
                )}
              </div>
              {status?.bind_ip && (
                <p className="text-xs text-amber-600 mt-1.5 flex items-center gap-1">
                  <AlertTriangle size={11} />
                  Dashboard port rebind requires a Gopher service restart.
                </p>
              )}
            </div>
          </div>

        </div>

        {/* Right column */}
        <div className="space-y-6">

          {/* Quick stats */}
          <div className="bg-white rounded-xl shadow-sm border p-5 space-y-4">
            <h2 className="text-base font-semibold text-gray-900">Overview</h2>
            <div className="space-y-3">
              <div className="flex items-center gap-3">
                <div className="p-2 bg-blue-50 rounded-lg">
                  <Server size={14} className="text-blue-500" />
                </div>
                <div>
                  <div className="text-xs text-gray-400">Machines</div>
                  <div className="text-sm font-semibold text-gray-800">{connectedMachines} / {machines.length} connected</div>
                </div>
              </div>
              <div className="flex items-center gap-3">
                <div className="p-2 bg-green-50 rounded-lg">
                  <Network size={14} className="text-green-500" />
                </div>
                <div>
                  <div className="text-xs text-gray-400">Tunnels</div>
                  <div className="text-sm font-semibold text-gray-800">{activeTunnels} / {tunnels.length} active</div>
                </div>
              </div>
              {status?.os_user && (
                <div className="flex items-center gap-3">
                  <div className="p-2 bg-gray-50 rounded-lg">
                    <Activity size={14} className="text-gray-400" />
                  </div>
                  <div>
                    <div className="text-xs text-gray-400">Running as</div>
                    <div className="text-sm font-semibold text-gray-800 font-mono">{status.os_user}</div>
                  </div>
                </div>
              )}
            </div>
          </div>

          {/* Version */}
          {updateInfo && (
            <div className="bg-white rounded-xl shadow-sm border p-5">
              <h2 className="text-base font-semibold text-gray-900 mb-3">Gopher Version</h2>
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <span className="text-xs text-gray-400">Installed</span>
                  <span className="text-sm font-mono text-gray-700">{updateInfo.current_version}</span>
                </div>
                {updateInfo.update_available ? (
                  <div className="flex items-center justify-between">
                    <span className="text-xs text-gray-400">Available</span>
                    <span className="text-xs text-emerald-600 bg-emerald-50 border border-emerald-200 px-2 py-0.5 rounded-full">
                      {updateInfo.latest_version}
                    </span>
                  </div>
                ) : (
                  <p className="text-xs text-gray-400">Up to date</p>
                )}
                <div className="pt-1 border-t">
                  <div className="text-xs text-gray-400 mb-1.5">Update channel</div>
                  <div className="flex gap-1">
                    {(['stable', 'beta', 'alpha'] as UpdateChannel[]).map(ch => (
                      <button
                        key={ch}
                        onClick={() => channelMutation.mutate(ch)}
                        disabled={channelMutation.isPending}
                        className={`flex-1 text-xs font-medium py-1 rounded-lg border transition-colors capitalize ${
                          updateInfo.channel === ch
                            ? ch === 'stable'
                              ? 'bg-blue-600 text-white border-blue-600'
                              : ch === 'beta'
                              ? 'bg-amber-500 text-white border-amber-500'
                              : 'bg-purple-600 text-white border-purple-600'
                            : 'bg-white text-gray-600 border-gray-300 hover:bg-gray-50 disabled:opacity-50'
                        }`}
                      >
                        {ch}
                      </button>
                    ))}
                  </div>
                </div>
              </div>
            </div>
          )}

          {/* SSH public key — collapsed by default, for advanced use */}
          {status?.ssh_public_key && (
            <div className="bg-white rounded-xl shadow-sm border p-5">
              <div className="flex items-center justify-between mb-2">
                <h2 className="text-sm font-semibold text-gray-700">Server Public Key</h2>
                <button onClick={copyKey} className="flex items-center gap-1 text-xs text-gray-400 hover:text-gray-700">
                  {keyCopied ? <Check size={13} className="text-green-500" /> : <Copy size={13} />}
                  {keyCopied ? 'Copied' : 'Copy'}
                </button>
              </div>
              <p className="text-xs text-gray-400 mb-2">Added to bootstrapped machines' <code className="bg-gray-100 px-0.5 rounded">authorized_keys</code> automatically.</p>
              <div className="bg-gray-50 border border-gray-200 rounded-lg px-2 py-1.5 text-xs font-mono text-gray-500 break-all leading-relaxed">
                {status.ssh_public_key.slice(0, 60)}…
              </div>
            </div>
          )}

        </div>
      </div>
    </div>
  )
}
