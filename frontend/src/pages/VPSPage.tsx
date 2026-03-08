import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Copy, Check } from 'lucide-react'
import { vpsApi } from '../api/vps'
import StatusBadge from '../components/StatusBadge'
import DeployLogModal from '../components/DeployLogModal'
import { toast } from '../lib/toast'
import type { VPSConfig } from '../types'

interface FormState {
  host: string
  port: number
  username: string
  private_key: string
  domain: string
}

interface DeployModal {
  isOpen: boolean
  title: string
  action: () => Promise<void>
}

const defaultForm: FormState = { host: '', port: 22, username: 'root', private_key: '', domain: '' }

export default function VPSPage() {
  const qc = useQueryClient()
  const [form, setForm] = useState<FormState>(defaultForm)
  const [errors, setErrors] = useState<Partial<FormState>>({})
  const [editing, setEditing] = useState(false)
  const [testResult, setTestResult] = useState<{ ok: boolean; message: string } | null>(null)
  const [testLoading, setTestLoading] = useState(false)
  const [deployModal, setDeployModal] = useState<DeployModal>({ isOpen: false, title: '', action: async () => {} })
  const [keyCopied, setKeyCopied] = useState(false)

  const copyKey = (key: string) => {
    navigator.clipboard.writeText(key).then(() => {
      setKeyCopied(true)
      setTimeout(() => setKeyCopied(false), 2000)
    })
  }

  const { data: vpsData, isLoading } = useQuery({
    queryKey: ['vps'],
    queryFn: () => vpsApi.get(),
    retry: false,
  })

  const vps = vpsData?.data

  const saveMutation = useMutation({
    mutationFn: (data: Partial<VPSConfig>) => vpsApi.create(data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['vps'] })
      toast.success('VPS configuration saved!')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const updateMutation = useMutation({
    mutationFn: (data: Partial<VPSConfig>) => vpsApi.update(data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['vps'] })
      setEditing(false)
      toast.success('VPS updated!')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const deleteMutation = useMutation({
    mutationFn: () => vpsApi.delete(),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['vps'] })
      toast.success('VPS configuration removed.')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const validate = (f: FormState): Partial<FormState> => {
    const e: Partial<FormState> = {}
    if (!f.host) e.host = 'Host is required'
    if (!f.domain) e.domain = 'Domain is required'
    if (!f.private_key) e.private_key = 'Private key is required'
    return e
  }

  const handleSave = () => {
    const e = validate(form)
    if (Object.keys(e).length) { setErrors(e); return }
    setErrors({})
    saveMutation.mutate(form)
  }

  const handleUpdate = () => {
    const e = validate(form)
    if (Object.keys(e).length) { setErrors(e); return }
    setErrors({})
    updateMutation.mutate(form)
  }

  const handleTest = async () => {
    const e = validate(form)
    if (Object.keys(e).length) { setErrors(e); return }
    setErrors({})
    setTestLoading(true)
    setTestResult(null)
    try {
      await vpsApi.create(form)
      setTestResult({ ok: true, message: 'Connection successful!' })
    } catch (err) {
      setTestResult({ ok: false, message: err instanceof Error ? err.message : 'Connection failed' })
    } finally {
      setTestLoading(false)
    }
  }

  const handleVpsTest = async () => {
    setTestLoading(true)
    setTestResult(null)
    try {
      await vpsApi.status()
      setTestResult({ ok: true, message: 'VPS is reachable!' })
    } catch (err) {
      setTestResult({ ok: false, message: err instanceof Error ? err.message : 'Connection failed' })
    } finally {
      setTestLoading(false)
    }
  }

  const handleDelete = () => {
    if (window.confirm('Are you sure you want to remove the VPS configuration?')) {
      deleteMutation.mutate()
    }
  }

  const startEdit = () => {
    if (vps) setForm({ host: vps.host, port: vps.port, username: vps.username, private_key: vps.private_key, domain: vps.domain })
    setEditing(true)
    setTestResult(null)
  }

  const F = ({ label, name, type = 'text', placeholder = '', required = false }: { label: string; name: keyof FormState; type?: string; placeholder?: string; required?: boolean }) => (
    <div>
      <label className="block text-sm font-medium text-gray-700 mb-1">{label}{required && <span className="text-red-500 ml-1">*</span>}</label>
      <input
        type={type}
        value={String(form[name])}
        onChange={e => setForm(f => ({ ...f, [name]: type === 'number' ? Number(e.target.value) : e.target.value }))}
        placeholder={placeholder}
        className={`w-full border rounded-lg px-3 py-2 text-sm focus:ring-2 focus:ring-blue-500 focus:outline-none ${errors[name] ? 'border-red-400' : 'border-gray-300'}`}
      />
      {errors[name] && <p className="text-xs text-red-500 mt-1">{errors[name]}</p>}
    </div>
  )

  if (isLoading) return <div className="text-gray-400 text-center py-12">Loading...</div>

  if (!vps) {
    return (
      <div className="max-w-2xl">
        <h1 className="text-2xl font-bold text-gray-900 mb-6">Configure VPS</h1>
        <div className="bg-white rounded-xl shadow-sm border p-6 space-y-4">
          <div className="grid grid-cols-2 gap-4">
            <F label="Host" name="host" placeholder="192.168.1.1 or server.com" required />
            <F label="Port" name="port" type="number" placeholder="22" />
          </div>
          <div className="grid grid-cols-2 gap-4">
            <F label="Username" name="username" placeholder="root" />
            <F label="Domain" name="domain" placeholder="tunnel.example.com" required />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">SSH Private Key (PEM)<span className="text-red-500 ml-1">*</span></label>
            <textarea
              value={form.private_key}
              onChange={e => setForm(f => ({ ...f, private_key: e.target.value }))}
              rows={6}
              placeholder="-----BEGIN RSA PRIVATE KEY-----&#10;..."
              className={`w-full border rounded-lg px-3 py-2 text-sm font-mono focus:ring-2 focus:ring-blue-500 focus:outline-none ${errors.private_key ? 'border-red-400' : 'border-gray-300'}`}
            />
            {errors.private_key && <p className="text-xs text-red-500 mt-1">{errors.private_key}</p>}
            <p className="text-xs text-gray-400 mt-1">Key is stored locally and sent only to your server</p>
          </div>

          {testResult && (
            <div className={`text-sm px-3 py-2 rounded-lg ${testResult.ok ? 'bg-green-50 text-green-700' : 'bg-red-50 text-red-700'}`}>
              {testResult.ok ? '✅' : '❌'} {testResult.message}
            </div>
          )}

          <div className="flex gap-3 pt-2">
            <button onClick={handleTest} disabled={testLoading} className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg hover:bg-gray-50 text-sm disabled:opacity-50">
              {testLoading ? 'Testing...' : 'Test Connection'}
            </button>
            <button onClick={handleSave} disabled={saveMutation.isPending} className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 text-sm disabled:opacity-50">
              {saveMutation.isPending ? 'Saving...' : 'Save & Connect'}
            </button>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="max-w-2xl space-y-6">
      <h1 className="text-2xl font-bold text-gray-900">VPS Configuration</h1>

      <div className="bg-white rounded-xl shadow-sm border p-6">
        <div className="flex items-center justify-between mb-4">
          <div>
            <div className="flex items-center gap-3">
              <StatusBadge status="connected" />
              <span className="font-semibold text-gray-900">{vps.host}</span>
            </div>
            <div className="text-sm text-gray-500 mt-1">{vps.domain}</div>
          </div>
        </div>

        <div className="grid grid-cols-2 gap-4 mb-6 text-sm">
          <div><span className="text-gray-500">Host:</span> <span className="font-medium">{vps.host}</span></div>
          <div><span className="text-gray-500">Port:</span> <span className="font-medium">{vps.port}</span></div>
          <div><span className="text-gray-500">Username:</span> <span className="font-medium">{vps.username}</span></div>
          <div><span className="text-gray-500">Domain:</span> <span className="font-medium">{vps.domain}</span></div>
        </div>

        {vps.ssh_public_key && (
          <div className="mb-4">
            <div className="flex items-center justify-between mb-1">
              <label className="text-sm font-medium text-gray-700">VPS SSH Public Key</label>
              <button
                onClick={() => copyKey(vps.ssh_public_key)}
                className="flex items-center gap-1 text-xs text-gray-500 hover:text-gray-700"
              >
                {keyCopied ? <Check className="w-3.5 h-3.5 text-green-500" /> : <Copy className="w-3.5 h-3.5" />}
                {keyCopied ? 'Copied!' : 'Copy'}
              </button>
            </div>
            <textarea
              readOnly
              value={vps.ssh_public_key}
              rows={2}
              className="w-full border border-gray-200 rounded-lg px-3 py-2 text-xs font-mono bg-gray-50 text-gray-600 resize-none focus:outline-none"
            />
            <p className="text-xs text-gray-400 mt-1">This key is automatically installed on machines during bootstrap.</p>
          </div>
        )}

        {testResult && (
          <div className={`text-sm px-3 py-2 rounded-lg mb-4 ${testResult.ok ? 'bg-green-50 text-green-700' : 'bg-red-50 text-red-700'}`}>
            {testResult.ok ? '✅' : '❌'} {testResult.message}
          </div>
        )}

        <div className="flex flex-wrap gap-2">
          <button onClick={handleVpsTest} disabled={testLoading} className="px-3 py-1.5 border border-gray-300 text-gray-700 rounded-lg hover:bg-gray-50 text-sm disabled:opacity-50">
            {testLoading ? 'Testing...' : 'Test Connection'}
          </button>
          <button onClick={() => setDeployModal({ isOpen: true, title: 'Bootstrap VPS', action: () => vpsApi.bootstrap() })} className="px-3 py-1.5 bg-orange-500 text-white rounded-lg hover:bg-orange-600 text-sm">
            Bootstrap VPS
          </button>
          <button onClick={() => setDeployModal({ isOpen: true, title: 'Redeploy Config', action: () => vpsApi.deploy() })} className="px-3 py-1.5 bg-blue-600 text-white rounded-lg hover:bg-blue-700 text-sm">
            Redeploy Config
          </button>
          <button onClick={startEdit} className="px-3 py-1.5 border border-gray-300 text-gray-700 rounded-lg hover:bg-gray-50 text-sm">Edit</button>
          <button onClick={handleDelete} className="px-3 py-1.5 bg-red-50 text-red-600 border border-red-200 rounded-lg hover:bg-red-100 text-sm">Remove</button>
        </div>
      </div>

      {editing && (
        <div className="bg-white rounded-xl shadow-sm border p-6 space-y-4">
          <h2 className="font-semibold text-gray-900">Edit VPS Configuration</h2>
          <div className="grid grid-cols-2 gap-4">
            <F label="Host" name="host" placeholder="192.168.1.1" required />
            <F label="Port" name="port" type="number" />
          </div>
          <div className="grid grid-cols-2 gap-4">
            <F label="Username" name="username" />
            <F label="Domain" name="domain" required />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">SSH Private Key (PEM)</label>
            <textarea
              value={form.private_key}
              onChange={e => setForm(f => ({ ...f, private_key: e.target.value }))}
              rows={6}
              className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm font-mono focus:ring-2 focus:ring-blue-500 focus:outline-none"
            />
          </div>
          <div className="flex gap-2">
            <button onClick={handleUpdate} disabled={updateMutation.isPending} className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 text-sm disabled:opacity-50">
              {updateMutation.isPending ? 'Saving...' : 'Save'}
            </button>
            <button onClick={() => { setEditing(false); setErrors({}) }} className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg hover:bg-gray-50 text-sm">Cancel</button>
          </div>
        </div>
      )}

      <DeployLogModal
        isOpen={deployModal.isOpen}
        onClose={() => setDeployModal(d => ({ ...d, isOpen: false }))}
        title={deployModal.title}
        onStart={deployModal.action}
      />
    </div>
  )
}
