import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Copy, Check, ExternalLink, RefreshCw, Globe, Lock, ShieldAlert } from 'lucide-react'
import { localApi } from '../api/local'
import StatusBadge from '../components/StatusBadge'
import { toast } from '../lib/toast'

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
  const qc = useQueryClient()
  const [keyCopied, setKeyCopied] = useState(false)

  const { data: status, refetch, isLoading } = useQuery({
    queryKey: ['local-status'],
    queryFn: () => localApi.status(),
    refetchInterval: 15000,
  })

  const dashboardMutation = useMutation({
    mutationFn: (dashboardPrivate: boolean) => localApi.setServerPorts(dashboardPrivate),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['local-status'] }),
    onError: (e: Error) => toast.error(e.message),
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
            href={`https://router.${status.domain}`}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1.5 text-sm text-blue-600 hover:underline"
          >
            <ExternalLink size={14} />
            https://router.{status.domain}
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

      {/* Port access */}
      <div className="bg-white rounded-xl shadow-sm border p-6">
        <h2 className="text-base font-semibold text-gray-900 mb-4">Port Access</h2>
        <div className="space-y-4">
          {/* Port 22 — always open */}
          <div className="flex items-start justify-between gap-4">
            <div>
              <div className="text-sm font-medium text-gray-800">Port 22 — VPS SSH</div>
              <div className="text-xs text-gray-500 mt-0.5">
                Always open. Required for server management — closing it via iptables risks permanent lockout.
                To restrict by IP, use the Firewall tab custom rules (e.g. <code className="bg-gray-100 px-1 rounded">-s 1.2.3.4 -p tcp --dport 22 -j ACCEPT</code> then drop all others).
              </div>
            </div>
            <span className="shrink-0 text-xs font-medium px-2 py-1 rounded-full bg-green-100 text-green-700 flex items-center gap-1">
              <Globe size={11} /> Public
            </span>
          </div>

          {/* Port 80/443 — Caddy */}
          <div className="flex items-start justify-between gap-4">
            <div>
              <div className="text-sm font-medium text-gray-800">Ports 80 / 443 — HTTP/HTTPS</div>
              <div className="text-xs text-gray-500 mt-0.5">Always open when Caddy is configured.</div>
            </div>
            <span className="shrink-0 text-xs font-medium px-2 py-1 rounded-full bg-green-100 text-green-700 flex items-center gap-1">
              <Globe size={11} /> Public
            </span>
          </div>

          {/* Dashboard port — toggleable */}
          <div className="flex items-start justify-between gap-4">
            <div>
              <div className="text-sm font-medium text-gray-800">Port {status?.dashboard_port ?? 4321} — Gopher dashboard</div>
              <div className="text-xs text-gray-500 mt-0.5">
                {status?.dashboard_private
                  ? <>Restricted to localhost. Access via <code className="bg-gray-100 px-1 rounded">router.{status.domain}</code>.</>
                  : 'Publicly accessible. Anyone who knows the port can reach the dashboard login.'}
              </div>
              {!status?.domain && (
                <div className="text-xs text-amber-600 mt-0.5 flex items-center gap-1">
                  <ShieldAlert size={11} /> No domain configured — dashboard port must stay public.
                </div>
              )}
            </div>
            <div className="shrink-0 flex gap-1.5">
              <button
                onClick={() => dashboardMutation.mutate(false)}
                disabled={dashboardMutation.isPending || !status?.dashboard_private}
                className={`text-xs font-medium px-2.5 py-1 rounded-lg border transition-colors flex items-center gap-1 ${
                  !status?.dashboard_private
                    ? 'bg-green-600 text-white border-green-600'
                    : 'bg-white text-gray-600 border-gray-300 hover:bg-gray-50 disabled:opacity-50'
                }`}
              >
                <Globe size={11} /> Public
              </button>
              <button
                onClick={() => dashboardMutation.mutate(true)}
                disabled={dashboardMutation.isPending || status?.dashboard_private || !status?.domain || status?.firewall_mode !== 'gopher'}
                title={!status?.domain ? 'Requires domain + Caddy' : status?.firewall_mode !== 'gopher' ? 'Requires Gopher-managed firewall' : undefined}
                className={`text-xs font-medium px-2.5 py-1 rounded-lg border transition-colors flex items-center gap-1 ${
                  status?.dashboard_private
                    ? 'bg-slate-700 text-white border-slate-700'
                    : 'bg-white text-gray-600 border-gray-300 hover:bg-gray-50 disabled:opacity-50 disabled:cursor-not-allowed'
                }`}
              >
                <Lock size={11} /> Caddy only
              </button>
            </div>
          </div>
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

