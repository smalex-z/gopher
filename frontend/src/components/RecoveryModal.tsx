import { useEffect, useState } from 'react'
import { AlertTriangle, Copy, Download, X } from 'lucide-react'
import { machinesApi } from '../api/machines'
import { toast } from '../lib/toast'

interface Props {
  isOpen: boolean
  onClose: () => void
  machineID: string
  machineName: string
  // Why the modal opened — typically the error from POST /recover. Shown
  // verbatim so the operator knows whether to fix disk space, restart a
  // service, or just paste the script.
  reason: string
}

// RecoveryModal is the third tier of the recovery fallback chain. The first
// two (agent push, SSH-via-tunnel) failed — both require the rathole tunnel
// to be working, which is exactly what's broken when recovery is needed. So
// the operator's last option is to run a script directly on the machine.
//
// We fetch the script lazily on open (not when the button is rendered) so
// we don't make an API call for every machine row on every render.
export default function RecoveryModal({ isOpen, onClose, machineID, machineName, reason }: Props) {
  const [script, setScript] = useState<string>('')
  const [loading, setLoading] = useState(false)
  const [fetchErr, setFetchErr] = useState<string>('')

  useEffect(() => {
    if (!isOpen) return
    setLoading(true)
    setFetchErr('')
    machinesApi.ratholeConfigScript(machineID)
      .then(text => setScript(text))
      .catch(err => setFetchErr(err instanceof Error ? err.message : String(err)))
      .finally(() => setLoading(false))
  }, [isOpen, machineID])

  if (!isOpen) return null

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(script)
      toast.success('Recovery script copied. Paste into a shell on the machine.')
    } catch {
      toast.error('Clipboard blocked by browser — use Download instead.')
    }
  }

  return (
    <div className="fixed inset-0 bg-black/60 z-50 overflow-y-auto">
      <div className="flex min-h-full items-center justify-center p-4">
        <div className="bg-white rounded-xl shadow-2xl w-full max-w-2xl">
          <div className="flex items-center justify-between p-4 border-b">
            <div className="flex items-center gap-2">
              <AlertTriangle size={18} className="text-amber-500" />
              <h2 className="text-lg font-semibold">Manual recovery — {machineName}</h2>
            </div>
            <button
              onClick={onClose}
              className="text-gray-400 hover:text-gray-600"
              aria-label="Close"
            >
              <X size={20} />
            </button>
          </div>

          <div className="p-4 space-y-4">
            <div className="bg-amber-50 border border-amber-200 rounded-lg p-3 text-sm">
              <p className="font-medium text-amber-900 mb-1">Server-side recovery failed</p>
              <p className="text-amber-800">
                Gopher tried the agent's HTTP back-channel and SSH-over-tunnel — both need the
                rathole tunnel up, which is what's broken.
              </p>
              {reason && (
                <p className="text-xs text-amber-700 mt-2 font-mono break-words">
                  Underlying error: {reason}
                </p>
              )}
            </div>

            <div>
              <p className="text-sm font-medium text-gray-700 mb-1">
                If <span className="font-mono text-xs">{machineName}</span> is reachable, run this:
              </p>
              <p className="text-xs text-gray-500 mb-2">
                If the machine itself is off / unreachable, no script will help — fix that first.
              </p>
              <ol className="text-sm text-gray-600 space-y-1 list-decimal list-inside">
                <li>
                  <span className="font-medium">Preferred — download &amp; bash:</span> click{' '}
                  <span className="font-mono text-xs">Download</span> below, copy the file to{' '}
                  <span className="font-mono text-xs">{machineName}</span>, then run{' '}
                  <span className="font-mono text-xs bg-gray-100 px-1 rounded">bash gopher-recover-{machineID}.sh</span>.
                  Works as either root or sudoer.
                </li>
                <li>
                  <span className="font-medium">Or paste directly:</span> click{' '}
                  <span className="font-mono text-xs">Copy</span> and paste into a shell on the
                  machine. The script uses <span className="font-mono text-xs">$SUDO</span> prefixes
                  so it won't terminate your session if you're not root.
                </li>
                <li>
                  <span className="font-medium">Free disk space first</span> if the underlying error
                  mentions <span className="font-mono text-xs">no space left on device</span> — the
                  script needs ~10 KB free in <span className="font-mono text-xs">/etc/rathole/</span>{' '}
                  to rewrite the config.
                </li>
              </ol>
            </div>

            <div>
              <div className="flex items-center justify-between mb-2">
                <p className="text-sm font-medium text-gray-700">Recovery script</p>
                <div className="flex gap-2">
                  <button
                    onClick={copy}
                    disabled={!script || loading}
                    className="flex items-center gap-1 px-2 py-1 text-xs border border-gray-300 rounded hover:bg-gray-50 disabled:opacity-50"
                  >
                    <Copy size={11} /> Copy
                  </button>
                  <a
                    href={machinesApi.ratholeConfigUrl(machineID, 'script')}
                    className="flex items-center gap-1 px-2 py-1 text-xs border border-gray-300 rounded hover:bg-gray-50"
                  >
                    <Download size={11} /> Download
                  </a>
                </div>
              </div>
              <pre className="bg-gray-900 text-gray-100 text-xs rounded-lg p-3 overflow-x-auto max-h-64 overflow-y-auto font-mono">
                {loading ? 'Loading script…' : (fetchErr ? `Failed to fetch: ${fetchErr}` : script)}
              </pre>
            </div>

            <p className="text-xs text-gray-500">
              Once the script runs, rathole-client picks up the new config via inotify and the
              tunnel reconnects within a few seconds. Watch the dashboard — the machine flips to{' '}
              <span className="font-medium">connected</span> on the next health-poll cycle.
            </p>
          </div>

          <div className="flex justify-end p-4 border-t bg-gray-50 rounded-b-xl">
            <button
              onClick={onClose}
              className="px-3 py-1.5 text-sm border border-gray-300 rounded hover:bg-gray-100"
            >
              Close
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
