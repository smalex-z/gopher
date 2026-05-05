import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Download, Lock } from 'lucide-react'
import { localApi } from '../api/local'
import { toast } from '../lib/toast'

const toKeyFilename = (name: string) =>
  name.toLowerCase().replace(/\s+/g, '_').replace(/[^a-z0-9_-]/g, '')

// DownloadKeyButton wraps the SSH private-key download with a re-auth
// challenge modal. Server-side, /api/local/ssh-keys/{id}/download requires
// either a current TOTP code (if 2FA is enrolled) or the login password.
//
// The protection isn't paranoia: the per-machine SSH keys stored in the
// dashboard's DB let the VPS impersonate any operator on every connected
// client. A stolen session cookie alone shouldn't be enough to walk away
// with them.
//
// Used everywhere a private key download is offered: SSH Keys page, the
// "key generated" success modal, and the initial setup flow.

interface DownloadKeyButtonProps {
  id: string
  name: string
  className?: string
  children?: React.ReactNode
}

export default function DownloadKeyButton({ id, name, className, children }: DownloadKeyButtonProps) {
  const [open, setOpen] = useState(false)
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)

  const challenge = useQuery({
    queryKey: ['ssh-key-challenge-info'],
    queryFn: localApi.sshKeyChallengeInfo,
    enabled: open, // fetch only once the modal is opened
    staleTime: 5 * 60_000,
  })

  const requires = challenge.data?.requires
  const fieldType = requires === 'totp' ? 'text' : 'password'
  const fieldLabel = requires === 'totp' ? '6-digit code from authenticator (or backup code)' : 'Login password'

  const submit = async () => {
    if (!requires || busy) return
    setBusy(true)
    try {
      const payload = requires === 'totp' ? { totp_code: code } : { password: code }
      const blob = await localApi.downloadSSHKey(id, payload)
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url; a.download = toKeyFilename(name) || 'id_rsa'; a.click()
      URL.revokeObjectURL(url)
      setOpen(false)
      setCode('')
      toast.success('Private key downloaded')
    } catch (err) {
      const e = err as Error
      toast.error(e.message || 'Verification failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <button
        onClick={() => { setCode(''); setOpen(true) }}
        className={className}
        title="Download private key (re-auth required)"
      >
        {children ?? <Download size={14} />}
      </button>
      {open && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
          <div className="bg-white rounded-2xl shadow-xl w-full max-w-md p-6 space-y-4">
            <div className="flex items-center gap-2 text-gray-700">
              <Lock size={16} />
              <h3 className="font-semibold text-base">Confirm to download private key</h3>
            </div>
            <p className="text-xs text-gray-500">
              Re-confirming protects the key from session-only attackers. The download is recorded in the audit log.
            </p>
            {challenge.isLoading && <p className="text-sm text-gray-400">Loading…</p>}
            {challenge.isError && <p className="text-sm text-red-600">Could not load challenge — try again.</p>}
            {requires && (
              <>
                <label className="block text-xs font-medium text-gray-600">{fieldLabel}</label>
                <input
                  type={fieldType}
                  value={code}
                  onChange={e => setCode(e.target.value)}
                  onKeyDown={e => { if (e.key === 'Enter') submit() }}
                  autoFocus
                  className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
                  placeholder={requires === 'totp' ? '123456' : '••••••••'}
                />
                <div className="flex justify-end gap-2 pt-2">
                  <button
                    onClick={() => { setOpen(false); setCode('') }}
                    className="px-3 py-1.5 text-sm font-medium text-gray-600 hover:text-gray-800"
                  >
                    Cancel
                  </button>
                  <button
                    onClick={submit}
                    disabled={!code || busy}
                    className="px-4 py-1.5 bg-gray-800 text-white rounded-lg text-sm font-semibold hover:bg-gray-900 disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
                  >
                    {busy ? 'Verifying…' : 'Download'}
                  </button>
                </div>
              </>
            )}
          </div>
        </div>
      )}
    </>
  )
}
