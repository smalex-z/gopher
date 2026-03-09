import { useEffect, useState } from 'react'
import { Lock, Eye, EyeOff, CheckCircle2, XCircle, Loader2, SkipForward } from 'lucide-react'
import client from '../api/client'
import { useAuth } from '../lib/auth'
import { localApi, type LocalServiceStatus } from '../api/local'
import { toast } from '../lib/toast'
import DeployLogModal from '../components/DeployLogModal'

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
        <span className="font-semibold text-sm uppercase tracking-wide">Step 1 of 2 — Admin password</span>
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
  const [status, setStatus] = useState<LocalServiceStatus | null>(null)
  const [showLogs, setShowLogs] = useState(false)
  const [skipping, setSkipping] = useState(false)

  const load = () => localApi.status().then(setStatus).catch(() => {})

  // On first mount, load status and pre-populate domain if already saved
  useEffect(() => {
    localApi.status().then(s => {
      setStatus(s)
      if (s.domain) setDomain(s.domain)
    }).catch(() => {})
  }, [])

  useEffect(() => {
    const t = setInterval(load, 5000)
    return () => clearInterval(t)
  }, [])

  const allGood = status?.caddy_active === 'active' && status?.rathole_active === 'active'

  const handleSkip = async () => {
    setSkipping(true)
    await localApi.skip(domain || undefined).catch(() => {})
    onDone()
  }

  const handleContinue = async () => {
    if (domain) {
      await localApi.skip(domain).catch(() => {})
    }
    onDone()
  }

  return (
    <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-8 space-y-6">
      <div className="flex items-center gap-2 text-blue-600">
        <span className="font-semibold text-sm uppercase tracking-wide">Step 2 of 2 — Local services</span>
      </div>

      <p className="text-sm text-gray-600">
        Gopher needs <strong>Caddy</strong> (HTTPS reverse proxy) and <strong>rathole</strong> (tunnel server)
        running locally as systemd services. Enter your domain and click <em>Install &amp; Configure</em> to set
        them up automatically.
      </p>

      {/* Permission warning */}
      {status && !status.has_install_permission && (
        <div className="bg-amber-50 border border-amber-200 rounded-lg p-4 text-sm text-amber-800">
          <strong>Permission required:</strong> Gopher needs root access to write to{' '}
          <code>/etc/caddy/</code>, <code>/etc/systemd/system/</code>, and run{' '}
          <code>systemctl</code>. Restart the binary with <code>sudo ./gopher</code> or ensure
          passwordless sudo is configured.
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

      {/* Wildcard DNS notice */}
      {domain && (
        <div className="bg-blue-50 border border-blue-200 rounded-lg p-4 text-sm text-blue-800 space-y-1">
          <div className="font-semibold">📋 DNS setup required</div>
          <p>
            Point a <strong>wildcard A record</strong> at your DNS provider to this server's IP:
          </p>
          <code className="block bg-white border border-blue-200 rounded px-3 py-1.5 text-xs font-mono mt-1">
            *.{domain}  →  {'<your server IP>'}
          </code>
          <p className="text-xs text-blue-600 mt-1">
            This lets every subdomain (e.g. <code>photos.{domain}</code>, <code>router.{domain}</code>) resolve to this machine automatically.
          </p>
        </div>
      )}

      <div className="flex gap-3">
        <button
          onClick={() => setShowLogs(true)}
          disabled={!domain || (status != null && !status.has_install_permission)}
          className="flex-1 bg-blue-600 text-white py-2.5 rounded-lg text-sm font-semibold hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
        >
          {allGood ? '↻ Re-configure' : '⚙ Install & Configure'}
        </button>
        <button
          onClick={handleSkip}
          disabled={skipping}
          className="flex items-center gap-1.5 px-4 py-2.5 border border-gray-300 text-gray-600 rounded-lg text-sm font-medium hover:bg-gray-50 transition-colors"
        >
          <SkipForward size={15} /> Skip
        </button>
      </div>

      {allGood && (
        <button
          onClick={handleContinue}
          disabled={!domain}
          className="w-full bg-green-600 text-white py-2.5 rounded-lg text-sm font-semibold hover:bg-green-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
        >
          {domain ? '✓ Continue to Dashboard →' : 'Enter a domain name above to continue'}
        </button>
      )}

      <DeployLogModal
        isOpen={showLogs}
        onClose={() => { setShowLogs(false); load() }}
        title="Installing Local Services"
        onStart={() => localApi.install(domain)}
        autoStart
      />
    </div>
  )
}

// ─── Main SetupPage ───────────────────────────────────────────────────────────

export default function SetupPage({ initialStep = 1 }: { initialStep?: 1 | 2 }) {
  const { refetch } = useAuth()
  const [step, setStep] = useState<1 | 2>(initialStep)

  return (
    <div className="min-h-screen bg-gray-50 flex items-center justify-center px-4">
      <div className="w-full max-w-md">
        <div className="text-center mb-8">
          <span className="text-5xl">🐹</span>
          <h1 className="mt-4 text-3xl font-bold text-gray-900">Welcome to Gopher</h1>
          <p className="mt-2 text-gray-500">
            {step === 1 ? 'Create an admin password to get started' : 'Set up local tunnel services'}
          </p>
        </div>

        {step === 1
          ? <PasswordStep onDone={() => setStep(2)} />
          : <ServicesStep onDone={refetch} />
        }
      </div>
    </div>
  )
}
