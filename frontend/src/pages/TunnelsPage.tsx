import React, { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Network, ClipboardCopy } from 'lucide-react'
import { tunnelsApi } from '../api/tunnels'
import { machinesApi } from '../api/machines'
import { vpsApi } from '../api/vps'
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
  protocol: string
}

const defaultForm: FormState = { machine_id: '', name: '', subdomain: '', local_port: 3000, protocol: 'http' }

export default function TunnelsPage() {
  const qc = useQueryClient()
  const [modal, setModal] = useState<ModalState>({ isOpen: false })
  const [testResults, setTestResults] = useState<Record<string, TestResult>>({})
  const [form, setForm] = useState<FormState>(defaultForm)

  const { data: tunnelsData, isLoading } = useQuery({ queryKey: ['tunnels'], queryFn: () => tunnelsApi.list() })
  const { data: machinesData } = useQuery({ queryKey: ['machines'], queryFn: () => machinesApi.list() })
  const { data: vpsData } = useQuery({ queryKey: ['vps'], queryFn: () => vpsApi.get(), retry: false })

  const tunnels: Tunnel[] = tunnelsData?.data ?? []
  const machines = machinesData?.data ?? []
  const vps = vpsData?.data

  const createMutation = useMutation({
    mutationFn: (d: Partial<Tunnel>) => tunnelsApi.create(d),
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

  const copyUrl = (subdomain: string) => {
    if (vps?.domain) {
      navigator.clipboard.writeText(`${subdomain}.${vps.domain}`)
      toast.success('URL copied!')
    }
  }

  if (isLoading) return <div className="text-gray-400 text-center py-12">Loading...</div>

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-gray-900">Tunnels</h1>
          <p className="text-gray-500 mt-1">Expose local services through your VPS</p>
        </div>
        <button onClick={() => { setForm(defaultForm); setModal({ isOpen: true }) }}
          className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 text-sm font-medium">
          + Add Tunnel
        </button>
      </div>

      {tunnels.length === 0 ? (
        <div className="bg-white rounded-xl shadow-sm border p-12 text-center">
          <Network className="w-12 h-12 text-gray-300 mx-auto mb-4" />
          <h2 className="text-lg font-semibold text-gray-700 mb-2">No tunnels configured yet</h2>
          <p className="text-gray-400 text-sm mb-6 max-w-sm mx-auto">Tunnels route traffic from your VPS domain to services running on your local machines</p>
          <button onClick={() => { setForm(defaultForm); setModal({ isOpen: true }) }}
            className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 text-sm font-medium">
            Add Your First Tunnel
          </button>
        </div>
      ) : (
        <div className="bg-white rounded-xl shadow-sm border overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-gray-50 border-b">
              <tr>
                {['Name', 'Machine', 'URL', 'Local Port', 'Protocol', 'Status', 'Actions'].map(h => (
                  <th key={h} className="text-left px-4 py-3 text-xs font-semibold text-gray-500 uppercase tracking-wide">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y">
              {tunnels.map(t => (
                <React.Fragment key={t.id}>
                  <tr className="hover:bg-gray-50">
                    <td className="px-4 py-3 font-medium text-gray-900">{t.name}</td>
                    <td className="px-4 py-3 text-gray-600">{getMachineName(t.machine_id)}</td>
                    <td className="px-4 py-3">
                      {vps?.domain ? (
                        <div className="flex items-center gap-1">
                          <a href={`https://${t.subdomain}.${vps.domain}`} target="_blank" rel="noopener noreferrer"
                            className="text-blue-600 hover:underline text-xs">{t.subdomain}.{vps.domain}</a>
                          <button onClick={() => copyUrl(t.subdomain)} className="text-gray-300 hover:text-gray-600 ml-1">
                            <ClipboardCopy size={12} />
                          </button>
                        </div>
                      ) : (
                        <span className="text-xs text-gray-400">{t.subdomain} <span className="text-gray-300">(configure VPS domain)</span></span>
                      )}
                    </td>
                    <td className="px-4 py-3 text-gray-600">{t.local_port}</td>
                    <td className="px-4 py-3">
                      <span className="uppercase text-xs font-mono bg-gray-100 px-2 py-0.5 rounded">{t.protocol}</span>
                    </td>
                    <td className="px-4 py-3"><StatusBadge status={t.status} /></td>
                    <td className="px-4 py-3">
                      <div className="flex gap-2">
                        <button onClick={() => testTunnel(t.id)} className="px-2 py-1 text-xs border border-gray-200 rounded hover:bg-gray-50">Test</button>
                        <button onClick={() => handleDelete(t.id)} className="px-2 py-1 text-xs bg-red-50 text-red-700 border border-red-200 rounded hover:bg-red-100">Delete</button>
                      </div>
                    </td>
                  </tr>
                  {testResults[t.id] && (
                    <tr>
                      <td colSpan={7} className="px-4 py-2">
                        <span className={`text-xs px-2 py-1 rounded ${testResults[t.id].ok ? 'bg-green-50 text-green-700' : 'bg-red-50 text-red-700'}`}>
                          {testResults[t.id].ok ? '✅' : '❌'} {testResults[t.id].message}
                        </span>
                      </td>
                    </tr>
                  )}
                </React.Fragment>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Add Tunnel Modal */}
      {modal.isOpen && (
        <div className="fixed inset-0 bg-black/60 z-50 flex items-center justify-center p-4">
          <div className="bg-white rounded-xl shadow-2xl w-full max-w-lg">
            <div className="flex items-center justify-between p-4 border-b">
              <h2 className="text-lg font-semibold">Add Tunnel</h2>
              <button onClick={() => setModal({ isOpen: false })} className="text-gray-400 hover:text-gray-600 text-xl">×</button>
            </div>
            <div className="p-4 space-y-3">
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Machine</label>
                <select
                  value={form.machine_id}
                  onChange={e => setForm(f => ({ ...f, machine_id: e.target.value }))}
                  className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:ring-2 focus:ring-blue-500 focus:outline-none"
                >
                  <option value="">Select a machine...</option>
                  {machines.map(m => <option key={m.id} value={m.id}>{m.name} ({m.host})</option>)}
                </select>
              </div>
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Name</label>
                <input type="text" value={form.name} onChange={e => setForm(f => ({ ...f, name: e.target.value }))} placeholder="My Web App"
                  className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:ring-2 focus:ring-blue-500 focus:outline-none" />
              </div>
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Subdomain</label>
                <input type="text" value={form.subdomain} onChange={e => setForm(f => ({ ...f, subdomain: e.target.value }))} placeholder="photos"
                  className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:ring-2 focus:ring-blue-500 focus:outline-none" />
                <p className="text-xs text-gray-400 mt-1">e.g. 'photos' → photos.yourdomain.com</p>
                {vps?.domain && form.subdomain && (
                  <div className="mt-1 text-xs bg-blue-50 text-blue-700 px-2 py-1 rounded">
                    Preview: <strong>{form.subdomain}.{vps.domain}</strong>
                  </div>
                )}
              </div>
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block text-sm font-medium text-gray-700 mb-1">Local Port</label>
                  <input type="number" value={form.local_port} onChange={e => setForm(f => ({ ...f, local_port: Number(e.target.value) }))}
                    className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:ring-2 focus:ring-blue-500 focus:outline-none" />
                </div>
                <div>
                  <label className="block text-sm font-medium text-gray-700 mb-1">Protocol</label>
                  <select value={form.protocol} onChange={e => setForm(f => ({ ...f, protocol: e.target.value }))}
                    className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:ring-2 focus:ring-blue-500 focus:outline-none">
                    <option value="http">HTTP</option>
                    <option value="tcp">TCP</option>
                  </select>
                  <p className="text-xs text-gray-400 mt-1">{form.protocol === 'http' ? 'HTTP: web apps' : 'TCP: SSH, databases'}</p>
                </div>
              </div>
            </div>
            <div className="flex justify-end gap-2 p-4 border-t">
              <button onClick={() => setModal({ isOpen: false })} className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg hover:bg-gray-50 text-sm">Cancel</button>
              <button onClick={() => createMutation.mutate(form)} disabled={createMutation.isPending}
                className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 text-sm disabled:opacity-50">
                {createMutation.isPending ? 'Creating...' : 'Create Tunnel'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
