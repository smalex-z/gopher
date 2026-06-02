import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { ShieldAlert, Copy, X, ChevronDown, ChevronUp } from 'lucide-react'
import { localApi } from '../api/local'
import { toast } from '../lib/toast'

// CustomServicesWarningBanner surfaces the names of user-managed rathole
// services that were detected in /etc/rathole/server.toml's BEGIN/END
// CUSTOM CONFIGURATION block during the noise migration.
//
// Those services are owned by the operator (not Gopher) and therefore
// weren't reachable by the automatic client.toml push — they need a
// manual update to add the [client.transport] noise block with the
// pubkey shown below, or they silently fail to reconnect.
//
// Banner is one-time: hidden when the list is empty OR after the
// operator clicks Dismiss. Server stores the dismissed flag.
//
// Rendered inside AppShell alongside the existing AgentMigrationBanner.
export default function CustomServicesWarningBanner() {
  const qc = useQueryClient()
  const [expanded, setExpanded] = useState(false)

  const { data } = useQuery({
    queryKey: ['local-status'],
    queryFn: localApi.status,
    refetchInterval: 60_000,
    retry: false,
  })

  const dismiss = useMutation({
    mutationFn: () => localApi.dismissCustomServicesWarning(),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['local-status'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const services = data?.rathole_custom_services_warning ?? []
  const pubkey = data?.rathole_noise_pubkey ?? ''
  if (services.length === 0) return null

  const copyPubkey = async () => {
    try {
      await navigator.clipboard.writeText(pubkey)
      toast.success('Noise pubkey copied. Paste into [client.transport.noise] remote_public_key on each affected client.')
    } catch {
      toast.error('Clipboard blocked by browser.')
    }
  }

  return (
    <div className="mb-6 bg-amber-50 border border-amber-200 rounded-xl px-4 py-3">
      <div className="flex items-start gap-3">
        <ShieldAlert size={18} className="text-amber-600 mt-0.5 shrink-0" />
        <div className="min-w-0 flex-1">
          <div className="flex items-center justify-between gap-3">
            <div className="text-sm font-semibold text-amber-900">
              {services.length} user-managed rathole service{services.length === 1 ? '' : 's'} need{services.length === 1 ? 's' : ''} a noise pubkey update
            </div>
            <button
              onClick={() => dismiss.mutate()}
              disabled={dismiss.isPending}
              className="text-amber-600 hover:text-amber-800 disabled:opacity-50 shrink-0"
              aria-label="Dismiss"
              title="Dismiss this warning (you've handled it manually)"
            >
              <X size={16} />
            </button>
          </div>
          <p className="text-xs text-amber-800 mt-1">
            Gopher just enabled encrypted rathole transport, but services in{' '}
            <code className="text-[11px] bg-amber-100 px-1 rounded">/etc/rathole/server.toml</code>'s
            custom block are managed by you — their client-side configs need a manual
            <code className="text-[11px] bg-amber-100 px-1 rounded mx-1">[client.transport]</code>
            noise block or they silently stop reconnecting.
          </p>

          <button
            onClick={() => setExpanded(v => !v)}
            className="mt-2 text-xs text-amber-900 hover:text-amber-700 font-medium flex items-center gap-1"
          >
            {expanded ? <ChevronUp size={12} /> : <ChevronDown size={12} />}
            {expanded ? 'Hide details' : 'Show pubkey + affected services'}
          </button>

          {expanded && (
            <div className="mt-3 space-y-3">
              <div>
                <p className="text-xs font-medium text-amber-900 mb-1">Affected services:</p>
                <ul className="text-xs text-amber-900 font-mono list-disc list-inside space-y-0.5">
                  {services.map(name => (
                    <li key={name}>[server.services.{name}]</li>
                  ))}
                </ul>
              </div>
              <div>
                <p className="text-xs font-medium text-amber-900 mb-1">Add this to each affected client.toml:</p>
                <pre className="bg-gray-900 text-gray-100 text-[11px] rounded-lg p-2 overflow-x-auto font-mono">
{`[client.transport]
type = "noise"

[client.transport.noise]
remote_public_key = "${pubkey}"`}
                </pre>
                <button
                  onClick={copyPubkey}
                  className="mt-1 text-xs text-amber-900 hover:text-amber-700 flex items-center gap-1"
                >
                  <Copy size={11} /> Copy pubkey only
                </button>
              </div>
              <p className="text-[11px] text-amber-800">
                After updating each client config, rathole-client's notify watcher reloads
                automatically — no restart needed.
              </p>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
