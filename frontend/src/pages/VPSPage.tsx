import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { vpsApi } from '../api/vps'

export default function VPSPage() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['vps'],
    queryFn: vpsApi.get,
    retry: false,
  })

  const [form, setForm] = useState({ host: '', port: 22, username: 'root', private_key: '', domain: '' })

  const createMutation = useMutation({
    mutationFn: vpsApi.create,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['vps'] }),
  })

  const deleteMutation = useMutation({
    mutationFn: vpsApi.delete,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['vps'] }),
  })

  const bootstrapMutation = useMutation({ mutationFn: vpsApi.bootstrap })
  const deployMutation = useMutation({ mutationFn: vpsApi.deploy })

  if (isLoading) return <div>Loading...</div>

  const vps = data?.data

  return (
    <div>
      <h1 className="text-2xl font-bold mb-6">VPS Configuration</h1>
      {vps ? (
        <div className="bg-white rounded-lg shadow p-6">
          <div className="grid grid-cols-2 gap-4 mb-6">
            <div><span className="text-gray-500">Host:</span> <span className="font-medium">{vps.host}</span></div>
            <div><span className="text-gray-500">Port:</span> <span className="font-medium">{vps.port}</span></div>
            <div><span className="text-gray-500">Username:</span> <span className="font-medium">{vps.username}</span></div>
            <div><span className="text-gray-500">Domain:</span> <span className="font-medium">{vps.domain}</span></div>
          </div>
          <div className="flex gap-4">
            <button onClick={() => bootstrapMutation.mutate()} className="bg-blue-600 text-white px-4 py-2 rounded hover:bg-blue-700">Bootstrap</button>
            <button onClick={() => deployMutation.mutate()} className="bg-green-600 text-white px-4 py-2 rounded hover:bg-green-700">Deploy</button>
            <button onClick={() => deleteMutation.mutate()} className="bg-red-600 text-white px-4 py-2 rounded hover:bg-red-700">Remove</button>
          </div>
        </div>
      ) : (
        <div className="bg-white rounded-lg shadow p-6">
          <h2 className="text-lg font-medium mb-4">Configure VPS</h2>
          <div className="grid grid-cols-2 gap-4">
            <input placeholder="Host" value={form.host} onChange={e => setForm(f => ({...f, host: e.target.value}))} className="border rounded p-2" />
            <input type="number" placeholder="Port" value={form.port} onChange={e => setForm(f => ({...f, port: +e.target.value}))} className="border rounded p-2" />
            <input placeholder="Username" value={form.username} onChange={e => setForm(f => ({...f, username: e.target.value}))} className="border rounded p-2" />
            <input placeholder="Domain" value={form.domain} onChange={e => setForm(f => ({...f, domain: e.target.value}))} className="border rounded p-2" />
            <textarea placeholder="Private Key (PEM)" value={form.private_key} onChange={e => setForm(f => ({...f, private_key: e.target.value}))} className="border rounded p-2 col-span-2 h-32" />
          </div>
          <button onClick={() => createMutation.mutate(form)} className="mt-4 bg-blue-600 text-white px-4 py-2 rounded hover:bg-blue-700">Save & Connect</button>
        </div>
      )}
    </div>
  )
}
