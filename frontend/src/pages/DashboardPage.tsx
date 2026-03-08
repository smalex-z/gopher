import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { CheckCircle, Circle, ClipboardCopy } from 'lucide-react'
import client from '../api/client'
import { vpsApi } from '../api/vps'
import { machinesApi } from '../api/machines'
import { tunnelsApi } from '../api/tunnels'
import { localApi } from '../api/local'
import StatusBadge from '../components/StatusBadge'
import { toast } from '../lib/toast'
import type { StatusData, Machine, Tunnel } from '../types'

export default function DashboardPage() {
  const navigate = useNavigate()

  useQuery({
    queryKey: ['status'],
    queryFn: () => client.get<{ data: StatusData }>('/status').then(r => r.data.data),
    refetchInterval: 30000,
  })

  const { data: vpsData } = useQuery({
    queryKey: ['vps'],
    queryFn: () => vpsApi.get(),
    retry: false,
  })

  const { data: machinesData } = useQuery({
    queryKey: ['machines'],
    queryFn: () => machinesApi.list(),
  })

  const { data: tunnelsData } = useQuery({
    queryKey: ['tunnels'],
    queryFn: () => tunnelsApi.list(),
  })

  const { data: localStatus } = useQuery({
    queryKey: ['local-status'],
    queryFn: () => localApi.status(),
    refetchInterval: 10000,
  })

  const vps = vpsData?.data
  const machines: Machine[] = machinesData?.data ?? []
  const tunnels: Tunnel[] = tunnelsData?.data ?? []

  const activeMachines = machines.filter(m => m.status === 'active' || m.status === 'connected').length
  const activeTunnels = tunnels.filter(t => t.status === 'active' || t.status === 'connected').length
  const isHealthy = !!vps && machines.length > 0 && tunnels.length > 0

  const steps = [
    { label: 'Configure VPS', done: !!vps, path: '/vps' },
    { label: 'Add a Machine', done: machines.length > 0, path: '/machines' },
    { label: 'Add a Tunnel', done: tunnels.length > 0, path: '/tunnels' },
    { label: 'Deploy & Go Live', done: false, path: '/status', isInfo: true },
  ]

  const showStepper = !vps || machines.length === 0 || tunnels.length === 0

  const copyUrl = (subdomain: string) => {
    if (vps?.domain) {
      navigator.clipboard.writeText(`${subdomain}.${vps.domain}`)
      toast.success('URL copied!')
    }
  }

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-bold text-gray-900">Dashboard</h1>
        <p className="text-gray-500 mt-1">Overview of your Gopher tunnel gateway</p>
      </div>

      {/* Stat cards */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <div className="bg-white rounded-xl p-5 shadow-sm border">
          <div className="text-sm text-gray-500 mb-1">This Server</div>
          {localStatus?.domain ? (
            <>
              <StatusBadge
                status={localStatus.caddy_active === 'active' && localStatus.rathole_active === 'active' ? 'active' : 'inactive'}
                className="font-semibold"
              />
              <div className="text-xs text-gray-400 mt-2 truncate">{localStatus.domain}</div>
              <div className="text-xs text-gray-400 truncate">dashboard.{localStatus.domain}</div>
            </>
          ) : (
            <>
              <StatusBadge status="inactive" className="font-semibold" />
              <div className="text-xs text-gray-400 mt-2">No domain configured</div>
            </>
          )}
        </div>

        <div onClick={() => navigate('/machines')} className="bg-white rounded-xl p-5 shadow-sm border cursor-pointer hover:shadow-md transition-shadow">
          <div className="text-sm text-gray-500 mb-1">Machines</div>
          <div className="text-2xl font-bold text-gray-900">{machines.length}</div>
          <div className="text-xs text-gray-500 mt-1">{activeMachines} active</div>
        </div>

        <div onClick={() => navigate('/tunnels')} className="bg-white rounded-xl p-5 shadow-sm border cursor-pointer hover:shadow-md transition-shadow">
          <div className="text-sm text-gray-500 mb-1">Tunnels</div>
          <div className="text-2xl font-bold text-gray-900">{tunnels.length}</div>
          <div className="text-xs text-gray-500 mt-1">{activeTunnels} active</div>
        </div>

        <div onClick={() => navigate('/status')} className="bg-white rounded-xl p-5 shadow-sm border cursor-pointer hover:shadow-md transition-shadow">
          <div className="text-sm text-gray-500 mb-1">System Health</div>
          {isHealthy ? (
            <StatusBadge status="active" className="font-semibold" />
          ) : (
            <StatusBadge status="inactive" className="font-semibold" />
          )}
          <div className="text-xs text-gray-400 mt-2">{isHealthy ? 'All systems operational' : 'Setup required'}</div>
        </div>
      </div>

      {/* Local Services status */}
      {localStatus && (
        <div className="bg-white rounded-xl shadow-sm border p-6">
          <h2 className="text-lg font-semibold text-gray-900 mb-4">Local Services</h2>
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
            {[
              {
                name: 'Caddy (reverse proxy)',
                installed: localStatus.caddy_installed,
                active: localStatus.caddy_active,
              },
              {
                name: 'Rathole Server (tunnels)',
                installed: localStatus.rathole_installed,
                active: localStatus.rathole_active,
              },
            ].map(svc => {
              const status = svc.installed ? svc.active : 'not-installed'
              const color =
                status === 'active' ? 'bg-green-50 border-green-200' :
                status === 'activating' ? 'bg-yellow-50 border-yellow-200' :
                'bg-red-50 border-red-200'
              const badge =
                status === 'active' ? 'bg-green-100 text-green-700' :
                status === 'activating' ? 'bg-yellow-100 text-yellow-700' :
                'bg-red-100 text-red-700'
              return (
                <div key={svc.name} className={`rounded-lg border p-4 ${color}`}>
                  <div className="flex items-center justify-between">
                    <span className="text-sm font-medium text-gray-800">{svc.name}</span>
                    <span className={`text-xs font-medium px-2 py-0.5 rounded-full ${badge}`}>
                      {status}
                    </span>
                  </div>
                  {localStatus.domain && status === 'active' && svc.name.startsWith('Caddy') && (
                    <div className="text-xs text-gray-500 mt-1 truncate">
                      dashboard.{localStatus.domain}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        </div>
      )}
      {showStepper && (
        <div className="bg-white rounded-xl shadow-sm border p-6">
          <h2 className="text-lg font-semibold text-gray-900 mb-4">Get Started</h2>
          <div className="space-y-3">
            {steps.map((step, i) => (
              <div key={i} className="flex items-center gap-4">
                <div className="flex-shrink-0">
                  {step.done ? (
                    <CheckCircle className="w-6 h-6 text-green-500" />
                  ) : (
                    <Circle className="w-6 h-6 text-gray-300" />
                  )}
                </div>
                <div className="flex-1">
                  <span className={step.done ? 'line-through text-gray-400' : 'text-gray-800 font-medium'}>
                    {i + 1}. {step.label}
                  </span>
                </div>
                {!step.done && (
                  <button
                    onClick={() => navigate(step.path)}
                    className={`text-sm px-3 py-1 rounded-lg ${step.isInfo ? 'bg-blue-50 text-blue-600 hover:bg-blue-100' : 'bg-blue-600 text-white hover:bg-blue-700'}`}
                  >
                    {step.isInfo ? 'View Status' : 'Configure'}
                  </button>
                )}
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Architecture flow */}
      <div className="bg-white rounded-xl shadow-sm border p-6">
        <h2 className="text-lg font-semibold text-gray-900 mb-4">How It Works</h2>
        <div className="flex flex-wrap items-center gap-2 justify-center py-2">
          <div className="bg-purple-100 text-purple-800 font-medium px-4 py-2 rounded-lg text-sm">🌐 Internet</div>
          <span className="text-gray-400 font-bold">→</span>
          <div className="bg-blue-100 text-blue-800 font-medium px-4 py-2 rounded-lg text-sm">🖥 Caddy on VPS</div>
          <span className="text-gray-400 font-bold">→</span>
          <div className="bg-orange-100 text-orange-800 font-medium px-4 py-2 rounded-lg text-sm">🔗 Rathole Tunnel</div>
          <span className="text-gray-400 font-bold">→</span>
          <div className="bg-green-100 text-green-800 font-medium px-4 py-2 rounded-lg text-sm">💻 Your Machine</div>
          <span className="text-gray-400 font-bold">→</span>
          <div className="bg-teal-100 text-teal-800 font-medium px-4 py-2 rounded-lg text-sm">🚀 Local Service</div>
        </div>
      </div>

      {/* Recent activity */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
        <div className="bg-white rounded-xl shadow-sm border p-6">
          <h2 className="text-lg font-semibold text-gray-900 mb-4">Recent Machines</h2>
          {machines.length === 0 ? (
            <p className="text-sm text-gray-400">No machines configured yet.</p>
          ) : (
            <div className="space-y-3">
              {machines.slice(-3).reverse().map(m => (
                <div key={m.id} className="flex items-center justify-between">
                  <div>
                    <div className="font-medium text-sm text-gray-800">{m.name}</div>
                    <div className="text-xs text-gray-400">{m.host}:{m.port}</div>
                  </div>
                  <StatusBadge status={m.status} />
                </div>
              ))}
            </div>
          )}
        </div>

        <div className="bg-white rounded-xl shadow-sm border p-6">
          <h2 className="text-lg font-semibold text-gray-900 mb-4">Recent Tunnels</h2>
          {tunnels.length === 0 ? (
            <p className="text-sm text-gray-400">No tunnels configured yet.</p>
          ) : (
            <div className="space-y-3">
              {tunnels.slice(-3).reverse().map(t => (
                <div key={t.id} className="flex items-center justify-between">
                  <div>
                    <div className="font-medium text-sm text-gray-800">{t.name}</div>
                    {vps?.domain ? (
                      <div className="flex items-center gap-1">
                        <span className="text-xs text-gray-400">{t.subdomain}.{vps.domain}</span>
                        <button onClick={() => copyUrl(t.subdomain)} className="text-gray-300 hover:text-gray-600">
                          <ClipboardCopy size={12} />
                        </button>
                      </div>
                    ) : (
                      <span className="text-xs text-gray-400">{t.subdomain}</span>
                    )}
                  </div>
                  <StatusBadge status={t.status} />
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
