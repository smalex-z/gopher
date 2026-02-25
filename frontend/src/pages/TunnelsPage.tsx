import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { tunnelsApi } from '../api/tunnels'
import { machinesApi } from '../api/machines'
import type { Tunnel } from '../types'

export default function TunnelsPage() {
  const qc = useQueryClient()
  const { data } = useQuery({ queryKey: ['tunnels'], queryFn: tunnelsApi.list })
  const { data: machinesData } = useQuery({ queryKey: ['machines'], queryFn: machinesApi.list })
  const [showForm, setShowForm] = useState(false)
  const [form, setForm] = useState({ machine_id: '', name: '', subdomain: '', local_port: 3000, protocol: 'http' })

  const createMutation = useMutation({
    mutationFn: tunnelsApi.create,
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['tunnels'] }); setShowForm(false) },
  })

  const deleteMutation = useMutation({
    mutationFn: tunnelsApi.delete,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['tunnels'] }),
  })

  const tunnels: Tunnel[] = data?.data ?? []
  const machines = machinesData?.data ?? []

  return (
    <div>
      <div className="flex justify-between items-center mb-6">
        <h1 className="text-2xl font-bold">Tunnels</h1>
        <button onClick={() => setShowForm(true)} className="bg-blue-600 text-white px-4 py-2 rounded hover:bg-blue-700">Add Tunnel</button>
      </div>

      {showForm && (
        <div className="bg-white rounded-lg shadow p-6 mb-6">
          <h2 className="text-lg font-medium mb-4">Add Tunnel</h2>
          <div className="grid grid-cols-2 gap-4">
            <select value={form.machine_id} onChange={e => setForm(f => ({...f, machine_id: e.target.value}))} className="border rounded p-2">
              <option value="">Select Machine</option>
              {machines.map(m => <option key={m.id} value={m.id}>{m.name}</option>)}
            </select>
            <input placeholder="Name" value={form.name} onChange={e => setForm(f => ({...f, name: e.target.value}))} className="border rounded p-2" />
            <input placeholder="Subdomain" value={form.subdomain} onChange={e => setForm(f => ({...f, subdomain: e.target.value}))} className="border rounded p-2" />
            <input type="number" placeholder="Local Port" value={form.local_port} onChange={e => setForm(f => ({...f, local_port: +e.target.value}))} className="border rounded p-2" />
            <select value={form.protocol} onChange={e => setForm(f => ({...f, protocol: e.target.value}))} className="border rounded p-2">
              <option value="http">HTTP</option>
              <option value="tcp">TCP</option>
            </select>
          </div>
          <div className="flex gap-4 mt-4">
            <button onClick={() => createMutation.mutate(form)} className="bg-blue-600 text-white px-4 py-2 rounded hover:bg-blue-700">Save</button>
            <button onClick={() => setShowForm(false)} className="bg-gray-200 px-4 py-2 rounded hover:bg-gray-300">Cancel</button>
          </div>
        </div>
      )}

      <div className="space-y-4">
        {tunnels.map(t => (
          <div key={t.id} className="bg-white rounded-lg shadow p-4 flex items-center justify-between">
            <div>
              <h3 className="font-medium">{t.name}</h3>
              <p className="text-sm text-gray-500">{t.subdomain} → localhost:{t.local_port} (via :{t.rathole_port})</p>
              <span className={`text-xs px-2 py-0.5 rounded ${t.status === 'active' ? 'bg-green-100 text-green-700' : 'bg-gray-100 text-gray-600'}`}>{t.status}</span>
            </div>
            <div className="flex gap-2">
              <button onClick={() => deleteMutation.mutate(t.id)} className="bg-red-600 text-white px-3 py-1.5 rounded text-sm hover:bg-red-700">Delete</button>
            </div>
          </div>
        ))}
        {tunnels.length === 0 && <p className="text-gray-500">No tunnels configured yet.</p>}
      </div>
    </div>
  )
}
