import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { CheckCircle, Circle, ClipboardCopy, Key, Upload, RefreshCw } from 'lucide-react'
import { machinesApi } from '../api/machines'
import { tunnelsApi } from '../api/tunnels'
import { localApi } from '../api/local'
import StatusBadge from '../components/StatusBadge'
import { toast } from '../lib/toast'
import type { Machine, Tunnel } from '../types'

export default function DashboardPage() {
  const navigate = useNavigate()

  const { data: machinesData } = useQuery({
    queryKey: ['machines'],
    queryFn: () => machinesApi.list(),
  })

  const { data: tunnelsData } = useQuery({
    queryKey: ['tunnels'],
    queryFn: () => tunnelsApi.list(),
  })

  const { data: localStatus, refetch: refetchLocalStatus } = useQuery({
    queryKey: ['local-status'],
    queryFn: () => localApi.status(),
    refetchInterval: 10000,
  })

  const machines: Machine[] = machinesData?.data ?? []
  const tunnels: Tunnel[] = tunnelsData?.data ?? []

  const [showKeyPanel, setShowKeyPanel] = useState(false)
  const [replaceMode, setReplaceMode] = useState<'choose' | 'upload'>('choose')
  const [replacePriv, setReplacePriv] = useState('')
  const [replacePub, setReplacePub] = useState('')
  const [replacing, setReplacing] = useState(false)

  const activeMachines = machines.filter(m => m.status === 'active' || m.status === 'connected').length
  const activeTunnels = tunnels.filter(t => t.status === 'active' || t.status === 'connected').length
  const servicesOk = localStatus?.caddy_active === 'active' && localStatus?.rathole_active === 'active'
  const isHealthy = servicesOk && machines.length > 0 && tunnels.length > 0

  const steps = [
    { label: 'Local services running', done: !!servicesOk, path: '/vps' },
    { label: 'Bootstrap a machine', done: machines.length > 0, path: '/machines' },
    { label: 'Add a tunnel', done: tunnels.length > 0, path: '/tunnels' },
    { label: 'Deploy & go live', done: false, path: '/status', isInfo: true },
  ]

  const showStepper = !servicesOk || machines.length === 0 || tunnels.length === 0

  const copyUrl = (subdomain: string) => {
    if (localStatus?.domain) {
      navigator.clipboard.writeText(`${subdomain}.${localStatus.domain}`)
      toast.success('URL copied!')
    }
  }

  const downloadSSHKey = async () => {
    try {
      const blob = await localApi.downloadSSHKey()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = 'gopher_id_rsa'
      a.click()
      URL.revokeObjectURL(url)
    } catch {
      toast.error('No SSH key available yet — bootstrap a machine first')
    }
  }

  const handleGenerateKey = async () => {
    setReplacing(true)
    try {
      await localApi.generateSSHKey()
      toast.success('New SSH key pair generated')
      setShowKeyPanel(false)
      refetchLocalStatus()
    } catch {
      toast.error('Failed to generate key')
    } finally {
      setReplacing(false)
    }
  }

  const handleUploadKey = async () => {
    setReplacing(true)
    try {
      await localApi.uploadSSHKey(replacePriv, replacePub)
      toast.success('SSH key pair updated')
      setShowKeyPanel(false)
      setReplacePriv('')
      setReplacePub('')
      refetchLocalStatus()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Invalid key pair — ensure they match')
    } finally {
      setReplacing(false)
    }
  }

  const readKeyFile = (file: File, setter: (v: string) => void) => {
    const reader = new FileReader()
    reader.onload = e => setter((e.target?.result as string) ?? '')
    reader.readAsText(file)
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
              <div className="text-xs text-gray-400 truncate">router.{localStatus.domain}</div>
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
          <StatusBadge status={isHealthy ? 'active' : 'inactive'} className="font-semibold" />
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
                      router.{localStatus.domain}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        </div>
      )}
      {/* SSH Key */}
      {localStatus?.ssh_public_key && (
        <div className="bg-white rounded-xl shadow-sm border p-6 space-y-4">
          <div className="flex items-center justify-between">
            <div>
              <h2 className="text-lg font-semibold text-gray-900">Server SSH Key</h2>
              <p className="text-sm text-gray-500 mt-0.5">Used to SSH into bootstrapped machines through their tunnels</p>
            </div>
            <div className="flex gap-2">
              <button
                onClick={downloadSSHKey}
                className="px-3 py-1.5 bg-gray-800 text-white rounded-lg hover:bg-gray-900 text-sm font-medium flex items-center gap-1.5"
              >
                ⬇ Download
              </button>
              <button
                onClick={() => { setShowKeyPanel(v => !v); setReplaceMode('choose') }}
                className="px-3 py-1.5 border border-gray-300 text-gray-600 rounded-lg hover:bg-gray-50 text-sm font-medium flex items-center gap-1.5"
              >
                <Key size={13} /> Replace
              </button>
            </div>
          </div>
          <div className="bg-gray-50 rounded-lg p-3">
            <div className="text-xs text-gray-500 mb-1">Public key (add to authorized_keys on any machine you want direct access to)</div>
            <div className="flex items-center gap-2">
              <code className="text-xs text-gray-700 break-all flex-1">{localStatus.ssh_public_key}</code>
              <button onClick={() => { navigator.clipboard.writeText(localStatus.ssh_public_key); toast.success('Copied!') }} className="shrink-0 text-gray-400 hover:text-gray-600">
                <ClipboardCopy size={14} />
              </button>
            </div>
          </div>
          <p className="text-xs text-gray-400">Usage: <code className="bg-gray-100 px-1 rounded">ssh -i gopher_id_rsa -p &lt;tunnel-port&gt; &lt;username&gt;@{localStatus.domain ?? 'your-vps'}</code></p>

          {showKeyPanel && (
            <div className="border-t pt-4 space-y-4">
              {replaceMode === 'choose' ? (
                <div className="grid grid-cols-2 gap-3">
                  <button
                    onClick={handleGenerateKey}
                    disabled={replacing}
                    className="flex flex-col items-center gap-2 p-4 border-2 border-blue-200 rounded-xl hover:border-blue-400 hover:bg-blue-50 transition-all disabled:opacity-50"
                  >
                    <RefreshCw size={20} className={replacing ? 'text-blue-300 animate-spin' : 'text-blue-500'} />
                    <div className="font-semibold text-gray-800 text-sm">Generate new</div>
                    <div className="text-xs text-gray-400">New RSA 4096-bit pair</div>
                  </button>
                  <button
                    onClick={() => setReplaceMode('upload')}
                    className="flex flex-col items-center gap-2 p-4 border-2 border-gray-200 rounded-xl hover:border-gray-400 hover:bg-gray-50 transition-all"
                  >
                    <Upload size={20} className="text-gray-500" />
                    <div className="font-semibold text-gray-800 text-sm">Upload own</div>
                    <div className="text-xs text-gray-400">Use id_rsa / id_rsa.pub</div>
                  </button>
                </div>
              ) : (
                <div className="space-y-3">
                  <div>
                    <label className="block text-xs font-medium text-gray-600 mb-1">Private key (PEM / OpenSSH)</label>
                    <div className="flex gap-2">
                      <textarea
                        value={replacePriv}
                        onChange={e => setReplacePriv(e.target.value)}
                        rows={4}
                        placeholder={'-----BEGIN RSA PRIVATE KEY-----\n...'}
                        className="flex-1 border border-gray-300 rounded-lg px-2 py-1.5 text-xs font-mono resize-none focus:outline-none focus:ring-2 focus:ring-blue-500"
                      />
                      <label className="cursor-pointer flex flex-col items-center justify-center px-2 border border-gray-300 rounded-lg hover:bg-gray-50 text-gray-500 text-xs gap-1">
                        <Upload size={12} />
                        Browse
                        <input type="file" className="hidden" onChange={e => { if (e.target.files?.[0]) readKeyFile(e.target.files[0], setReplacePriv) }} />
                      </label>
                    </div>
                  </div>
                  <div>
                    <label className="block text-xs font-medium text-gray-600 mb-1">Public key (authorized_keys format)</label>
                    <div className="flex gap-2">
                      <textarea
                        value={replacePub}
                        onChange={e => setReplacePub(e.target.value)}
                        rows={2}
                        placeholder="ssh-rsa AAAA..."
                        className="flex-1 border border-gray-300 rounded-lg px-2 py-1.5 text-xs font-mono resize-none focus:outline-none focus:ring-2 focus:ring-blue-500"
                      />
                      <label className="cursor-pointer flex flex-col items-center justify-center px-2 border border-gray-300 rounded-lg hover:bg-gray-50 text-gray-500 text-xs gap-1">
                        <Upload size={12} />
                        Browse
                        <input type="file" className="hidden" onChange={e => { if (e.target.files?.[0]) readKeyFile(e.target.files[0], setReplacePub) }} />
                      </label>
                    </div>
                  </div>
                  <div className="flex gap-2">
                    <button
                      onClick={() => setReplaceMode('choose')}
                      className="px-3 py-1.5 border border-gray-300 text-gray-600 rounded-lg text-xs hover:bg-gray-50"
                    >
                      ← Back
                    </button>
                    <button
                      onClick={handleUploadKey}
                      disabled={replacing || !replacePriv || !replacePub}
                      className="flex-1 bg-blue-600 text-white py-1.5 rounded-lg text-xs font-semibold hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed"
                    >
                      {replacing ? 'Validating…' : 'Save key pair'}
                    </button>
                  </div>
                </div>
              )}
            </div>
          )}
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
                    {localStatus?.domain ? (
                      <div className="flex items-center gap-1">
                        <span className="text-xs text-gray-400">{t.subdomain}.{localStatus.domain}</span>
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
