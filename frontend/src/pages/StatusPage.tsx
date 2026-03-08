import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { vpsApi } from '../api/vps'
import { machinesApi } from '../api/machines'
import { tunnelsApi } from '../api/tunnels'
import StatusBadge from '../components/StatusBadge'
import type { Machine, Tunnel } from '../types'

interface MachineStatus { ok: boolean; message: string }

export default function StatusPage() {
  const [vpsStatus, setVpsStatus] = useState<{ ok: boolean; message: string } | null>(null)
  const [vpsChecking, setVpsChecking] = useState(false)
  const [machineStatuses, setMachineStatuses] = useState<Record<string, MachineStatus>>({})

  const { data: vpsData, refetch: refetchVps } = useQuery({ queryKey: ['vps'], queryFn: () => vpsApi.get(), retry: false })
  const { data: machinesData, refetch: refetchMachines } = useQuery({ queryKey: ['machines'], queryFn: () => machinesApi.list(), refetchInterval: 30000 })
  const { data: tunnelsData, refetch: refetchTunnels } = useQuery({ queryKey: ['tunnels'], queryFn: () => tunnelsApi.list(), refetchInterval: 30000 })

  const vps = vpsData?.data
  const machines: Machine[] = machinesData?.data ?? []
  const tunnels: Tunnel[] = tunnelsData?.data ?? []

  const checkVpsStatus = async () => {
    setVpsChecking(true)
    setVpsStatus(null)
    try {
      await vpsApi.status()
      setVpsStatus({ ok: true, message: 'VPS is reachable' })
    } catch (err) {
      setVpsStatus({ ok: false, message: err instanceof Error ? err.message : 'Unreachable' })
    } finally {
      setVpsChecking(false)
    }
  }

  const checkMachineStatus = async (id: string) => {
    try {
      const r = await machinesApi.status(id)
      setMachineStatuses(s => ({ ...s, [id]: { ok: true, message: r?.message ?? 'Reachable' } }))
    } catch (err) {
      setMachineStatuses(s => ({ ...s, [id]: { ok: false, message: err instanceof Error ? err.message : 'Failed' } }))
    }
  }

  const refreshAll = () => {
    refetchVps()
    refetchMachines()
    refetchTunnels()
  }

  const timeline = [
    ...machines.map(m => ({ type: 'machine' as const, name: m.name, at: m.created_at })),
    ...tunnels.map(t => ({ type: 'tunnel' as const, name: t.name, at: t.created_at })),
  ].sort((a, b) => new Date(b.at).getTime() - new Date(a.at).getTime()).slice(0, 10)

  return (
    <div className="space-y-8">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-gray-900">System Status</h1>
          <p className="text-gray-500 mt-1">Health overview of all components</p>
        </div>
        <button onClick={refreshAll} className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg hover:bg-gray-50 text-sm font-medium">
          ↺ Refresh All
        </button>
      </div>

      {/* VPS Status */}
      <div className="bg-white rounded-xl shadow-sm border p-6">
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-lg font-semibold text-gray-900">VPS</h2>
        </div>
        {vps ? (
          <div className="flex flex-wrap items-center gap-4">
            <div>
              <div className="text-sm text-gray-500">Host</div>
              <div className="font-medium">{vps.host}:{vps.port}</div>
            </div>
            <div>
              <div className="text-sm text-gray-500">Domain</div>
              <div className="font-medium">{vps.domain}</div>
            </div>
            <div>
              <div className="text-sm text-gray-500">Status</div>
              {vpsStatus ? (
                <span className={`text-sm ${vpsStatus.ok ? 'text-green-600' : 'text-red-600'}`}>
                  {vpsStatus.ok ? '✅' : '❌'} {vpsStatus.message}
                </span>
              ) : (
                <StatusBadge status="connected" />
              )}
            </div>
            <button onClick={checkVpsStatus} disabled={vpsChecking} className="px-3 py-1.5 border border-gray-300 text-gray-700 rounded-lg hover:bg-gray-50 text-sm disabled:opacity-50 ml-auto">
              {vpsChecking ? 'Checking...' : 'Check Status'}
            </button>
          </div>
        ) : (
          <p className="text-gray-400 text-sm">No VPS configured.</p>
        )}
      </div>

      {/* Machines */}
      <div className="bg-white rounded-xl shadow-sm border overflow-hidden">
        <div className="p-4 border-b">
          <h2 className="text-lg font-semibold text-gray-900">Machines ({machines.length})</h2>
        </div>
        {machines.length === 0 ? (
          <p className="text-gray-400 text-sm p-6">No machines configured.</p>
        ) : (
          <table className="w-full text-sm">
            <thead className="bg-gray-50 border-b">
              <tr>
                {['Name', 'Host', 'Status', 'Last Seen', 'Check'].map(h => (
                  <th key={h} className="text-left px-4 py-3 text-xs font-semibold text-gray-500 uppercase tracking-wide">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y">
              {machines.map(m => (
                <tr key={m.id} className="hover:bg-gray-50">
                  <td className="px-4 py-3 font-medium text-gray-900">{m.name}</td>
                  <td className="px-4 py-3 text-gray-600">{m.host}:{m.port}</td>
                  <td className="px-4 py-3">
                    {machineStatuses[m.id] ? (
                      <span className={`text-xs ${machineStatuses[m.id].ok ? 'text-green-600' : 'text-red-600'}`}>
                        {machineStatuses[m.id].ok ? '✅' : '❌'} {machineStatuses[m.id].message}
                      </span>
                    ) : (
                      <StatusBadge status={m.status} />
                    )}
                  </td>
                  <td className="px-4 py-3 text-gray-500">{m.last_seen ? new Date(m.last_seen).toLocaleString() : 'Never'}</td>
                  <td className="px-4 py-3">
                    <button onClick={() => checkMachineStatus(m.id)} className="px-2 py-1 text-xs border border-gray-200 rounded hover:bg-gray-50">Check</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {/* Tunnels */}
      <div className="bg-white rounded-xl shadow-sm border overflow-hidden">
        <div className="p-4 border-b">
          <h2 className="text-lg font-semibold text-gray-900">Tunnels ({tunnels.length})</h2>
        </div>
        {tunnels.length === 0 ? (
          <p className="text-gray-400 text-sm p-6">No tunnels configured.</p>
        ) : (
          <table className="w-full text-sm">
            <thead className="bg-gray-50 border-b">
              <tr>
                {['Name', 'Subdomain', 'Protocol', 'Status', 'Machine'].map(h => (
                  <th key={h} className="text-left px-4 py-3 text-xs font-semibold text-gray-500 uppercase tracking-wide">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y">
              {tunnels.map(t => (
                <tr key={t.id} className="hover:bg-gray-50">
                  <td className="px-4 py-3 font-medium text-gray-900">{t.name}</td>
                  <td className="px-4 py-3 text-gray-600">{t.subdomain}</td>
                  <td className="px-4 py-3">
                    <span className="uppercase text-xs font-mono bg-gray-100 px-2 py-0.5 rounded">{t.protocol}</span>
                  </td>
                  <td className="px-4 py-3"><StatusBadge status={t.status} /></td>
                  <td className="px-4 py-3 text-gray-600">{machines.find(m => m.id === t.machine_id)?.name ?? t.machine_id}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {/* Activity Timeline */}
      <div className="bg-white rounded-xl shadow-sm border p-6">
        <h2 className="text-lg font-semibold text-gray-900 mb-4">Activity Timeline</h2>
        {timeline.length === 0 ? (
          <p className="text-gray-400 text-sm">No activity yet.</p>
        ) : (
          <div className="space-y-3">
            {timeline.map((item, i) => (
              <div key={i} className={`flex items-start gap-3 pl-3 border-l-2 ${item.type === 'machine' ? 'border-blue-400' : 'border-green-400'}`}>
                <div>
                  <div className="text-sm text-gray-800">
                    {item.type === 'machine' ? `Machine '${item.name}' added` : `Tunnel '${item.name}' created`}
                  </div>
                  <div className="text-xs text-gray-400">{new Date(item.at).toLocaleString()}</div>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
