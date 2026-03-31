import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Key, Download, Trash2, Star, Upload, RefreshCw, Copy, Check, Plus } from 'lucide-react'
import { localApi } from '../api/local'
import { toast } from '../lib/toast'
import type { SSHKey } from '../types'

// ─── Add Key Modal ────────────────────────────────────────────────────────────
type AddMode = 'generate' | 'upload'

interface AddKeyModalProps {
  onClose: () => void
}

function AddKeyModal({ onClose }: AddKeyModalProps) {
  const queryClient = useQueryClient()
  const [mode, setMode] = useState<AddMode>('generate')
  const [name, setName] = useState('')
  const [setDefault, setSetDefault] = useState(false)
  const [privateKey, setPrivateKey] = useState('')
  const [publicKey, setPublicKey] = useState('')
  const [generatedKey, setGeneratedKey] = useState<SSHKey | null>(null)
  const [copied, setCopied] = useState(false)

  const generateMutation = useMutation({
    mutationFn: () => localApi.generateSSHKey(name || 'Unnamed key', setDefault),
    onSuccess: (res) => {
      if (res.data) {
        setGeneratedKey(res.data)
        queryClient.invalidateQueries({ queryKey: ['ssh-keys'] })
        toast.success('SSH key generated')
      }
    },
    onError: (err: Error) => toast.error(err.message || 'Failed to generate key'),
  })

  const uploadMutation = useMutation({
    mutationFn: () => localApi.uploadSSHKey(name || 'Uploaded key', privateKey, publicKey, setDefault),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['ssh-keys'] })
      toast.success('SSH key saved')
      onClose()
    },
    onError: (err: Error) => toast.error(err.message || 'Invalid key pair'),
  })

  const downloadKey = async (id: string) => {
    try {
      const blob = await localApi.downloadSSHKey(id)
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url; a.download = 'gopher_id_rsa'; a.click()
      URL.revokeObjectURL(url)
    } catch {
      toast.error('Download failed')
    }
  }

  const copyPublicKey = (key: string) => {
    navigator.clipboard.writeText(key)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
    toast.success('Copied!')
  }

  const readFile = (file: File, setter: (v: string) => void) => {
    const reader = new FileReader()
    reader.onload = e => setter((e.target?.result as string) ?? '')
    reader.readAsText(file)
  }

  // After generating — show the public key + download
  if (generatedKey) {
    return (
      <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
        <div className="bg-white rounded-2xl shadow-xl w-full max-w-lg p-6 space-y-5">
          <div className="flex items-center gap-2 text-green-600">
            <Key size={18} />
            <h2 className="font-semibold text-base">Key Generated</h2>
          </div>
          <div className="bg-amber-50 border border-amber-200 rounded-lg p-4 text-sm text-amber-800">
            <strong>Download the private key now</strong> — you cannot retrieve it again after closing this dialog.
          </div>
          <div>
            <div className="text-xs font-medium text-gray-500 mb-1">Public key</div>
            <div className="bg-gray-50 rounded-lg p-3 flex items-start gap-2">
              <code className="text-xs text-gray-700 break-all flex-1">{generatedKey.public_key}</code>
              <button
                onClick={() => copyPublicKey(generatedKey.public_key)}
                className="shrink-0 text-gray-400 hover:text-gray-600 mt-0.5"
              >
                {copied ? <Check size={14} className="text-green-500" /> : <Copy size={14} />}
              </button>
            </div>
          </div>
          <button
            onClick={() => downloadKey(generatedKey.id)}
            className="w-full flex items-center justify-center gap-2 bg-gray-800 text-white py-2.5 rounded-lg text-sm font-semibold hover:bg-gray-900 transition-colors"
          >
            <Download size={15} /> Download private key (gopher_id_rsa)
          </button>
          <button
            onClick={onClose}
            className="w-full bg-green-600 text-white py-2.5 rounded-lg text-sm font-semibold hover:bg-green-700 transition-colors"
          >
            Done
          </button>
        </div>
      </div>
    )
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
      <div className="bg-white rounded-2xl shadow-xl w-full max-w-lg p-6 space-y-5">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Key size={18} className="text-blue-600" />
            <h2 className="font-semibold text-base text-gray-900">Add SSH Key</h2>
          </div>
          <button onClick={onClose} className="text-gray-400 hover:text-gray-600 text-lg leading-none">&times;</button>
        </div>

        {/* Mode tabs */}
        <div className="flex gap-1 bg-gray-100 rounded-lg p-1">
          <button
            onClick={() => setMode('generate')}
            className={`flex-1 flex items-center justify-center gap-1.5 py-2 rounded-md text-sm font-medium transition-colors ${mode === 'generate' ? 'bg-white shadow-sm text-gray-900' : 'text-gray-500 hover:text-gray-700'}`}
          >
            <RefreshCw size={14} /> Generate
          </button>
          <button
            onClick={() => setMode('upload')}
            className={`flex-1 flex items-center justify-center gap-1.5 py-2 rounded-md text-sm font-medium transition-colors ${mode === 'upload' ? 'bg-white shadow-sm text-gray-900' : 'text-gray-500 hover:text-gray-700'}`}
          >
            <Upload size={14} /> Upload
          </button>
        </div>

        {/* Name field */}
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Name <span className="text-red-500">*</span></label>
          <input
            type="text"
            value={name}
            onChange={e => setName(e.target.value)}
            placeholder={mode === 'generate' ? 'Unnamed key' : 'Uploaded key'}
            className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
          />
        </div>

        {mode === 'generate' ? (
          <>
            <label className="flex items-center gap-2 text-sm text-gray-700 cursor-pointer">
              <input type="checkbox" checked={setDefault} onChange={e => setSetDefault(e.target.checked)} />
              Set as default key
            </label>
            <div className="flex gap-3">
              <button onClick={onClose} className="px-4 py-2.5 border border-gray-300 text-gray-600 rounded-lg text-sm hover:bg-gray-50 transition-colors">
                Cancel
              </button>
              <button
                onClick={() => generateMutation.mutate()}
                disabled={generateMutation.isPending}
                className="flex-1 flex items-center justify-center gap-2 bg-blue-600 text-white py-2.5 rounded-lg text-sm font-semibold hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
              >
                <RefreshCw size={14} className={generateMutation.isPending ? 'animate-spin' : ''} />
                {generateMutation.isPending ? 'Generating…' : 'Generate key'}
              </button>
            </div>
          </>
        ) : (
          <>
            <div>
              <label className="block text-sm font-medium text-gray-700 mb-1">
                Private key <span className="text-gray-400 font-normal">(PEM or OpenSSH format)</span>
              </label>
              <div className="flex gap-2">
                <textarea
                  value={privateKey}
                  onChange={e => setPrivateKey(e.target.value)}
                  rows={4}
                  placeholder={'-----BEGIN RSA PRIVATE KEY-----\n...'}
                  className="flex-1 border border-gray-300 rounded-lg px-3 py-2 text-xs font-mono resize-none focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
                <label className="cursor-pointer flex flex-col items-center justify-center px-3 border border-gray-300 rounded-lg hover:bg-gray-50 text-gray-500 text-xs gap-1">
                  <Upload size={14} />
                  Browse
                  <input type="file" className="hidden" onChange={e => { if (e.target.files?.[0]) readFile(e.target.files[0], setPrivateKey) }} />
                </label>
              </div>
            </div>
            <div>
              <label className="block text-sm font-medium text-gray-700 mb-1">
                Public key <span className="text-gray-400 font-normal">(authorized_keys format)</span>
              </label>
              <div className="flex gap-2">
                <textarea
                  value={publicKey}
                  onChange={e => setPublicKey(e.target.value)}
                  rows={2}
                  placeholder="ssh-rsa AAAA..."
                  className="flex-1 border border-gray-300 rounded-lg px-3 py-2 text-xs font-mono resize-none focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
                <label className="cursor-pointer flex flex-col items-center justify-center px-3 border border-gray-300 rounded-lg hover:bg-gray-50 text-gray-500 text-xs gap-1">
                  <Upload size={14} />
                  Browse
                  <input type="file" className="hidden" onChange={e => { if (e.target.files?.[0]) readFile(e.target.files[0], setPublicKey) }} />
                </label>
              </div>
            </div>
            <label className="flex items-center gap-2 text-sm text-gray-700 cursor-pointer">
              <input type="checkbox" checked={setDefault} onChange={e => setSetDefault(e.target.checked)} />
              Set as default key
            </label>
            <div className="flex gap-3">
              <button onClick={onClose} className="px-4 py-2.5 border border-gray-300 text-gray-600 rounded-lg text-sm hover:bg-gray-50 transition-colors">
                Cancel
              </button>
              <button
                onClick={() => uploadMutation.mutate()}
                disabled={uploadMutation.isPending || !privateKey || !publicKey}
                className="flex-1 bg-blue-600 text-white py-2.5 rounded-lg text-sm font-semibold hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
              >
                {uploadMutation.isPending ? 'Saving…' : 'Save key pair'}
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  )
}

// ─── SSH Keys Page ────────────────────────────────────────────────────────────
export default function SSHKeysPage() {
  const queryClient = useQueryClient()
  const [showAddModal, setShowAddModal] = useState(false)

  const { data: keysRes, isLoading } = useQuery({
    queryKey: ['ssh-keys'],
    queryFn: () => localApi.listSSHKeys(),
  })

  const keys: SSHKey[] = keysRes?.data ?? []

  const deleteMutation = useMutation({
    mutationFn: (id: string) => localApi.deleteSSHKey(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['ssh-keys'] })
      toast.success('Key deleted')
    },
    onError: (err: Error) => toast.error(err.message || 'Failed to delete key'),
  })

  const setDefaultMutation = useMutation({
    mutationFn: (id: string) => localApi.setDefaultSSHKey(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['ssh-keys'] })
      toast.success('Default key updated')
    },
    onError: (err: Error) => toast.error(err.message || 'Failed to update default'),
  })

  const downloadKey = async (id: string) => {
    try {
      const blob = await localApi.downloadSSHKey(id)
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url; a.download = 'gopher_id_rsa'; a.click()
      URL.revokeObjectURL(url)
    } catch {
      toast.error('Download failed')
    }
  }

  const truncateKey = (key: string) => {
    const parts = key.trim().split(' ')
    if (parts.length >= 2) {
      return parts[0] + ' ' + parts[1].slice(0, 30) + '…'
    }
    return key.slice(0, 40) + '…'
  }

  const formatDate = (dateStr: string) => {
    try {
      return new Date(dateStr).toLocaleDateString('en-US', { year: 'numeric', month: 'short', day: 'numeric' })
    } catch {
      return dateStr
    }
  }

  return (
    <div className="space-y-6">
      {showAddModal && <AddKeyModal onClose={() => setShowAddModal(false)} />}

      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-gray-900">SSH Keys</h1>
          <p className="text-sm text-gray-500 mt-1">Server keys used to connect to bootstrapped machines</p>
        </div>
        <button
          onClick={() => setShowAddModal(true)}
          className="flex items-center gap-1.5 px-4 py-2 bg-blue-600 text-white rounded-lg text-sm font-semibold hover:bg-blue-700 transition-colors"
        >
          <Plus size={15} /> Add Key
        </button>
      </div>

      {isLoading ? (
        <div className="bg-white rounded-xl border border-gray-200 p-12 text-center text-gray-400 text-sm">
          Loading…
        </div>
      ) : keys.length === 0 ? (
        <div className="bg-white rounded-xl border border-gray-200 p-12 text-center space-y-3">
          <Key size={32} className="mx-auto text-gray-300" />
          <p className="text-gray-500 text-sm">No SSH keys stored yet.</p>
          <button
            onClick={() => setShowAddModal(true)}
            className="text-blue-600 text-sm hover:underline"
          >
            Add your first key
          </button>
        </div>
      ) : (
        <div className="bg-white rounded-xl border border-gray-200 overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-gray-50 border-b border-gray-200">
              <tr>
                <th className="text-left px-4 py-3 font-medium text-gray-600">Name</th>
                <th className="text-left px-4 py-3 font-medium text-gray-600 hidden md:table-cell">Public key</th>
                <th className="text-left px-4 py-3 font-medium text-gray-600 hidden sm:table-cell">Created</th>
                <th className="text-right px-4 py-3 font-medium text-gray-600">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {keys.map(key => (
                <tr key={key.id} className="hover:bg-gray-50 transition-colors">
                  <td className="px-4 py-3">
                    <div className="flex items-center gap-2">
                      <span className="font-medium text-gray-900">{key.name}</span>
                      {key.is_default && (
                        <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-xs font-medium bg-blue-100 text-blue-700">
                          <Star size={10} fill="currentColor" /> Default
                        </span>
                      )}
                    </div>
                  </td>
                  <td className="px-4 py-3 hidden md:table-cell">
                    <code className="text-xs text-gray-500 font-mono">{truncateKey(key.public_key)}</code>
                  </td>
                  <td className="px-4 py-3 text-gray-500 hidden sm:table-cell">{formatDate(key.created_at)}</td>
                  <td className="px-4 py-3">
                    <div className="flex items-center justify-end gap-1">
                      <button
                        onClick={() => downloadKey(key.id)}
                        title="Download private key"
                        className="p-1.5 text-gray-400 hover:text-gray-700 hover:bg-gray-100 rounded-lg transition-colors"
                      >
                        <Download size={14} />
                      </button>
                      <button
                        onClick={() => setDefaultMutation.mutate(key.id)}
                        disabled={key.is_default || setDefaultMutation.isPending}
                        title={key.is_default ? 'Already default' : 'Set as default'}
                        className="p-1.5 text-gray-400 hover:text-blue-600 hover:bg-blue-50 rounded-lg transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
                      >
                        <Star size={14} />
                      </button>
                      <button
                        onClick={() => {
                          if (confirm(`Delete key "${key.name}"? This cannot be undone.`)) {
                            deleteMutation.mutate(key.id)
                          }
                        }}
                        disabled={deleteMutation.isPending}
                        title="Delete key"
                        className="p-1.5 text-gray-400 hover:text-red-600 hover:bg-red-50 rounded-lg transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
                      >
                        <Trash2 size={14} />
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
