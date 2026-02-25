import React, { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Server } from 'lucide-react'
import { machinesApi } from '../api/machines'
import StatusBadge from '../components/StatusBadge'
import DeployLogModal from '../components/DeployLogModal'
import { toast } from '../lib/toast'
import type { Machine } from '../types'

interface ModalState { isOpen: boolean; mode: 'add' | 'edit'; editId: string }
interface DeployModalState { isOpen: boolean; machineId: string; machineName: string }
interface FormState { name: string; host: string; port: number; username: string; private_key: string }

const defaultForm: FormState = { name: '', host: '', port: 22, username: 'root', private_key: '' }

export default function MachinesPage() {
  const qc = useQueryClient()
  const [modal, setModal] = useState<ModalState>({ isOpen: false, mode: 'add', editId: '' })
  const [deployModal, setDeployModal] = useState<DeployModalState>({ isOpen: false, machineId: '', machineName: '' })
  const [testResults, setTestResults] = useState<Record<string, { ok: boolean; message: string }>>({})
  const [form, setForm] = useState<FormState>(defaultForm)

  const { data, isLoading } = useQuery({ queryKey: ['machines'], queryFn: () => machinesApi.list() })
  const machines: Machine[] = data?.data ?? []

  const createMutation = useMutation({
    mutationFn: (d: Partial<Machine>) => machinesApi.create(d),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['machines'] })
      setModal(m => ({ ...m, isOpen: false }))
      toast.success('Machine added!')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) => machinesApi.delete(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['machines'] })
      toast.success('Machine deleted.')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const testMachine = async (id: string) => {
    try {
      const result = await machinesApi.status(id)
      setTestResults(r => ({ ...r, [id]: { ok: true, message: result?.message ?? 'Reachable' } }))
    } catch (err) {
      setTestResults(r => ({ ...r, [id]: { ok: false, message: err instanceof Error ? err.message : 'Failed' } }))
    }
  }

  const openDeployModal = (id: string, name: string) => {
    setDeployModal({ isOpen: true, machineId: id, machineName: name })
  }

  const handleDelete = (id: string) => {
    if (window.confirm('Are you sure you want to delete this machine?')) {
      deleteMutation.mutate(id)
    }
  }

  const handleSave = () => {
    createMutation.mutate(form)
  }

  if (isLoading) return <div className="text-gray-400 text-center py-12">Loading...</div>

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-gray-900">Machines</h1>
          <p className="text-gray-500 mt-1">Linux servers running the rathole tunnel client</p>
        </div>
        <button onClick={() => { setForm(defaultForm); setModal({ isOpen: true, mode: 'add', editId: '' }) }}
          className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 text-sm font-medium">
          + Add Machine
        </button>
      </div>

      {machines.length === 0 ? (
        <div className="bg-white rounded-xl shadow-sm border p-12 text-center">
          <Server className="w-12 h-12 text-gray-300 mx-auto mb-4" />
          <h2 className="text-lg font-semibold text-gray-700 mb-2">No machines added yet</h2>
          <p className="text-gray-400 text-sm mb-6 max-w-sm mx-auto">Machines are Linux servers on your local network that run the rathole tunnel client</p>
          <button onClick={() => { setForm(defaultForm); setModal({ isOpen: true, mode: 'add', editId: '' }) }}
            className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 text-sm font-medium">
            Add Your First Machine
          </button>
        </div>
      ) : (
        <div className="bg-white rounded-xl shadow-sm border overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-gray-50 border-b">
              <tr>
                {['Name', 'Host', 'Username', 'Status', 'Last Seen', 'Actions'].map(h => (
                  <th key={h} className="text-left px-4 py-3 text-xs font-semibold text-gray-500 uppercase tracking-wide">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y">
              {machines.map(m => (
                <React.Fragment key={m.id}>
                  <tr className="hover:bg-gray-50">
                    <td className="px-4 py-3 font-medium text-gray-900">{m.name}</td>
                    <td className="px-4 py-3 text-gray-600">{m.host}:{m.port}</td>
                    <td className="px-4 py-3 text-gray-600">{m.username}</td>
                    <td className="px-4 py-3"><StatusBadge status={m.status} /></td>
                    <td className="px-4 py-3 text-gray-500">{m.last_seen ? new Date(m.last_seen).toLocaleString() : 'Never'}</td>
                    <td className="px-4 py-3">
                      <div className="flex gap-2">
                        <button onClick={() => testMachine(m.id)} className="px-2 py-1 text-xs border border-gray-200 rounded hover:bg-gray-50">Test</button>
                        <button onClick={() => openDeployModal(m.id, m.name)} className="px-2 py-1 text-xs bg-blue-50 text-blue-700 border border-blue-200 rounded hover:bg-blue-100">Deploy Client</button>
                        <button onClick={() => handleDelete(m.id)} className="px-2 py-1 text-xs bg-red-50 text-red-700 border border-red-200 rounded hover:bg-red-100">Delete</button>
                      </div>
                    </td>
                  </tr>
                  {testResults[m.id] && (
                    <tr>
                      <td colSpan={6} className="px-4 py-2">
                        <span className={`text-xs px-2 py-1 rounded ${testResults[m.id].ok ? 'bg-green-50 text-green-700' : 'bg-red-50 text-red-700'}`}>
                          {testResults[m.id].ok ? '✅' : '❌'} {testResults[m.id].message}
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

      {/* Add/Edit Modal */}
      {modal.isOpen && (
        <div className="fixed inset-0 bg-black/60 z-50 flex items-center justify-center p-4">
          <div className="bg-white rounded-xl shadow-2xl w-full max-w-lg">
            <div className="flex items-center justify-between p-4 border-b">
              <h2 className="text-lg font-semibold">Add Machine</h2>
              <button onClick={() => setModal(m => ({ ...m, isOpen: false }))} className="text-gray-400 hover:text-gray-600 text-xl">×</button>
            </div>
            <div className="p-4 space-y-3">
              {([
                { label: 'Name', key: 'name', placeholder: 'My Home Server' },
                { label: 'Host', key: 'host', placeholder: '192.168.1.100' },
                { label: 'Port', key: 'port', type: 'number', placeholder: '22' },
                { label: 'Username', key: 'username', placeholder: 'root' },
              ] as Array<{ label: string; key: keyof FormState; type?: string; placeholder: string }>).map(({ label, key, type = 'text', placeholder }) => (
                <div key={key}>
                  <label className="block text-sm font-medium text-gray-700 mb-1">{label}</label>
                  <input
                    type={type}
                    value={String(form[key])}
                    onChange={e => setForm(f => ({ ...f, [key]: type === 'number' ? Number(e.target.value) : e.target.value }))}
                    placeholder={placeholder}
                    className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:ring-2 focus:ring-blue-500 focus:outline-none"
                  />
                </div>
              ))}
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">SSH Private Key (PEM)</label>
                <textarea
                  value={form.private_key}
                  onChange={e => setForm(f => ({ ...f, private_key: e.target.value }))}
                  rows={5}
                  placeholder="-----BEGIN RSA PRIVATE KEY-----&#10;..."
                  className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm font-mono focus:ring-2 focus:ring-blue-500 focus:outline-none"
                />
              </div>
            </div>
            <div className="flex justify-end gap-2 p-4 border-t">
              <button onClick={() => setModal(m => ({ ...m, isOpen: false }))} className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg hover:bg-gray-50 text-sm">Cancel</button>
              <button onClick={handleSave} disabled={createMutation.isPending} className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 text-sm disabled:opacity-50">
                {createMutation.isPending ? 'Saving...' : 'Save'}
              </button>
            </div>
          </div>
        </div>
      )}

      <DeployLogModal
        isOpen={deployModal.isOpen}
        onClose={() => setDeployModal(d => ({ ...d, isOpen: false }))}
        title={`Deploy Client to ${deployModal.machineName}`}
        onStart={() => machinesApi.deploy(deployModal.machineId)}
      />
    </div>
  )
}
