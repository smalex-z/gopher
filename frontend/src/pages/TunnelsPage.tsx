import React, { useState, useEffect } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { Network, ClipboardCopy, ArrowRight, Globe, Lock, Terminal, Info, AlertTriangle } from 'lucide-react'
import { tunnelsApi } from '../api/tunnels'
import { machinesApi } from '../api/machines'
import { localApi } from '../api/local'
import StatusBadge from '../components/StatusBadge'
import { toast } from '../lib/toast'
import type { Tunnel } from '../types'

interface ModalState { isOpen: boolean }
interface FormState {
  machine_id: string
  name: string
  subdomain: string
  local_port: number
  rathole_port: number
  transport: string
  no_tls: boolean
  private: boolean
  bot_protection_enabled: boolean
  bot_protection_ttl: number    // stored as seconds; 0 = default
  bot_protection_allow_ip: string // newline-delimited in the textarea, JSON on wire
}

const defaultForm: FormState = {
  machine_id: '', name: '', subdomain: '', local_port: 3000, rathole_port: 0,
  transport: 'tcp', no_tls: false, private: false,
  bot_protection_enabled: false, bot_protection_ttl: 0, bot_protection_allow_ip: '',
}

function cidrToJSON(raw: string): string {
  const entries = raw.split('\n').map(s => s.trim()).filter(Boolean)
  return entries.length > 0 ? JSON.stringify(entries) : ''
}

function allowIPDisplay(json: string): string {
  if (!json) return ''
  try { return (JSON.parse(json) as string[]).join('\n') } catch { return json }
}

export default function TunnelsPage() {
  const qc = useQueryClient()
  const [searchParams] = useSearchParams()
  const [modal, setModal] = useState<ModalState>({ isOpen: false })
  const [form, setForm] = useState<FormState>(defaultForm)
  const [nextPortLoading, setNextPortLoading] = useState(false)

  const openAddModal = async () => {
    setNextPortLoading(true)
    try {
      const port = await tunnelsApi.nextPort()
      setForm({ ...defaultForm, rathole_port: port })
    } catch {
      setForm(defaultForm)
    } finally {
      setNextPortLoading(false)
    }
    setModal({ isOpen: true })
  }

  const { data: tunnelsData, isLoading } = useQuery({ queryKey: ['tunnels'], queryFn: () => tunnelsApi.list() })
  const { data: machinesData } = useQuery({ queryKey: ['machines'], queryFn: () => machinesApi.list() })
  const { data: localStatus } = useQuery({ queryKey: ['local-status'], queryFn: () => localApi.status() })

  const tunnels: Tunnel[] = tunnelsData?.data ?? []
  const machines = machinesData?.data ?? []
  const domain = localStatus?.domain
  const routingEnabled = Boolean(domain)

  const { data: domainIPData } = useQuery({
    queryKey: ['resolve-ip', domain ?? ''],
    queryFn: () => localApi.resolveIP(domain!),
    enabled: !!domain,
    staleTime: 10 * 60 * 1000,
  })
  const { data: routerIPData } = useQuery({
    queryKey: ['resolve-ip', domain ? `router.${domain}` : ''],
    queryFn: () => localApi.resolveIP(`router.${domain}`),
    enabled: !!domain,
    staleTime: 10 * 60 * 1000,
  })
  const domainIP = domainIPData?.ip ?? ''
  const routerIP = routerIPData?.ip ?? ''
  const displayHost = domain
    ? (domainIP && routerIP && domainIP === routerIP ? domain : `router.${domain}`)
    : undefined

  useEffect(() => {
    const machineId = searchParams.get('machine')
    if (machineId) {
      setForm(f => ({ ...f, machine_id: machineId }))
      setModal({ isOpen: true })
    }
  }, [searchParams])

  const createMutation = useMutation({
    mutationFn: (d: Partial<FormState>) => tunnelsApi.create(d),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['tunnels'] })
      setModal({ isOpen: false })
      toast.success('Tunnel created!')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) => tunnelsApi.delete(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['tunnels'] })
      toast.success('Tunnel deleted.')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const updateMutation = useMutation({
    mutationFn: ({ id, data }: { id: string; data: Partial<Tunnel> }) => tunnelsApi.update(id, data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['tunnels'] }),
    onError: (e: Error) => toast.error(e.message),
  })

  const togglePrivate = (t: Tunnel) => {
    updateMutation.mutate({ id: t.id, data: { name: t.name, local_port: t.local_port, subdomain: t.subdomain, private: !t.private } })
  }

  // VPS config for jumpbox commands
  const { data: vpsData } = useQuery({ queryKey: ['vps'], queryFn: () => import('../api/vps').then(m => m.vpsApi.get()) })
  const vps = vpsData?.data

  const jumpboxCmd = (t: Tunnel) => {
    if (!vps) return ''
    const vpsAddr = `${vps.username}@${vps.host}`
    return `ssh -L ${t.local_port}:localhost:${t.rathole_port} ${vpsAddr} -N`
  }

  const testTunnel = async (id: string) => {
    try {
      await tunnelsApi.test(id)
      toast.success('Tunnel is reachable!')
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { error?: string } } })?.response?.data?.error
        ?? (err instanceof Error ? err.message : 'Tunnel test failed')
      toast.error(msg)
    }
  }

  const handleDelete = (id: string) => {
    if (window.confirm('Are you sure you want to delete this tunnel?')) {
      deleteMutation.mutate(id)
    }
  }

  const getMachineName = (machineId: string) => machines.find(m => m.id === machineId)?.name ?? machineId

  const copyUrl = (t: Tunnel) => {
    const url = routingEnabled && t.subdomain ? `${t.subdomain}.${domain}` : `${displayHost ?? window.location.hostname}:${t.rathole_port}`
    navigator.clipboard.writeText(url)
    toast.success('Copied!')
  }

  if (isLoading) return <div className="text-gray-400 text-center py-12">Loading...</div>

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-gray-900">Tunnels</h1>
          <p className="text-gray-500 mt-1">Expose local services through your VPS</p>
        </div>
        <button onClick={openAddModal} disabled={nextPortLoading}
          className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 text-sm font-medium disabled:opacity-50">
          {nextPortLoading ? 'Loading...' : '+ Add Tunnel'}
        </button>
      </div>

      {tunnels.length === 0 ? (
        <div className="bg-white rounded-xl shadow-sm border p-12 text-center">
          <Network className="w-12 h-12 text-gray-300 mx-auto mb-4" />
          <h2 className="text-lg font-semibold text-gray-700 mb-2">No tunnels configured yet</h2>
          <p className="text-gray-400 text-sm mb-6 max-w-sm mx-auto">Tunnels route traffic from your VPS to services running on your machines</p>
          <button onClick={openAddModal} disabled={nextPortLoading}
            className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 text-sm font-medium disabled:opacity-50">
            {nextPortLoading ? 'Loading...' : 'Add Your First Tunnel'}
          </button>
        </div>
      ) : (
        <div className="bg-white rounded-xl shadow-sm border overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-gray-50 border-b">
              <tr>
                {['Name', 'Machine', 'Routing', 'Status', 'Actions'].map(h => (
                  <th key={h} className="text-left px-4 py-3 text-xs font-semibold text-gray-500 uppercase tracking-wide">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y">
              {tunnels.map(t => (
                <React.Fragment key={t.id}>
                  {(() => {
                    const isProtectedTunnel = Boolean(t.managed || t.kind === 'machine-ssh' || t.local_port === 22)
                    const isPrivate = Boolean(t.private)
                    return (
                  <tr className="hover:bg-gray-50">
                    <td className="px-4 py-3 font-medium text-gray-900">{t.name}</td>
                    <td className="px-4 py-3 text-gray-600">{getMachineName(t.machine_id)}</td>
                    <td className="px-4 py-3">
                      <div className="flex items-center gap-2 font-mono text-xs text-gray-700">
                        <div className="flex flex-col gap-0.5">
                          {t.subdomain && domain && (
                            <a href={`https://${t.subdomain}.${domain}`} target="_blank" rel="noopener noreferrer"
                              className="text-blue-600 hover:underline">{t.subdomain}.{domain}</a>
                          )}
                          {isPrivate ? (
                            <span className="text-gray-400">VPS-local <span className="text-gray-500">:{t.rathole_port}</span></span>
                          ) : (
                            <span className="text-gray-500">
                              {t.transport === 'udp' && <span className="text-purple-600 font-semibold mr-0.5">UDP</span>}
                              {displayHost ?? 'server'}:{t.rathole_port}
                            </span>
                          )}
                        </div>
                        <ArrowRight size={12} className="text-gray-400 shrink-0" />
                        <span>localhost:{t.local_port}</span>
                        {(!isPrivate || (t.subdomain && domain)) && (
                          <button onClick={() => copyUrl(t)} className="text-gray-300 hover:text-gray-600 ml-1">
                            <ClipboardCopy size={12} />
                          </button>
                        )}
                      </div>
                    </td>
                    <td className="px-4 py-3">
                      <div className="flex items-center gap-1.5">
                        <StatusBadge status={t.status} />
                        {isPrivate && (
                          <span className="text-xs font-semibold px-1.5 py-0.5 rounded bg-slate-100 text-slate-600 border border-slate-200 flex items-center gap-0.5">
                            <Lock size={10} /> Private
                          </span>
                        )}
                        {t.transport === 'udp' && (
                          <span className="text-xs font-semibold px-1.5 py-0.5 rounded bg-purple-50 text-purple-700 border border-purple-200">UDP</span>
                        )}
                        {t.no_tls && t.subdomain && (
                          <span className="text-xs px-1.5 py-0.5 rounded bg-amber-50 text-amber-700 border border-amber-200">HTTP</span>
                        )}
                        {t.bot_protection_enabled && (
                          <span className="text-xs font-semibold px-1.5 py-0.5 rounded bg-orange-50 text-orange-700 border border-orange-200">Bot Shield</span>
                        )}
                      </div>
                    </td>
                    <td className="px-4 py-3">
                      <div className="flex gap-2">
                        {/* Privacy toggle — available on all tunnels including managed machine-ssh */}
                        <button
                          onClick={() => togglePrivate(t)}
                          disabled={updateMutation.isPending}
                          title={isPrivate ? 'Make public' : 'Make private'}
                          className={`px-2 py-1 text-xs border rounded flex items-center gap-1 ${isPrivate
                            ? 'bg-slate-50 text-slate-600 border-slate-200 hover:bg-slate-100'
                            : 'bg-white text-gray-600 border-gray-200 hover:bg-gray-50'}`}
                        >
                          {isPrivate ? <Globe size={11} /> : <Lock size={11} />}
                          {isPrivate ? 'Public' : 'Private'}
                        </button>
                        {!isProtectedTunnel && (
                          <>
                            <button onClick={() => testTunnel(t.id)} className="px-2 py-1 text-xs border border-gray-200 rounded hover:bg-gray-50">Test</button>
                            <button onClick={() => handleDelete(t.id)} className="px-2 py-1 text-xs bg-red-50 text-red-700 border border-red-200 rounded hover:bg-red-100">Delete</button>
                          </>
                        )}
                      </div>
                    </td>
                  </tr>
                    )
                  })()}
                  {t.private && jumpboxCmd(t) && (
                    <tr className="bg-slate-50 border-t-0">
                      <td colSpan={5} className="px-4 pb-2 pt-0">
                        <div className="flex items-center gap-2 text-xs text-slate-600">
                          <Terminal size={11} className="shrink-0" />
                          <span className="font-medium">Jumpbox:</span>
                          <code className="font-mono text-slate-700 bg-slate-100 px-2 py-0.5 rounded select-all">{jumpboxCmd(t)}</code>
                          <button onClick={() => { navigator.clipboard.writeText(jumpboxCmd(t)); toast.success('Copied!') }} className="text-slate-400 hover:text-slate-600">
                            <ClipboardCopy size={11} />
                          </button>
                        </div>
                      </td>
                    </tr>
                  )}
                </React.Fragment>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {modal.isOpen && (
        <div className="fixed inset-0 bg-black/60 z-50 overflow-y-auto"><div className="flex min-h-full items-center justify-center p-4">
          <div className="bg-white rounded-xl shadow-2xl w-full max-w-lg">
            <div className="flex items-center justify-between p-4 border-b">
              <h2 className="text-lg font-semibold">Add Tunnel</h2>
              <button onClick={() => setModal({ isOpen: false })} className="text-gray-400 hover:text-gray-600 text-xl">×</button>
            </div>
            {(() => {
              const serverPortConflict = form.rathole_port > 0 && (
                tunnels.some(t => t.rathole_port === form.rathole_port) ||
                machines.some(m => m.tunnel_port === form.rathole_port)
              )
              const localPortConflict = form.local_port > 0 && form.machine_id !== '' &&
                tunnels.some(t => t.machine_id === form.machine_id && t.local_port === form.local_port)
              const canCreate = form.machine_id !== '' && form.name.trim() !== '' &&
                form.rathole_port > 0 && !serverPortConflict && !localPortConflict && !createMutation.isPending
              return (
            <>
            <div className="p-4 space-y-4">
              {!routingEnabled && (
                <div className="bg-blue-50 border border-blue-200 rounded-lg px-3 py-2 text-xs text-blue-800">
                  <strong>URL routing is disabled.</strong> Caddy/reverse-proxy setup was skipped, so tunnels are exposed
                  by server port only.
                </div>
              )}

              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Machine</label>
                <select
                  value={form.machine_id}
                  onChange={e => setForm(f => ({ ...f, machine_id: e.target.value }))}
                  className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:ring-2 focus:ring-blue-500 focus:outline-none"
                >
                  <option value="">Select a machine...</option>
                  {machines.map(m => <option key={m.id} value={m.id}>{m.name}</option>)}
                </select>
              </div>

              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Name</label>
                <input type="text" value={form.name} onChange={e => setForm(f => ({ ...f, name: e.target.value }))} placeholder="My Web App"
                  className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:ring-2 focus:ring-blue-500 focus:outline-none" />
              </div>

              <div className="flex gap-4">
                <div className="flex-1">
                  <label className="block text-sm font-medium text-gray-700 mb-2">Transport</label>
                  <div className="flex gap-2">
                    {(['tcp', 'udp'] as const).map(t => (
                      <button key={t} type="button"
                        onClick={() => setForm(f => ({ ...f, transport: t, ...(t === 'udp' ? { subdomain: '', no_tls: false } : {}) }))}
                        className={`px-4 py-1.5 rounded-lg text-sm font-semibold border transition-colors ${
                          form.transport === t
                            ? t === 'udp' ? 'bg-purple-600 text-white border-purple-600' : 'bg-blue-600 text-white border-blue-600'
                            : 'bg-white text-gray-600 border-gray-300 hover:bg-gray-50'
                        }`}>
                        {t.toUpperCase()}
                      </button>
                    ))}
                  </div>
                  {form.transport === 'udp' && (
                    <p className="text-xs text-purple-600 mt-1">UDP tunnels don't support HTTP subdomain routing.</p>
                  )}
                </div>
                <div>
                  <div className="flex items-center gap-1 mb-2">
                    <label className="block text-sm font-medium text-gray-700">Visibility</label>
                    <span className="relative group">
                      <Info size={13} className="text-gray-400 cursor-help" />
                      <div className="absolute left-1/2 -translate-x-1/2 bottom-full mb-1.5 w-64 bg-gray-900 text-white text-xs rounded-lg px-3 py-2 hidden group-hover:block z-50 shadow-lg pointer-events-none">
                        <p><strong>Private</strong> binds 127.0.0.1 — the port is only reachable from the VPS itself, not the public internet.</p>
                        <p className="mt-1"><strong>Public</strong> binds 0.0.0.0 — the port is open on all interfaces.</p>
                        {routingEnabled && form.subdomain.trim() !== '' && (
                          <p className="mt-1 text-blue-300">Since you have a subdomain configured, traffic will be routed through Caddy — keeping the port private is recommended.</p>
                        )}
                        {routingEnabled && form.subdomain.trim() === '' && (
                          <p className="mt-1 text-gray-400">If you add a subdomain, Caddy will handle routing and you can safely keep the port private.</p>
                        )}
                      </div>
                    </span>
                  </div>
                  <div className="flex gap-2">
                    {([false, true] as const).map(priv => (
                      <button key={String(priv)} type="button"
                        onClick={() => setForm(f => ({ ...f, private: priv }))}
                        className={`px-3 py-1.5 rounded-lg text-sm font-semibold border transition-colors flex items-center gap-1 ${
                          form.private === priv
                            ? priv ? 'bg-slate-700 text-white border-slate-700' : 'bg-green-600 text-white border-green-600'
                            : 'bg-white text-gray-600 border-gray-300 hover:bg-gray-50'
                        }`}>
                        {priv ? <><Lock size={12} /> Private</> : <><Globe size={12} /> Public</>}
                      </button>
                    ))}
                  </div>
                </div>
              </div>

              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">
                  Local Port
                  <span className="ml-1 font-normal text-gray-400 text-xs">(port your service listens on)</span>
                </label>
                <div className="flex items-center gap-2">
                  <span className="text-sm text-gray-500 font-mono shrink-0">localhost:</span>
                  <input type="number" value={form.local_port} onChange={e => setForm(f => ({ ...f, local_port: Number(e.target.value) }))}
                    className={`flex-1 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 border ${
                      localPortConflict
                        ? 'border-amber-400 focus:ring-amber-400 bg-amber-50'
                        : 'border-gray-300 focus:ring-blue-500'
                    }`} />
                </div>
                {localPortConflict && (
                  <p className="text-xs text-amber-600 mt-1 flex items-center gap-1">
                    ⚠ Port {form.local_port} is already used by another tunnel on this machine.
                  </p>
                )}
              </div>

              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">
                  Server Port
                  <span className="ml-1 font-normal text-gray-400 text-xs">(port on your VPS — 1024–65535)</span>
                </label>
                <div className="flex items-center gap-2">
                  <span className="text-sm text-gray-500 font-mono shrink-0 truncate max-w-[160px]" title={displayHost ?? 'server'}>{displayHost ?? 'server'}:</span>
                  <input
                    type="number"
                    min={1024}
                    max={65535}
                    value={form.rathole_port || ''}
                    onChange={e => setForm(f => ({ ...f, rathole_port: Number(e.target.value) }))}
                    className={`flex-1 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 border ${
                      serverPortConflict
                        ? 'border-red-400 focus:ring-red-400 bg-red-50'
                        : 'border-gray-300 focus:ring-blue-500'
                    }`}
                  />
                </div>
                {serverPortConflict && (
                  <p className="text-xs text-red-600 mt-1 flex items-center gap-1">
                    ⚠ Port {form.rathole_port} is already in use.
                  </p>
                )}
              </div>

              {routingEnabled && form.transport !== 'udp' ? (
                <div>
                  <label className="block text-sm font-medium text-gray-700 mb-1">
                    Subdomain
                    <span className="ml-1 font-normal text-gray-400 text-xs">(optional — exposes service via HTTPS subdomain)</span>
                  </label>
                  <input type="text" value={form.subdomain} onChange={e => setForm(f => ({ ...f, subdomain: e.target.value }))} placeholder="photos"
                    className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:ring-2 focus:ring-blue-500 focus:outline-none" />
                  {form.subdomain ? (
                    <div className="mt-2 space-y-2">
                      {domain && (
                        <div className="text-xs px-2 py-1 rounded font-mono bg-blue-50 text-blue-700">
                          {form.no_tls ? 'http' : 'https'}://{form.subdomain}.{domain} → localhost:{form.local_port}
                        </div>
                      )}
                      {/* Contextual hint — SSH port escalates to a warning */}
                      {form.local_port === 22 ? (
                        <p className="flex items-start gap-1.5 text-xs text-amber-700">
                          <AlertTriangle size={13} className="mt-0.5 shrink-0" />
                          Port 22 is SSH — subdomain routing only works for HTTP/HTTPS. SSH connections use the server port directly, not a subdomain.
                        </p>
                      ) : (
                        <p className="flex items-start gap-1.5 text-xs text-gray-400">
                          <Info size={13} className="mt-0.5 shrink-0" />
                          Subdomain routing is HTTP/HTTPS only. For SSH or databases, connect via the server port directly — no subdomain needed.
                        </p>
                      )}
                      {domain && (
                        <div className="border border-gray-200 rounded-lg p-3">
                          <label className="flex items-center gap-2 cursor-pointer select-none">
                            <input type="checkbox" checked={!form.no_tls}
                              onChange={e => setForm(f => ({ ...f, no_tls: !e.target.checked }))}
                              className="rounded" />
                            <span className="text-sm font-medium text-gray-700">HTTPS</span>
                            <span className="text-xs text-gray-400 font-normal">auto-provision TLS certificate via Caddy</span>
                          </label>
                        </div>
                      )}
                    </div>
                  ) : (
                    <p className="text-xs text-gray-400 mt-1">Leave blank to expose by port only.</p>
                  )}
                </div>
              ) : null}

              {/* Bot Protection — requires subdomain (Host-header routing) */}
              {routingEnabled && form.transport !== 'udp' && form.subdomain.trim() !== '' && (
                <div className="border border-gray-200 rounded-lg p-3 space-y-3">
                  <label className="flex items-center gap-2 cursor-pointer select-none">
                    <input
                      type="checkbox"
                      checked={form.bot_protection_enabled}
                      onChange={e => setForm(f => ({ ...f, bot_protection_enabled: e.target.checked }))}
                      className="rounded"
                    />
                    <span className="text-sm font-medium text-gray-700">Bot Protection <span className="bg-orange-100 text-orange-700 text-xs font-semibold px-1.5 py-0.5 rounded">Alpha</span></span>
                    <span className="text-xs text-gray-400 font-normal">JS proof-of-work challenge for browsers</span>
                  </label>
                  {form.bot_protection_enabled && (
                    <div className="space-y-3 pl-5">
                      <div>
                        <label className="block text-xs font-medium text-gray-600 mb-1">
                          Session TTL (hours) <span className="text-gray-400 font-normal">— 0 = 24 h default</span>
                        </label>
                        <input
                          type="number"
                          min={0}
                          value={form.bot_protection_ttl === 0 ? '' : Math.round(form.bot_protection_ttl / 3600)}
                          onChange={e => setForm(f => ({
                            ...f,
                            bot_protection_ttl: e.target.value === '' ? 0 : Number(e.target.value) * 3600,
                          }))}
                          placeholder="24"
                          className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:ring-2 focus:ring-blue-500 focus:outline-none"
                        />
                      </div>
                      <div>
                        <label className="block text-xs font-medium text-gray-600 mb-1">
                          IP Allowlist <span className="text-gray-400 font-normal">— one CIDR or IP per line, bypasses challenge</span>
                        </label>
                        <textarea
                          rows={3}
                          value={allowIPDisplay(form.bot_protection_allow_ip)}
                          onChange={e => setForm(f => ({ ...f, bot_protection_allow_ip: cidrToJSON(e.target.value) }))}
                          placeholder={"192.168.1.0/24\n10.0.0.1"}
                          className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm font-mono focus:ring-2 focus:ring-blue-500 focus:outline-none resize-none"
                        />
                      </div>
                      <p className="text-xs text-orange-700 bg-orange-50 border border-orange-100 rounded px-2 py-1.5">
                        API clients (Accept: application/json) receive 403. WebSocket connections are allowed after the cookie is set. Does not protect against L3/L4 attacks.
                      </p>
                    </div>
                  )}
                </div>
              )}
            </div>
            <div className="flex justify-end gap-2 p-4 border-t">
              <button onClick={() => setModal({ isOpen: false })} className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg hover:bg-gray-50 text-sm">Cancel</button>
              <button
                onClick={() => createMutation.mutate({ ...form, subdomain: routingEnabled && form.transport !== 'udp' ? form.subdomain : '' })}
                disabled={!canCreate}
                className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 text-sm disabled:opacity-50 disabled:cursor-not-allowed">
                {createMutation.isPending ? 'Creating...' : 'Create Tunnel'}
              </button>
            </div>
            </>
          )
        })()}
          </div>
        </div></div>
      )}
    </div>
  )
}
