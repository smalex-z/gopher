import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { KeyRound, Lock } from 'lucide-react'
import { localApi } from '../api/local'
import { toast } from '../lib/toast'

// DeletePrivateKeyButton clears the stored private half of a key, keeping the
// public key. Gated by the same re-auth challenge as the private-key download —
// it's irreversible and destroys a credential, so a stolen session cookie alone
// must not be able to trigger it.
//
// After deletion the key is public-only: still usable for authorized_keys and
// the jumpbox, but the server can no longer SSH into origins with it (server
// control runs over the agent). Download the key first if you want to keep it.

interface DeletePrivateKeyButtonProps {
  id: string
  name: string
  className?: string
  children?: React.ReactNode
}

export default function DeletePrivateKeyButton({ id, name, className, children }: DeletePrivateKeyButtonProps) {
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)

  const challenge = useQuery({
    queryKey: ['ssh-key-challenge-info'],
    queryFn: localApi.sshKeyChallengeInfo,
    enabled: open,
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
      await localApi.deletePrivateKey(id, payload)
      qc.invalidateQueries({ queryKey: ['ssh-keys'] })
      setOpen(false)
      setCode('')
      toast.success('Private key deleted; public key kept')
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
        title={`Delete "${name}" private key, keep public (re-auth required)`}
      >
        {children ?? <KeyRound size={14} />}
      </button>
      {open && (
        <div className="fixed inset-0 !mt-0 z-50 flex items-center justify-center bg-black/40 p-4">
          <div className="bg-white rounded-2xl shadow-xl w-full max-w-md p-6 space-y-4">
            <div className="flex items-center gap-2 text-gray-700">
              <Lock size={16} />
              <h3 className="font-semibold text-base">Delete private key?</h3>
            </div>
            <p className="text-xs text-gray-500">
              This <strong>permanently removes the private key</strong> from the server, keeping only the public key.
              The key stays usable for the jumpbox and <code>authorized_keys</code>, but the server can no longer SSH
              into origins with it. Download it first if you want a copy — this can't be undone. Recorded in the audit log.
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
                  className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-red-500"
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
                    className="px-4 py-1.5 bg-red-600 text-white rounded-lg text-sm font-semibold hover:bg-red-700 disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
                  >
                    {busy ? 'Deleting…' : 'Delete private key'}
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
