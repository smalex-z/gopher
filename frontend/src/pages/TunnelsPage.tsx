import React, { useState, useEffect } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { Network, ClipboardCopy, ArrowRight, Globe, Lock, Terminal } from 'lucide-react'
import { tunnelsApi } from '../api/tunnels'
import { machinesApi } from '../api/machines'
import { localApi } from '../api/local'
import StatusBadge from '../components/StatusBadge'
import { toast } from '../lib/toast'
import type { Tunnel } from '../types'

interface ModalState { isOpen: boolean }
interface TestResult { ok: boolean; message: string }
interface FormState {
  machine_id: string
  name: string
  subdomain: string
  local_port: number
  rathole_port: number
  transport: string
  no_tls: boolean
  private: boolean
}

const defaultForm: FormState = { machine_id: '', name: '', subdomain: '', local_port: 3000, rathole_port: 0, transport: 'tcp', no_tls: false, private: false }

export default function TunnelsPage() {
  const qc = useQueryClient()
  const [searchParams] = useSearchParams()
  const [modal, setModal] = useState<ModalState>({ isOpen: false })
  const [testResults, setTestResults] = useState<Record<string, TestResult>>({})
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
      setTestResults(r => ({ ...r, [id]: { ok: true, message: 'Tunnel is reachable!' } }))
    } catch (err) {
      setTestResults(r => ({ ...r, [id]: { ok: false, message: err instanceof Error ? err.message : 'Test failed' } }))
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
                          {isPrivate ? (
                            <span className="text-gray-400">VPS-local <span className="text-gray-500">:{t.rathole_port}</span></span>
                          ) : (
                            <>
                              {t.subdomain && domain && (
                                <a href={`https://${t.subdomain}.${domain}`} target="_blank" rel="noopener noreferrer"
                                  className="text-blue-600 hover:underline">{t.subdomain}.{domain}</a>
                              )}
                              <span className="text-gray-500">
                                {t.transport === 'udp' && <span className="text-purple-600 font-semibold mr-0.5">UDP</span>}
                                {displayHost ?? 'server'}:{t.rathole_port}
                              </span>
                            </>
                          )}
                        </div>
                        <ArrowRight size={12} className="text-gray-400 shrink-0" />
                        <span>localhost:{t.local_port}</span>
                        {!isPrivate && (
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
                      </div>
                    </td>
                    <td className="px-4 py-3">
                      <div className="flex gap-2">
                        {isProtectedTunnel ? (
                          <span className="px-2 py-1 text-xs bg-gray-50 text-gray-500 border border-gray-200 rounded">Managed</span>
                        ) : (
                          <>
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
                            <button onClick={() => testTunnel(t.id)} className="px-2 py-1 text-xs border border-gray-200 rounded hover:bg-gray-50">Test</button>
                            <button onClick={() => handleDelete(t.id)} className="px-2 py-1 text-xs bg-red-50 text-red-700 border border-red-200 rounded hover:bg-red-100">Delete</button>
                          </>
                        )}
                      </div>
                    </td>
                  </tr>
                    )
                  })()}
                  {testResults[t.id] && (
                    <tr>
                      <td colSpan={5} className="px-4 py-2">
                        <span className={`text-xs px-2 py-1 rounded ${testResults[t.id].ok ? 'bg-green-50 text-green-700' : 'bg-red-50 text-red-700'}`}>
                          {testResults[t.id].ok ? '✅' : '❌'} {testResults[t.id].message}
                        </span>
                      </td>
                    </tr>
                  )}
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
            <div className="p-4 space-y-4">
              {routingEnabled ? (
                <div className="bg-amber-50 border border-amber-200 rounded-lg px-3 py-2 text-xs text-amber-800">
                  <strong>HTTP traffic only via subdomain.</strong> Caddy proxies subdomain → local port over plain HTTP.
                  For raw TCP (SSH, databases), use the server port directly — no subdomain needed.
                </div>
              ) : (
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
                  <label className="block text-sm font-medium text-gray-700 mb-2">Visibility</label>
                  <div className="flex gap-2">
                    {([false, true] as const).map(priv => (
                      <button key={String(priv)} type="button"
                        onClick={() => setForm(f => ({ ...f, private: priv, ...(priv ? { subdomain: '', no_tls: false } : {}) }))}
                        className={`px-3 py-1.5 rounded-lg text-sm font-semibold border transition-colors flex items-center gap-1 ${
                          form.private === priv
                            ? priv ? 'bg-slate-700 text-white border-slate-700' : 'bg-green-600 text-white border-green-600'
                            : 'bg-white text-gray-600 border-gray-300 hover:bg-gray-50'
                        }`}>
                        {priv ? <><Lock size={12} /> Private</> : <><Globe size={12} /> Public</>}
                      </button>
                    ))}
                  </div>
                  {form.private && (
                    <p className="text-xs text-slate-600 mt-1">Binds 127.0.0.1 — VPS-local only.</p>
                  )}
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
                    className="flex-1 border border-gray-300 rounded-lg px-3 py-2 text-sm focus:ring-2 focus:ring-blue-500 focus:outline-none" />
                </div>
              </div>

              {routingEnabled && form.transport !== 'udp' && !form.private ? (
                <div>
                  <label className="block text-sm font-medium text-gray-700 mb-1">
                    Subdomain
                    <span className="ml-1 font-normal text-gray-400 text-xs">(optional — HTTP web apps only)</span>
                  </label>
                  <input type="text" value={form.subdomain} onChange={e => setForm(f => ({ ...f, subdomain: e.target.value }))} placeholder="photos"
                    className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:ring-2 focus:ring-blue-500 focus:outline-none" />
                  {domain && form.subdomain ? (
                    <>
                      <div className="mt-1 text-xs bg-blue-50 text-blue-700 px-2 py-1 rounded font-mono">
                        {form.no_tls ? 'http' : 'https'}://{form.subdomain}.{domain} → localhost:{form.local_port}
                      </div>
                      <label className="flex items-center gap-2 mt-2 text-sm text-gray-700 cursor-pointer select-none">
                        <input type="checkbox" checked={form.no_tls}
                          onChange={e => setForm(f => ({ ...f, no_tls: e.target.checked }))}
                          className="rounded" />
                        <span>HTTP only <span className="text-gray-400 font-normal text-xs">(skip TLS certificate — useful for internal services)</span></span>
                      </label>
                    </>
                  ) : (
                    <p className="text-xs text-gray-400 mt-1">
                      Leave blank to use raw TCP port only (e.g. {displayHost ?? 'server'}:20000).
                    </p>
                  )}
                </div>
              ) : null}

              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">
                  Server Port
                  <span className="ml-1 font-normal text-gray-400 text-xs">(port on your VPS — must be 20000–65535)</span>
                </label>
                <div className="flex items-center gap-2">
                  <span className="text-sm text-gray-500 font-mono shrink-0 truncate max-w-[160px]" title={displayHost ?? 'server'}>{displayHost ?? 'server'}:</span>
                  <input
                    type="number"
                    min={20000}
                    max={65535}
                    value={form.rathole_port || ''}
                    onChange={e => setForm(f => ({ ...f, rathole_port: Number(e.target.value) }))}
                    placeholder="80"
                    className="flex-1 border border-gray-300 rounded-lg px-3 py-2 text-sm focus:ring-2 focus:ring-blue-500 focus:outline-none"
                  />
                </div>
                <p className="text-xs text-gray-400 mt-1">Leave at 0 to auto-assign the next available port.</p>
              </div>
            </div>
            <div className="flex justify-end gap-2 p-4 border-t">
              <button onClick={() => setModal({ isOpen: false })} className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg hover:bg-gray-50 text-sm">Cancel</button>
              <button
                onClick={() => createMutation.mutate({ ...form, subdomain: routingEnabled && form.transport !== 'udp' ? form.subdomain : '' })}
                disabled={createMutation.isPending}
                className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 text-sm disabled:opacity-50">
                {createMutation.isPending ? 'Creating...' : 'Create Tunnel'}
              </button>
            </div>
          </div>
        </div></div>
      )}
    </div>
  )
}
