import { useEffect, useRef, useState } from 'react'
import { Lock, Eye, EyeOff, CheckCircle2, XCircle, Loader2, SkipForward, Key, RefreshCw, Upload, Download, ClipboardCopy } from 'lucide-react'
import client from '../api/client'
import { useAuth } from '../lib/auth'
import { localApi, type LocalServiceStatus } from '../api/local'
import { toast } from '../lib/toast'
import DeployLogModal from '../components/DeployLogModal'
import type { SSHKey } from '../types'

// ─── Step 1: Password ────────────────────────────────────────────────────────

function PasswordStep({ onDone }: { onDone: () => void }) {
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [showPw, setShowPw] = useState(false)
  const [loading, setLoading] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (password.length < 8) { toast.error('Password must be at least 8 characters'); return }
    if (password !== confirm) { toast.error('Passwords do not match'); return }
    setLoading(true)
    try {
      await client.post('/auth/setup', { password })
      onDone()
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Setup failed'
      toast.error(msg)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-8">
      <div className="flex items-center gap-2 mb-6 text-blue-600">
        <Lock size={18} />
        <span className="font-semibold text-sm uppercase tracking-wide">Step 1 of 3 — Admin password</span>
      </div>
      <form onSubmit={handleSubmit} className="space-y-5">
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Password</label>
          <div className="relative">
            <input
              type={showPw ? 'text' : 'password'}
              value={password}
              onChange={e => setPassword(e.target.value)}
              placeholder="Minimum 8 characters"
              className="w-full border border-gray-300 rounded-lg px-3 py-2 pr-10 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
              required autoFocus
            />
            <button type="button" tabIndex={-1}
              className="absolute right-2.5 top-2.5 text-gray-400 hover:text-gray-600"
              onClick={() => setShowPw(p => !p)}>
              {showPw ? <EyeOff size={16} /> : <Eye size={16} />}
            </button>
          </div>
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Confirm password</label>
          <input
            type={showPw ? 'text' : 'password'}
            value={confirm}
            onChange={e => setConfirm(e.target.value)}
            placeholder="Re-enter password"
            className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
            required
          />
        </div>
        {password && confirm && password !== confirm && (
          <p className="text-red-500 text-xs">Passwords do not match</p>
        )}
        <button type="submit" disabled={loading || !password || !confirm}
          className="w-full bg-blue-600 text-white py-2.5 rounded-lg text-sm font-semibold hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors">
          {loading ? 'Setting up…' : 'Continue →'}
        </button>
      </form>
    </div>
  )
}

// ─── Step 2: Local Services ───────────────────────────────────────────────────

function ServicePill({ state, label }: { state: string; label: string }) {
  const map: Record<string, string> = {
    active: 'bg-green-100 text-green-700',
    activating: 'bg-yellow-100 text-yellow-700',
    failed: 'bg-red-100 text-red-700',
    inactive: 'bg-orange-100 text-orange-700',
    'not-found': 'bg-gray-100 text-gray-500',
  }
  const cls = map[state] ?? 'bg-gray-100 text-gray-500'
  const icon = state === 'active'
    ? <CheckCircle2 size={14} />
    : state === 'not-found' || !state
    ? <XCircle size={14} />
    : <Loader2 size={14} className="animate-spin" />
  return (
    <div className={`flex items-center gap-1.5 px-3 py-1.5 rounded-full text-xs font-medium ${cls}`}>
      {icon} {label}: {state || 'unknown'}
    </div>
  )
}

function ServicesStep({ onDone }: { onDone: () => void }) {
  const [domain, setDomain] = useState('')
  const [skipCaddy, setSkipCaddy] = useState(false)
  const [status, setStatus] = useState<LocalServiceStatus | null>(null)
  const [showLogs, setShowLogs] = useState(false)
  const [skipping, setSkipping] = useState(false)
  const [dnsStatus, setDnsStatus] = useState<'idle' | 'checking' | 'ok' | 'fail'>('idle')
  const [dnsMessage, setDnsMessage] = useState('')
  const [installComplete, setInstallComplete] = useState(false)
  const dnsTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const load = () => localApi.status().then(setStatus).catch(() => {})

  // On first mount, load status and pre-populate domain if already saved
  useEffect(() => {
    localApi.status().then(s => {
      setStatus(s)
      if (s.domain) {
        setDomain(s.domain)
        setSkipCaddy(false)
      } else if (s.local_setup_done) {
        setSkipCaddy(true)
      }
    }).catch(() => {})
  }, [])

  useEffect(() => {
    const t = setInterval(load, 5000)
    return () => clearInterval(t)
  }, [])

  // Debounced DNS check whenever domain changes
  useEffect(() => {
    if (dnsTimerRef.current) clearTimeout(dnsTimerRef.current)
    if (skipCaddy) {
      setDnsStatus('idle')
      setDnsMessage('')
      return
    }
    const trimmed = domain.trim()
    if (!trimmed || !trimmed.includes('.')) {
      setDnsStatus('idle')
      setDnsMessage('')
      return
    }
    setDnsStatus('checking')
    dnsTimerRef.current = setTimeout(async () => {
      try {
        const result = await localApi.checkDNS(trimmed)
        if (result.ok) {
          setDnsStatus('ok')
          setDnsMessage(result.resolved_to ? `Resolves to ${result.resolved_to}` : 'DNS resolves ✓')
        } else {
          setDnsStatus('fail')
          setDnsMessage(result.message ?? 'DNS not found')
        }
      } catch {
        setDnsStatus('fail')
        setDnsMessage('DNS check failed')
      }
    }, 1200)
    return () => { if (dnsTimerRef.current) clearTimeout(dnsTimerRef.current) }
  }, [domain, skipCaddy])

  // Redirect to router.domain after install completes
  useEffect(() => {
    if (!installComplete) return
    const t = setTimeout(() => {
      if (skipCaddy || !domain.trim()) {
        window.location.href = '/'
        return
      }
      window.location.href = `https://router.${domain.trim()}`
    }, skipCaddy || !domain.trim() ? 1000 : 5000)
    return () => clearTimeout(t)
  }, [installComplete, domain, skipCaddy])

  const allGood = status?.caddy_active === 'active' && status?.rathole_active === 'active'

  const handleSkip = async () => {
    setSkipping(true)
    await localApi.skip(domain || undefined).catch(() => {})
    onDone()
  }

  const canInstall = skipCaddy
    ? (status == null || status.has_install_permission)
    : Boolean(domain && dnsStatus === 'ok' && (status == null || status.has_install_permission))

  return (
    <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-8 space-y-6">
      <div className="flex items-center gap-2 text-blue-600">
        <span className="font-semibold text-sm uppercase tracking-wide">Step 2 of 3 — Local services</span>
      </div>

      <p className="text-sm text-gray-600">
        Gopher can install <strong>Caddy</strong> (HTTPS reverse proxy) and <strong>rathole</strong> (tunnel server)
        as local systemd services. Enable Caddy for domain/subdomain routing, or skip it for raw rathole-only ports.
      </p>

      <label className="flex items-start gap-3 text-sm text-gray-700">
        <input
          type="checkbox"
          checked={skipCaddy}
          onChange={e => setSkipCaddy(e.target.checked)}
          className="mt-0.5"
        />
        <span>
          <strong>Skip Caddy / reverse proxy</strong>
          <span className="block text-xs text-gray-500 mt-0.5">
            Use rathole only. URL/subdomain routing is disabled and tunnels use server ports directly.
          </span>
        </span>
      </label>

      {/* Permission warning */}
      {status && !status.has_install_permission && (
        <div className="bg-amber-50 border border-amber-200 rounded-lg p-4 text-sm text-amber-800">
          <strong>Permission required:</strong> Run <code>./gopher install</code> from the terminal to initialize. 
          It will prompt for your password to set up systemd services and Caddy configuration.
        </div>
      )}

      {/* Service status */}
      {status && (
        <div className="flex flex-wrap gap-2">
          <ServicePill
            state={status.caddy_installed ? status.caddy_active : 'not-found'}
            label="Caddy"
          />
          <ServicePill
            state={status.rathole_installed ? status.rathole_active : 'not-found'}
            label="Rathole server"
          />
        </div>
      )}

      {/* Domain input */}
      {!skipCaddy && (
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">
            Your domain <span className="text-red-500">*</span>{' '}
            <span className="text-gray-400 font-normal">(e.g. <code>example.com</code>)</span>
          </label>
          <input
            type="text"
            value={domain}
            onChange={e => setDomain(e.target.value)}
            placeholder="example.com"
            className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
            required
            autoFocus
          />
          {domain ? (
            <p className="text-xs text-gray-400 mt-1">
              Dashboard will be accessible at <strong>https://router.{domain}</strong>
            </p>
          ) : (
            <p className="text-xs text-orange-500 mt-1">Required — used to configure the Caddy reverse proxy</p>
          )}
        </div>
      )}

      {/* DNS check banner */}
      {!skipCaddy && domain && domain.includes('.') && (
        <div className={`rounded-lg p-4 text-sm border space-y-2 ${
          dnsStatus === 'ok'
            ? 'bg-green-50 border-green-200 text-green-800'
            : dnsStatus === 'fail'
            ? 'bg-red-50 border-red-200 text-red-800'
            : 'bg-blue-50 border-blue-200 text-blue-800'
        }`}>
          <div className="flex items-center gap-2 font-semibold">
            {dnsStatus === 'checking' && <Loader2 size={15} className="animate-spin" />}
            {dnsStatus === 'ok' && <CheckCircle2 size={15} />}
            {dnsStatus === 'fail' && <XCircle size={15} />}
            {dnsStatus === 'idle' && '📋'}
            {dnsStatus === 'checking' ? 'Checking DNS…' :
             dnsStatus === 'ok' ? 'Wildcard DNS is set up ✓' :
             dnsStatus === 'fail' ? 'Wildcard DNS not detected' :
             'DNS setup required'}
          </div>
          {dnsStatus === 'ok' && (
            <p className="text-xs">{dnsMessage}</p>
          )}
          {dnsStatus === 'fail' && (
            <>
              <p>Point a <strong>wildcard A record</strong> at your DNS provider to this server's IP:</p>
              <code className="block bg-white border border-red-200 rounded px-3 py-1.5 text-xs font-mono">
                *.{domain}  →  {'<your server IP>'}
              </code>
              <p className="text-xs mt-1">{dnsMessage}</p>
            </>
          )}
          {(dnsStatus === 'idle') && (
            <>
              <p>Point a <strong>wildcard A record</strong> at your DNS provider to this server's IP:</p>
              <code className="block bg-white border border-blue-200 rounded px-3 py-1.5 text-xs font-mono">
                *.{domain}  →  {'<your server IP>'}
              </code>
              <p className="text-xs text-blue-600 mt-1">
                This lets every subdomain (e.g. <code>router.{domain}</code>) resolve here automatically.
              </p>
            </>
          )}
        </div>
      )}

      {/* Post-install redirect notice */}
      {installComplete && (
        <div className="bg-green-50 border border-green-200 rounded-lg p-4 text-sm text-green-800 space-y-1">
          <div className="font-semibold flex items-center gap-2"><CheckCircle2 size={15} /> Installation complete!</div>
          {skipCaddy || !domain ? (
            <p>Redirecting you to the dashboard…</p>
          ) : (
            <p>
              Redirecting you to{' '}
              <a href={`https://router.${domain}`} className="underline font-medium">
                https://router.{domain}
              </a>{' '}
              in about 5 seconds…
            </p>
          )}
        </div>
      )}

      <div className="flex gap-3">
        <button
          onClick={() => setShowLogs(true)}
          disabled={!canInstall}
          className="flex-1 bg-blue-600 text-white py-2.5 rounded-lg text-sm font-semibold hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
          title={!skipCaddy && dnsStatus !== 'ok' ? 'Waiting for DNS to resolve before installing…' : undefined}
        >
          {skipCaddy
            ? (status?.rathole_active === 'active' ? '↻ Re-configure Rathole' : '⚙ Install Rathole Only')
            : (allGood ? '↻ Re-configure' : '⚙ Install & Configure')}
        </button>
        <button
          onClick={handleSkip}
          disabled={skipping}
          className="flex items-center gap-1.5 px-4 py-2.5 border border-gray-300 text-gray-600 rounded-lg text-sm font-medium hover:bg-gray-50 transition-colors"
        >
          <SkipForward size={15} /> Skip
        </button>
      </div>

      <DeployLogModal
        isOpen={showLogs}
        onClose={() => { setShowLogs(false); load() }}
        onComplete={() => setInstallComplete(true)}
        title="Installing Local Services"
        onStart={() => localApi.install(skipCaddy ? '' : domain, skipCaddy)}
        wsPath="/api/local/logs/ws"
        autoStart
      />
    </div>
  )
}

// ─── Step 3: SSH Key ─────────────────────────────────────────────────────────
type KeyMode = 'choose' | 'generate' | 'upload'

function SSHKeyStep({ onDone }: { onDone: () => void }) {
  const [mode, setMode] = useState<KeyMode>('choose')
  const [loading, setLoading] = useState(false)
  const [generatedKey, setGeneratedKey] = useState<SSHKey | null>(null)
  const [keyName, setKeyName] = useState('Default')
  const [privKeyText, setPrivKeyText] = useState('')
  const [pubKeyText, setPubKeyText] = useState('')

  const handleGenerate = async () => {
    setLoading(true)
    try {
      const res = await localApi.generateSSHKey(keyName || 'Default', true)
      if (res.data) {
        setGeneratedKey(res.data)
        setMode('generate')
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to generate key')
    } finally {
      setLoading(false)
    }
  }

  const handleUploadSave = async () => {
    setLoading(true)
    try {
      await localApi.uploadSSHKey(keyName || 'Uploaded key', privKeyText, pubKeyText, true)
      toast.success('SSH key pair saved')
      onDone()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Invalid key pair — ensure private and public keys match')
    } finally {
      setLoading(false)
    }
  }

  const readFile = (file: File, setter: (v: string) => void) => {
    const reader = new FileReader()
    reader.onload = e => setter((e.target?.result as string) ?? '')
    reader.readAsText(file)
  }

  const downloadPrivateKey = async () => {
    if (!generatedKey) return
    try {
      const blob = await localApi.downloadSSHKey(generatedKey.id)
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url; a.download = 'gopher_id_rsa'; a.click()
      URL.revokeObjectURL(url)
    } catch {
      toast.error('Download failed')
    }
  }

  // ── Choose ────────────────────────────────────────────────────────────────
  if (mode === 'choose') {
    return (
      <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-8 space-y-6">
        <div className="flex items-center gap-2 text-blue-600">
          <Key size={18} />
          <span className="font-semibold text-sm uppercase tracking-wide">Step 3 of 3 — SSH key</span>
        </div>
        <p className="text-sm text-gray-600">
          Gopher uses an SSH key pair to connect back into bootstrapped machines through their
          reverse tunnels. Generate a fresh key or bring your own.
        </p>
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Key name</label>
          <input
            type="text"
            value={keyName}
            onChange={e => setKeyName(e.target.value)}
            placeholder="Default"
            className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
          />
        </div>
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <button
            onClick={handleGenerate}
            disabled={loading}
            className="flex flex-col items-center gap-3 p-6 border-2 border-blue-200 rounded-xl hover:border-blue-400 hover:bg-blue-50 transition-all disabled:opacity-50"
          >
            {loading
              ? <Loader2 size={28} className="text-blue-400 animate-spin" />
              : <RefreshCw size={28} className="text-blue-500" />}
            <div className="text-center">
              <div className="font-semibold text-gray-800">Generate new key</div>
              <div className="text-xs text-gray-400 mt-1">Create a fresh RSA 4096-bit keypair</div>
            </div>
          </button>
          <button
            onClick={() => setMode('upload')}
            className="flex flex-col items-center gap-3 p-6 border-2 border-gray-200 rounded-xl hover:border-gray-400 hover:bg-gray-50 transition-all"
          >
            <Upload size={28} className="text-gray-500" />
            <div className="text-center">
              <div className="font-semibold text-gray-800">Upload existing key</div>
              <div className="text-xs text-gray-400 mt-1">Use your own id_rsa / id_rsa.pub</div>
            </div>
          </button>
        </div>
        <button
          onClick={onDone}
          className="w-full text-sm text-gray-400 hover:text-gray-600 py-2 transition-colors"
        >
          Skip — I'll set this up later
        </button>
      </div>
    )
  }

  // ── Generate result ───────────────────────────────────────────────────────
  if (mode === 'generate') {
    return (
      <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-8 space-y-5">
        <div className="flex items-center gap-2 text-blue-600">
          <Key size={18} />
          <span className="font-semibold text-sm uppercase tracking-wide">Step 3 of 3 — Key generated</span>
        </div>
        <div className="bg-green-50 border border-green-200 rounded-lg p-4 flex items-start gap-3">
          <CheckCircle2 size={18} className="text-green-600 mt-0.5 shrink-0" />
          <div className="text-sm text-green-800">
            <div className="font-semibold">Key pair generated and saved</div>
            <div className="text-xs mt-1">Download the private key now — it cannot be retrieved again after leaving this page.</div>
          </div>
        </div>
        <div>
          <div className="text-xs font-medium text-gray-500 mb-1">Public key</div>
          <div className="bg-gray-50 rounded-lg p-3 flex items-center gap-2">
            <code className="text-xs text-gray-700 break-all flex-1">{generatedKey?.public_key}</code>
            <button
              onClick={() => { navigator.clipboard.writeText(generatedKey?.public_key ?? ''); toast.success('Copied!') }}
              className="shrink-0 text-gray-400 hover:text-gray-600"
            >
              <ClipboardCopy size={14} />
            </button>
          </div>
        </div>
        <button
          onClick={downloadPrivateKey}
          className="w-full flex items-center justify-center gap-2 bg-gray-800 text-white py-2.5 rounded-lg text-sm font-semibold hover:bg-gray-900 transition-colors"
        >
          <Download size={15} /> Download private key (gopher_id_rsa)
        </button>
        <button
          onClick={onDone}
          className="w-full bg-green-600 text-white py-2.5 rounded-lg text-sm font-semibold hover:bg-green-700 transition-colors"
        >
          Continue to Dashboard →
        </button>
      </div>
    )
  }

  // ── Upload ────────────────────────────────────────────────────────────────
  return (
    <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-8 space-y-5">
      <div className="flex items-center gap-2 text-blue-600">
        <Upload size={18} />
        <span className="font-semibold text-sm uppercase tracking-wide">Step 3 of 3 — Upload SSH key</span>
      </div>
      <p className="text-sm text-gray-500">Paste your key contents or click Browse to select files.</p>
      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">Key name</label>
        <input
          type="text"
          value={keyName}
          onChange={e => setKeyName(e.target.value)}
          placeholder="Uploaded key"
          className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
        />
      </div>
      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">
          Private key <span className="text-gray-400 font-normal">(id_rsa — PEM or OpenSSH format)</span>
        </label>
        <div className="flex gap-2">
          <textarea
            value={privKeyText}
            onChange={e => setPrivKeyText(e.target.value)}
            rows={5}
            placeholder={'-----BEGIN RSA PRIVATE KEY-----\n...'}
            className="flex-1 border border-gray-300 rounded-lg px-3 py-2 text-xs font-mono resize-none focus:outline-none focus:ring-2 focus:ring-blue-500"
          />
          <label className="cursor-pointer flex flex-col items-center justify-center px-3 border border-gray-300 rounded-lg hover:bg-gray-50 text-gray-500 text-xs gap-1">
            <Upload size={14} />
            Browse
            <input type="file" className="hidden" onChange={e => { if (e.target.files?.[0]) readFile(e.target.files[0], setPrivKeyText) }} />
          </label>
        </div>
      </div>
      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">
          Public key <span className="text-gray-400 font-normal">(id_rsa.pub — authorized_keys format)</span>
        </label>
        <div className="flex gap-2">
          <textarea
            value={pubKeyText}
            onChange={e => setPubKeyText(e.target.value)}
            rows={3}
            placeholder="ssh-rsa AAAA..."
            className="flex-1 border border-gray-300 rounded-lg px-3 py-2 text-xs font-mono resize-none focus:outline-none focus:ring-2 focus:ring-blue-500"
          />
          <label className="cursor-pointer flex flex-col items-center justify-center px-3 border border-gray-300 rounded-lg hover:bg-gray-50 text-gray-500 text-xs gap-1">
            <Upload size={14} />
            Browse
            <input type="file" className="hidden" onChange={e => { if (e.target.files?.[0]) readFile(e.target.files[0], setPubKeyText) }} />
          </label>
        </div>
      </div>
      <div className="flex gap-3">
        <button
          onClick={() => setMode('choose')}
          className="px-4 py-2.5 border border-gray-300 text-gray-600 rounded-lg text-sm hover:bg-gray-50 transition-colors"
        >
          ← Back
        </button>
        <button
          onClick={handleUploadSave}
          disabled={loading || !privKeyText || !pubKeyText}
          className="flex-1 bg-blue-600 text-white py-2.5 rounded-lg text-sm font-semibold hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
        >
          {loading ? 'Validating…' : 'Save key pair'}
        </button>
      </div>
      <button onClick={onDone} className="w-full text-sm text-gray-400 hover:text-gray-600 py-1 transition-colors">
        Skip
      </button>
    </div>
  )
}

// ─── Main SetupPage ───────────────────────────────────────────────────────────
type SetupStep = 1 | 2 | 3

export default function SetupPage({ initialStep = 1 }: { initialStep?: SetupStep }) {
  const { refetch } = useAuth()
  const [step, setStep] = useState<SetupStep>(initialStep)

  const subtitle =
    step === 1 ? 'Create an admin password to get started'
    : step === 2 ? 'Set up local tunnel services'
    : 'Configure SSH key for machine access'

  return (
    <div className="min-h-screen bg-gray-50 flex items-center justify-center px-4">
      <div className="w-full max-w-md">
        <div className="text-center mb-8">
          <span className="text-5xl">🐹</span>
          <h1 className="mt-4 text-3xl font-bold text-gray-900">Welcome to Gopher</h1>
          <p className="mt-2 text-gray-500">{subtitle}</p>
        </div>

        {step === 1
          ? <PasswordStep onDone={() => setStep(2)} />
          : step === 2
          ? <ServicesStep onDone={() => setStep(3)} />
          : <SSHKeyStep onDone={refetch} />
        }
      </div>
    </div>
  )
}
