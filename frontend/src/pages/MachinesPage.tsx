import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { machinesApi } from '../api/machines'
import type { Machine } from '../types'

export default function MachinesPage() {
  const qc = useQueryClient()
  const { data } = useQuery({ queryKey: ['machines'], queryFn: machinesApi.list })
  const [showForm, setShowForm] = useState(false)
  const [form, setForm] = useState({ name: '', host: '', port: 22, username: 'root', private_key: '' })

  const createMutation = useMutation({
    mutationFn: machinesApi.create,
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['machines'] }); setShowForm(false) },
  })

  const deleteMutation = useMutation({
    mutationFn: machinesApi.delete,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['machines'] }),
  })

  const deployMutation = useMutation({ mutationFn: machinesApi.deploy })

  const machines: Machine[] = data?.data ?? []

  return (
    <div>
      <div className="flex justify-between items-center mb-6">
        <h1 className="text-2xl font-bold">Machines</h1>
        <button onClick={() => setShowForm(true)} className="bg-blue-600 text-white px-4 py-2 rounded hover:bg-blue-700">Add Machine</button>
      </div>

      {showForm && (
        <div className="bg-white rounded-lg shadow p-6 mb-6">
          <h2 className="text-lg font-medium mb-4">Add Machine</h2>
          <div className="grid grid-cols-2 gap-4">
            <input placeholder="Name" value={form.name} onChange={e => setForm(f => ({...f, name: e.target.value}))} className="border rounded p-2" />
            <input placeholder="Host" value={form.host} onChange={e => setForm(f => ({...f, host: e.target.value}))} className="border rounded p-2" />
            <input type="number" placeholder="Port" value={form.port} onChange={e => setForm(f => ({...f, port: +e.target.value}))} className="border rounded p-2" />
            <input placeholder="Username" value={form.username} onChange={e => setForm(f => ({...f, username: e.target.value}))} className="border rounded p-2" />
            <textarea placeholder="Private Key (PEM)" value={form.private_key} onChange={e => setForm(f => ({...f, private_key: e.target.value}))} className="border rounded p-2 col-span-2 h-32" />
          </div>
          <div className="flex gap-4 mt-4">
            <button onClick={() => createMutation.mutate(form)} className="bg-blue-600 text-white px-4 py-2 rounded hover:bg-blue-700">Save</button>
            <button onClick={() => setShowForm(false)} className="bg-gray-200 px-4 py-2 rounded hover:bg-gray-300">Cancel</button>
          </div>
        </div>
      )}

      <div className="space-y-4">
        {machines.map(m => (
          <div key={m.id} className="bg-white rounded-lg shadow p-4 flex items-center justify-between">
            <div>
              <h3 className="font-medium">{m.name}</h3>
              <p className="text-sm text-gray-500">{m.username}@{m.host}:{m.port}</p>
              <span className={`text-xs px-2 py-0.5 rounded ${m.status === 'active' ? 'bg-green-100 text-green-700' : 'bg-gray-100 text-gray-600'}`}>{m.status}</span>
            </div>
            <div className="flex gap-2">
              <button onClick={() => deployMutation.mutate(m.id)} className="bg-green-600 text-white px-3 py-1.5 rounded text-sm hover:bg-green-700">Deploy</button>
              <button onClick={() => deleteMutation.mutate(m.id)} className="bg-red-600 text-white px-3 py-1.5 rounded text-sm hover:bg-red-700">Delete</button>
            </div>
          </div>
        ))}
        {machines.length === 0 && <p className="text-gray-500">No machines configured yet.</p>}
      </div>
    </div>
  )
}
