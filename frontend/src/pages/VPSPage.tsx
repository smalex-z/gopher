import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Copy, Check, ExternalLink, RefreshCw } from 'lucide-react'
import { localApi } from '../api/local'
import StatusBadge from '../components/StatusBadge'

function ServiceRow({ label, active }: { label: string; active: string }) {
  const color =
    active === 'active' ? 'bg-green-100 text-green-700' :
    active === 'activating' ? 'bg-yellow-100 text-yellow-700' :
    active === 'not-found' ? 'bg-gray-100 text-gray-500' :
    'bg-red-100 text-red-700'
  return (
    <div className="flex items-center justify-between py-2">
      <span className="text-sm text-gray-700">{label}</span>
      <span className={`text-xs font-medium px-2 py-0.5 rounded-full ${color}`}>{active}</span>
    </div>
  )
}

export default function ServerPage() {
  const [keyCopied, setKeyCopied] = useState(false)

  const { data: status, refetch, isLoading } = useQuery({
    queryKey: ['local-status'],
    queryFn: () => localApi.status(),
    refetchInterval: 15000,
  })

  const copyKey = () => {
    if (!status?.ssh_public_key) return
    navigator.clipboard.writeText(status.ssh_public_key).then(() => {
      setKeyCopied(true)
      setTimeout(() => setKeyCopied(false), 2000)
    })
  }

  if (isLoading) return <div className="text-gray-400 text-center py-12">Loading…</div>

  const bothActive = status?.caddy_active === 'active' && status?.rathole_active === 'active'

  return (
    <div className="max-w-2xl space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-gray-900">This Server</h1>
          <p className="text-gray-500 mt-1">Gopher is running here — local services and SSH access</p>
        </div>
        <button onClick={() => refetch()} className="p-2 text-gray-400 hover:text-gray-600 rounded-lg hover:bg-gray-100" title="Refresh">
          <RefreshCw size={16} />
        </button>
      </div>

      {/* Domain + overall status */}
      <div className="bg-white rounded-xl shadow-sm border p-6">
        <div className="flex items-center gap-3 mb-4">
          <StatusBadge status={bothActive ? 'active' : 'inactive'} />
          <span className="font-semibold text-gray-900">{status?.domain || 'No domain configured'}</span>
        </div>
        {status?.domain && (
          <a
            href={`https://dashboard.${status.domain}`}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1.5 text-sm text-blue-600 hover:underline"
          >
            <ExternalLink size={14} />
            https://dashboard.{status.domain}
          </a>
        )}
        {!status?.domain && (
          <p className="text-sm text-amber-600">
            Domain not configured. Go through the setup wizard to set one.
          </p>
        )}
      </div>

      {/* Service status */}
      <div className="bg-white rounded-xl shadow-sm border p-6">
        <h2 className="text-base font-semibold text-gray-900 mb-3">Systemd Services</h2>
        <div className="divide-y">
          <ServiceRow label="caddy.service (reverse proxy)" active={status?.caddy_installed ? (status.caddy_active || 'unknown') : 'not-installed'} />
          <ServiceRow label="rathole-server.service (tunnel server)" active={status?.rathole_installed ? (status.rathole_active || 'unknown') : 'not-installed'} />
        </div>
      </div>

      {/* SSH public key */}
      <div className="bg-white rounded-xl shadow-sm border p-6">
        <div className="flex items-center justify-between mb-2">
          <h2 className="text-base font-semibold text-gray-900">Server SSH Public Key</h2>
          {status?.ssh_public_key && (
            <button onClick={copyKey} className="flex items-center gap-1 text-xs text-gray-500 hover:text-gray-700">
              {keyCopied ? <Check size={14} className="text-green-500" /> : <Copy size={14} />}
              {keyCopied ? 'Copied!' : 'Copy'}
            </button>
          )}
        </div>
        {status?.ssh_public_key ? (
          <>
            <textarea
              readOnly
              value={status.ssh_public_key}
              rows={3}
              className="w-full border border-gray-200 rounded-lg px-3 py-2 text-xs font-mono bg-gray-50 text-gray-600 resize-none focus:outline-none"
            />
            <p className="text-xs text-gray-400 mt-1">
              This key is automatically added to <code>~/.ssh/authorized_keys</code> on every bootstrapped machine so Gopher can SSH back in through the tunnel.
            </p>
          </>
        ) : (
          <p className="text-sm text-gray-400">
            Generated automatically when the first machine bootstraps.
          </p>
        )}
      </div>
    </div>
  )
}

