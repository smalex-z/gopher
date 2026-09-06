import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { KeyRound, Upload } from 'lucide-react'
import { localApi } from '../api/local'
import { toast } from '../lib/toast'

// AddPrivateKeyButton restores/attaches the private half of a public-only key.
// The server verifies the private key matches the stored public key, so only
// the legitimate key is accepted — no re-auth challenge needed.

interface AddPrivateKeyButtonProps {
  id: string
  name: string
  className?: string
  children?: React.ReactNode
}

export default function AddPrivateKeyButton({ id, name, className, children }: AddPrivateKeyButtonProps) {
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const [priv, setPriv] = useState('')
  const [busy, setBusy] = useState(false)

  const readFile = (file: File) => {
    const reader = new FileReader()
    reader.onload = e => setPriv((e.target?.result as string) ?? '')
    reader.readAsText(file)
  }

  const submit = async () => {
    if (!priv.trim() || busy) return
    setBusy(true)
    try {
      await localApi.addPrivateKey(id, priv)
      qc.invalidateQueries({ queryKey: ['ssh-keys'] })
      setOpen(false)
      setPriv('')
      toast.success('Private key stored')
    } catch (err) {
      toast.error((err as Error).message || 'Could not store private key')
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <button onClick={() => { setPriv(''); setOpen(true) }} className={className} title={`Add the private key for "${name}"`}>
        {children ?? <KeyRound size={14} />}
      </button>
      {open && (
        <div className="fixed inset-0 !mt-0 z-50 flex items-center justify-center bg-black/40 p-4">
          <div className="bg-white rounded-2xl shadow-xl w-full max-w-lg p-6 space-y-4">
            <div className="flex items-center gap-2 text-gray-700">
              <KeyRound size={16} />
              <h3 className="font-semibold text-base">Add private key for “{name}”</h3>
            </div>
            <p className="text-xs text-gray-500">
              Paste the matching private key. It's verified against the stored public key — only the correct key is
              accepted — and lets the server SSH with this key again.
            </p>
            <div className="flex gap-2">
              <textarea
                value={priv}
                onChange={e => setPriv(e.target.value)}
                rows={5}
                autoFocus
                placeholder={'-----BEGIN OPENSSH PRIVATE KEY-----\n...'}
                className="flex-1 border border-gray-300 rounded-lg px-3 py-2 text-xs font-mono resize-none focus:outline-none focus:ring-2 focus:ring-blue-500"
              />
              <label className="cursor-pointer flex flex-col items-center justify-center px-3 border border-gray-300 rounded-lg hover:bg-gray-50 text-gray-500 text-xs gap-1">
                <Upload size={14} />
                Browse
                <input type="file" className="hidden" onChange={e => { if (e.target.files?.[0]) readFile(e.target.files[0]) }} />
              </label>
            </div>
            <div className="flex justify-end gap-2 pt-1">
              <button onClick={() => { setOpen(false); setPriv('') }} className="px-3 py-1.5 text-sm font-medium text-gray-600 hover:text-gray-800">
                Cancel
              </button>
              <button
                onClick={submit}
                disabled={!priv.trim() || busy}
                className="px-4 py-1.5 bg-blue-600 text-white rounded-lg text-sm font-semibold hover:bg-blue-700 disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
              >
                {busy ? 'Verifying…' : 'Add private key'}
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}
