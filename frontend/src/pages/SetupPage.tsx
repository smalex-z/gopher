import { useEffect, useRef, useState } from 'react'

const toKeyFilename = (name: string) =>
  name.toLowerCase().replace(/\s+/g, '_').replace(/[^a-z0-9_-]/g, '')
import { Lock, Eye, EyeOff, CheckCircle2, XCircle, Loader2, SkipForward, Key, RefreshCw, Upload, Download, ClipboardCopy, Shield, ShieldAlert, ShieldCheck, ShieldOff, ShieldBan, AlertTriangle, MinusCircle } from 'lucide-react'
import client from '../api/client'
import { useAuth } from '../lib/auth'
import { localApi, type LocalServiceStatus, type FirewallStatus, type FirewallMode, type DNSCheckResult, type DNSCheck } from '../api/local'
import { toast } from '../lib/toast'
import DeployLogModal from '../components/DeployLogModal'
import DownloadKeyButton from '../components/DownloadKeyButton'
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
        <span className="font-semibold text-sm uppercase tracking-wide">Step 1 of 4 — Admin password</span>
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

// ─── DNS preflight UI ────────────────────────────────────────────────────────

function DNSCheckRow({ check }: { check: DNSCheck }) {
  const iconMap = {
    pass: <CheckCircle2 size={14} className="text-green-600" />,
    warn: <AlertTriangle size={14} className="text-amber-600" />,
    fail: <XCircle size={14} className="text-red-600" />,
    skip: <MinusCircle size={14} className="text-gray-400" />,
  }
  const labelColor = {
    pass: 'text-green-800',
    warn: 'text-amber-800',
    fail: 'text-red-800',
    skip: 'text-gray-500',
  }[check.status]
  return (
    <li className="flex items-start gap-2 text-xs">
      <span className="mt-0.5 shrink-0">{iconMap[check.status]}</span>
      <span>
        <span className={`font-medium ${labelColor}`}>{check.label}</span>
        <span className="text-gray-600"> — {check.message}</span>
      </span>
    </li>
  )
}

function DNSPreflightBanner({
  domain,
  serverIP,
  status,
  message,
  result,
}: {
  domain: string
  serverIP: string
  status: 'idle' | 'checking' | 'ok' | 'fail'
  message: string
  result: DNSCheckResult | null
}) {
  const wrapperCls =
    status === 'ok'
      ? 'bg-green-50 border-green-200 text-green-800'
      : status === 'fail'
      ? 'bg-red-50 border-red-200 text-red-800'
      : 'bg-blue-50 border-blue-200 text-blue-800'

  const headerIcon =
    status === 'checking' ? <Loader2 size={15} className="animate-spin" /> :
    status === 'ok' ? <CheckCircle2 size={15} /> :
    status === 'fail' ? <XCircle size={15} /> :
    <span>📋</span>

  const headerText =
    status === 'checking' ? 'Checking DNS…' :
    status === 'ok' ? 'DNS looks good' :
    status === 'fail' ? 'DNS not ready' :
    'DNS setup required'

  const serverIPDisplay = serverIP || '<your server IP>'

  return (
    <div className={`rounded-lg p-4 text-sm border space-y-2 ${wrapperCls}`}>
      <div className="flex items-center gap-2 font-semibold">
        {headerIcon}
        {headerText}
      </div>

      {/* Setup-guidance code block — shown when there's no result yet or the
          top-level check failed. Skipped when the preflight is happy. */}
      {(status === 'idle' || status === 'fail') && (
        <>
          <p>Point a <strong>wildcard A record</strong> at your DNS provider to this server's IP:</p>
          <code className="block bg-white border border-current/20 rounded px-3 py-1.5 text-xs font-mono text-gray-800">
            *.{domain}  →  {serverIPDisplay}
          </code>
          {status === 'idle' && (
            <p className="text-xs text-blue-600 mt-1">
              Every subdomain (e.g. <code>router.{domain}</code>) will resolve here automatically.
            </p>
          )}
        </>
      )}

      {/* One-line summary on the happy path */}
      {status === 'ok' && message && (
        <p className="text-xs">{message}</p>
      )}

      {/* Structured per-check results from the preflight */}
      {result?.checks && result.checks.length > 0 && (
        <ul className="space-y-1.5 pt-1 border-t border-current/15">
          {result.checks.map(c => (
            <DNSCheckRow key={c.name} check={c} />
          ))}
        </ul>
      )}
    </div>
  )
}

function ServicesStep({ onDone }: { onDone: () => void }) {
  const [domain, setDomain] = useState('')
  const [serverHost, setServerHost] = useState('')
  const [detectingIP, setDetectingIP] = useState(false)
  const [skipCaddy, setSkipCaddy] = useState(false)
  const [status, setStatus] = useState<LocalServiceStatus | null>(null)
  const [showLogs, setShowLogs] = useState(false)
  const [skipping, setSkipping] = useState(false)
  const [dnsStatus, setDnsStatus] = useState<'idle' | 'checking' | 'ok' | 'fail'>('idle')
  const [dnsMessage, setDnsMessage] = useState('')
  const [dnsResult, setDnsResult] = useState<DNSCheckResult | null>(null)
  // Public IP of this VPS — detected once on mount and passed to /check-dns
  // so the preflight's ip_match check can flag parking-page IPs and stale
  // records pointing at the wrong host.
  const [serverIP, setServerIP] = useState('')
  const [installComplete, setInstallComplete] = useState(false)
  const dnsTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const load = () => localApi.status().then(setStatus).catch(() => {})

  // On first mount, load status and pre-populate domain if already saved.
  // (We can't reach this step with local_setup_done=true — the gate in AppShell
  // would skip past us — so no else-branch is needed.)
  useEffect(() => {
    localApi.status().then(s => {
      setStatus(s)
      if (s.domain) {
        setDomain(s.domain)
        setSkipCaddy(false)
      }
    }).catch(() => {})
  }, [])

  useEffect(() => {
    const t = setInterval(load, 5000)
    return () => clearInterval(t)
  }, [])

  // Auto-detect public IP when switching to rathole-only mode
  useEffect(() => {
    if (!skipCaddy || serverHost) return
    setDetectingIP(true)
    localApi.detectIP()
      .then(({ ip }) => { if (ip) setServerHost(ip) })
      .catch(() => {})
      .finally(() => setDetectingIP(false))
  }, [skipCaddy, serverHost])

  // Detect the public IP once on mount regardless of skipCaddy — the DNS
  // preflight needs it for the ip_match check (catches parking-page IPs
  // and stale records pointing at the wrong host). Cheap, runs in parallel
  // with everything else, swallows errors silently.
  useEffect(() => {
    localApi.detectIP()
      .then(({ ip }) => { if (ip) setServerIP(ip) })
      .catch(() => {})
  }, [])

  // Debounced DNS preflight whenever domain (or detected server IP) changes.
  // Re-runs when serverIP arrives so the ip_match check has something to
  // compare against; the wizard would otherwise have to wait for the user
  // to retype the domain before the check became aware of the IP.
  useEffect(() => {
    if (dnsTimerRef.current) clearTimeout(dnsTimerRef.current)
    if (skipCaddy) {
      setDnsStatus('idle')
      setDnsMessage('')
      setDnsResult(null)
      return
    }
    const trimmed = domain.trim()
    if (!trimmed || !trimmed.includes('.')) {
      setDnsStatus('idle')
      setDnsMessage('')
      setDnsResult(null)
      return
    }
    setDnsStatus('checking')
    dnsTimerRef.current = setTimeout(async () => {
      try {
        const result = await localApi.checkDNS(trimmed, serverIP || undefined)
        setDnsResult(result)
        if (result.ok) {
          setDnsStatus('ok')
          setDnsMessage(result.resolved_to ? `Resolves to ${result.resolved_to}` : 'DNS resolves')
        } else {
          setDnsStatus('fail')
          setDnsMessage(result.message ?? 'DNS not ready')
        }
      } catch {
        setDnsStatus('fail')
        setDnsMessage('DNS check failed')
        setDnsResult(null)
      }
    }, 1200)
    return () => { if (dnsTimerRef.current) clearTimeout(dnsTimerRef.current) }
  }, [domain, skipCaddy, serverIP])

  // Advance to step 3 (firewall) once install completes. We deliberately do NOT
  // redirect to https://router.{domain} here — port 80/443 may still be blocked by
  // the existing firewall, so Caddy can't obtain a cert yet. The redirect happens
  // after the firewall step, when ports are guaranteed open.
  useEffect(() => {
    if (!installComplete) return
    const t = setTimeout(onDone, 1500)
    return () => clearTimeout(t)
  }, [installComplete, onDone])

  const allGood = status?.caddy_active === 'active' && status?.rathole_active === 'active'

  const handleSkip = async () => {
    setSkipping(true)
    await localApi.skip(domain || undefined).catch(() => {})
    onDone()
  }

  const canInstall = skipCaddy
    ? Boolean(serverHost.trim() && (status == null || status.has_install_permission))
    : Boolean(domain && dnsStatus === 'ok' && (status == null || status.has_install_permission))

  return (
    <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-8 space-y-6">
      <div className="flex items-center gap-2 text-blue-600">
        <span className="font-semibold text-sm uppercase tracking-wide">Step 2 of 4 — Local services</span>
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

      {/* VPS host input (rathole-only mode) */}
      {skipCaddy && (
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">
            VPS hostname or IP <span className="text-red-500">*</span>
          </label>
          <div className="flex gap-2">
            <input
              type="text"
              value={serverHost}
              onChange={e => setServerHost(e.target.value)}
              placeholder={detectingIP ? 'Detecting…' : '203.0.113.10 or vps.example.com'}
              disabled={detectingIP}
              className="flex-1 border border-gray-300 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500 disabled:bg-gray-50 disabled:text-gray-400"
              autoFocus
            />
            <button
              type="button"
              onClick={() => {
                setServerHost('')
                setDetectingIP(true)
                localApi.detectIP()
                  .then(({ ip }) => { if (ip) setServerHost(ip) })
                  .catch(() => {})
                  .finally(() => setDetectingIP(false))
              }}
              disabled={detectingIP}
              className="px-3 py-2 border border-gray-300 rounded-lg text-sm text-gray-600 hover:bg-gray-50 disabled:opacity-50 transition-colors"
              title="Re-detect public IP"
            >
              {detectingIP ? <Loader2 size={14} className="animate-spin" /> : <RefreshCw size={14} />}
            </button>
          </div>
          <p className="text-xs text-gray-400 mt-1">
            Used as the rathole server address in client configs — must be reachable from your private machines.
          </p>
        </div>
      )}

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

      {/* DNS preflight banner */}
      {!skipCaddy && domain && domain.includes('.') && (
        <DNSPreflightBanner
          domain={domain}
          serverIP={serverIP}
          status={dnsStatus}
          message={dnsMessage}
          result={dnsResult}
        />
      )}

      {/* Post-install advance notice */}
      {installComplete && (
        <div className="bg-green-50 border border-green-200 rounded-lg p-4 text-sm text-green-800 space-y-1">
          <div className="font-semibold flex items-center gap-2"><CheckCircle2 size={15} /> Local services installed</div>
          <p>Continuing to firewall configuration…</p>
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
        onStart={() => localApi.install(skipCaddy ? '' : domain, skipCaddy ? serverHost.trim() : domain, skipCaddy)}
        wsPath="/api/local/logs/ws"
        autoStart
      />
    </div>
  )
}

// ─── Step 3: Firewall ────────────────────────────────────────────────────────

type FirewallModeOption = { id: FirewallMode; label: string; description: string; icon: React.ReactNode; recommended?: boolean }

const FIREWALL_OPTIONS: FirewallModeOption[] = [
  {
    id: 'gopher',
    label: 'Gopher-managed firewall',
    recommended: true,
    icon: <ShieldCheck size={22} className="text-blue-500" />,
    description: 'Gopher manages iptables directly. Ports are opened and closed automatically as tunnels are created or deleted. Existing UFW/firewalld/nftables services will be disabled.',
  },
  {
    id: 'manual',
    label: 'Keep existing firewall',
    icon: <Shield size={22} className="text-amber-500" />,
    description: 'Gopher will NOT modify firewall rules. You must manually open the rathole port for each tunnel. Gopher will show the required commands when needed.',
  },
  {
    id: 'none',
    label: 'No firewall management',
    icon: <ShieldOff size={22} className="text-gray-400" />,
    description: 'No firewall enforcement by Gopher. Only safe in isolated or already-locked-down environments.',
  },
]

function FirewallPill({ label, active, installed }: { label: string; active: boolean; installed?: boolean }) {
  if (!installed && installed !== undefined) {
    return <span className="px-2 py-0.5 rounded-full text-xs bg-gray-100 text-gray-400">{label}: not installed</span>
  }
  return (
    <span className={`flex items-center gap-1 px-2 py-0.5 rounded-full text-xs font-medium ${active ? 'bg-amber-100 text-amber-700' : 'bg-gray-100 text-gray-500'}`}>
      {active ? <ShieldAlert size={11} /> : <Shield size={11} />}
      {label}: {active ? 'active' : 'inactive'}
    </span>
  )
}

function FirewallStep({ onDone }: { onDone: () => void }) {
  const [detected, setDetected] = useState<FirewallStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [selected, setSelected] = useState<FirewallMode>('gopher')
  const [confirming, setConfirming] = useState(false)
  const [showLogs, setShowLogs] = useState(false)
  const [done, setDone] = useState(false)
  const [skipping, setSkipping] = useState(false)
  const [domain, setDomain] = useState('')
  const [caddyInstalled, setCaddyInstalled] = useState(false)
  // Countdown shown on the success card before we redirect / advance.
  const [countdown, setCountdown] = useState(5)

  useEffect(() => {
    localApi.detectFirewall()
      .then(setDetected)
      .catch(() => setDetected(null))
      .finally(() => setLoading(false))
    // Cache domain + caddy install state so we know whether to redirect to
    // router.{domain} after takeover (only meaningful when Caddy was set up).
    localApi.status().then(s => {
      setDomain(s.domain || '')
      setCaddyInstalled(Boolean(s.caddy_installed))
    }).catch(() => {})
  }, [])

  // After gopher-mode takeover succeeds, the dashboard port (4321) is locked
  // down to localhost. If Caddy was installed and we have a domain, the
  // dashboard now lives at https://router.{domain} — redirect there. The 5s
  // wait gives Caddy time to complete the ACME challenge against the
  // newly-opened ports 80/443 before the browser hits the new URL. Without
  // Caddy / a domain there's no router.{domain}, so we just advance via
  // refetch and let AppShell's gates pick the next step (step 4 SSH key).
  const shouldRedirect = caddyInstalled && domain !== ''

  useEffect(() => {
    if (!done) return
    if (countdown <= 0) {
      if (shouldRedirect) {
        window.location.href = `https://router.${domain}`
      } else {
        onDone()
      }
      return
    }
    const t = setTimeout(() => setCountdown(c => c - 1), 1000)
    return () => clearTimeout(t)
  }, [done, countdown, shouldRedirect, domain, onDone])

  const handleContinue = () => {
    if (selected === 'gopher') {
      setConfirming(true)
    } else {
      // Manual/none: kick off configure (which just saves the setting) then advance.
      setSkipping(true)
      localApi.configureFirewall(selected)
        .then(() => onDone())
        .catch(() => {
          toast.error('Failed to save firewall preference')
          setSkipping(false)
        })
    }
  }

  if (done) {
    const skipNow = () => {
      setCountdown(0)
    }
    return (
      <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-8 space-y-4">
        <div className="flex items-center gap-2 text-blue-600">
          <ShieldCheck size={18} />
          <span className="font-semibold text-sm uppercase tracking-wide">Step 3 of 4 — Firewall</span>
        </div>
        <div className="bg-green-50 border border-green-200 rounded-lg p-4 flex items-start gap-3">
          <CheckCircle2 size={18} className="text-green-600 mt-0.5 shrink-0" />
          <div className="text-sm text-green-800">
            <div className="font-semibold">Firewall configured successfully</div>
            <div className="text-xs mt-1">iptables rules are active and will persist across reboots.</div>
            {shouldRedirect ? (
              <div className="text-xs mt-2">
                The dashboard now lives at <strong>https://router.{domain}</strong>.
                Waiting <strong>{countdown}s</strong> for Caddy to issue the TLS cert before redirecting…
              </div>
            ) : (
              <div className="text-xs mt-2">
                Continuing in <strong>{countdown}s</strong>…
              </div>
            )}
          </div>
        </div>
        <button
          onClick={skipNow}
          className="w-full bg-blue-600 text-white py-2.5 rounded-lg text-sm font-semibold hover:bg-blue-700 transition-colors"
        >
          {shouldRedirect ? `Go to https://router.${domain} now →` : 'Continue now →'}
        </button>
      </div>
    )
  }

  if (confirming) {
    const activeManagers = [
      detected?.ufw.active && 'UFW',
      detected?.firewalld.active && 'firewalld',
      detected?.nftables.active && 'nftables',
    ].filter(Boolean)

    return (
      <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-8 space-y-5">
        <div className="flex items-center gap-2 text-blue-600">
          <ShieldCheck size={18} />
          <span className="font-semibold text-sm uppercase tracking-wide">Step 3 of 4 — Firewall</span>
        </div>

        <div className="bg-amber-50 border border-amber-200 rounded-lg p-4 space-y-3 text-sm text-amber-900">
          <div className="font-semibold flex items-center gap-2"><ShieldAlert size={16} /> Confirm firewall takeover</div>
          <ul className="space-y-1.5 text-xs list-none">
            {activeManagers.length > 0 && (
              <li className="flex gap-2"><span>•</span><span><strong>{activeManagers.join(', ')}</strong> will be disabled (not uninstalled — you can re-enable manually)</span></li>
            )}
            <li className="flex gap-2"><span>•</span><span>Current iptables rules will be backed up to <code>/root/gopher-firewall-backup.rules</code></span></li>
            <li className="flex gap-2"><span>•</span><span><strong>SSH (port 22) will remain open</strong> — you will not be locked out</span></li>
            <li className="flex gap-2"><span>•</span><span>Ports 80, 443, 2333, and the dashboard port will be allowed</span></li>
            <li className="flex gap-2"><span>•</span><span>All tunnel ports will be opened automatically going forward</span></li>
          </ul>
        </div>

        <div className="flex gap-3">
          <button
            onClick={() => setConfirming(false)}
            className="px-4 py-2.5 border border-gray-300 text-gray-600 rounded-lg text-sm hover:bg-gray-50 transition-colors"
          >
            ← Back
          </button>
          <button
            onClick={() => setShowLogs(true)}
            className="flex-1 bg-blue-600 text-white py-2.5 rounded-lg text-sm font-semibold hover:bg-blue-700 transition-colors"
          >
            Confirm & Apply
          </button>
        </div>

        <DeployLogModal
          isOpen={showLogs}
          onClose={() => setShowLogs(false)}
          onComplete={() => setDone(true)}
          title="Configuring Firewall"
          onStart={() => localApi.configureFirewall('gopher')}
          wsPath="/api/local/logs/ws"
          autoStart
        />
      </div>
    )
  }

  return (
    <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-8 space-y-6">
      <div className="flex items-center gap-2 text-blue-600">
        <Shield size={18} />
        <span className="font-semibold text-sm uppercase tracking-wide">Step 3 of 4 — Firewall</span>
      </div>

      <p className="text-sm text-gray-600">
        Choose how Gopher should handle firewall rules. This controls which ports are open on this server.
      </p>

      {/* Detection results */}
      {loading ? (
        <div className="flex items-center gap-2 text-sm text-gray-400">
          <Loader2 size={14} className="animate-spin" /> Detecting firewall state…
        </div>
      ) : detected ? (
        <div className="space-y-2">
          <p className="text-xs font-medium text-gray-500 uppercase tracking-wide">Detected on this system</p>
          <div className="flex flex-wrap gap-2">
            <FirewallPill label="UFW" installed={detected.ufw.installed} active={detected.ufw.active} />
            <FirewallPill label="firewalld" installed={detected.firewalld.installed} active={detected.firewalld.active} />
            <FirewallPill label="nftables" installed={detected.nftables.installed} active={detected.nftables.active} />
            <FirewallPill label="iptables" installed={detected.iptables.available} active={false} />
          </div>
          {!detected.any_active && (
            <p className="text-xs text-gray-400">No active firewall detected. Gopher-managed mode will set up a fresh iptables configuration.</p>
          )}
        </div>
      ) : (
        <p className="text-xs text-amber-600">Could not detect firewall state — detection may require sudo access.</p>
      )}

      {/* Mode selection */}
      <div className="space-y-3">
        {FIREWALL_OPTIONS.map(opt => (
          <button
            key={opt.id}
            onClick={() => setSelected(opt.id)}
            className={`w-full text-left p-4 rounded-xl border-2 transition-all ${
              selected === opt.id
                ? 'border-blue-400 bg-blue-50'
                : 'border-gray-200 hover:border-gray-300 hover:bg-gray-50'
            }`}
          >
            <div className="flex items-center gap-3">
              {opt.icon}
              <div className="flex-1">
                <div className="flex items-center gap-2">
                  <span className="font-semibold text-sm text-gray-800">{opt.label}</span>
                  {opt.recommended && (
                    <span className="px-1.5 py-0.5 bg-blue-100 text-blue-700 text-xs rounded font-medium">Recommended</span>
                  )}
                </div>
                <p className="text-xs text-gray-500 mt-0.5">{opt.description}</p>
              </div>
              <div className={`w-4 h-4 rounded-full border-2 shrink-0 flex items-center justify-center ${
                selected === opt.id ? 'border-blue-500 bg-blue-500' : 'border-gray-300'
              }`}>
                {selected === opt.id && <div className="w-1.5 h-1.5 rounded-full bg-white" />}
              </div>
            </div>
          </button>
        ))}
      </div>

      <div className="flex gap-3">
        <button
          onClick={handleContinue}
          disabled={skipping}
          className="flex-1 bg-blue-600 text-white py-2.5 rounded-lg text-sm font-semibold hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
        >
          {skipping ? <span className="flex items-center justify-center gap-2"><Loader2 size={14} className="animate-spin" /> Saving…</span> : 'Continue →'}
        </button>
        <button
          onClick={onDone}
          className="flex items-center gap-1.5 px-4 py-2.5 border border-gray-300 text-gray-600 rounded-lg text-sm font-medium hover:bg-gray-50 transition-colors"
        >
          <SkipForward size={15} /> Skip
        </button>
      </div>
    </div>
  )
}

// ─── Step 4: SSH Key ─────────────────────────────────────────────────────────
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


  // ── Choose ────────────────────────────────────────────────────────────────
  if (mode === 'choose') {
    return (
      <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-8 space-y-6">
        <div className="flex items-center gap-2 text-blue-600">
          <Key size={18} />
          <span className="font-semibold text-sm uppercase tracking-wide">Step 4 of 4 — SSH key</span>
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
          <span className="font-semibold text-sm uppercase tracking-wide">Step 4 of 4 — Key generated</span>
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
        {generatedKey && (
          <DownloadKeyButton
            id={generatedKey.id}
            name={generatedKey.name}
            className="w-full flex items-center justify-center gap-2 bg-gray-800 text-white py-2.5 rounded-lg text-sm font-semibold hover:bg-gray-900 transition-colors"
          >
            <Download size={15} /> Download private key ({toKeyFilename(generatedKey.name) || 'id_rsa'})
          </DownloadKeyButton>
        )}
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
        <span className="font-semibold text-sm uppercase tracking-wide">Step 4 of 4 — Upload SSH key</span>
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

// ─── Step 5: fail2ban (upgrade prompt for existing installs) ─────────────────

function Fail2banStep({ onDone }: { onDone: () => void }) {
  const [showLogs, setShowLogs] = useState(false)
  const [skipping, setSkipping] = useState(false)

  const handleSkip = () => {
    setSkipping(true)
    // Mark done server-side by calling the endpoint with a no-op flag,
    // or just advance — the flag won't be set, user can re-trigger from Security page.
    // For skip, we call the skip-fail2ban endpoint (or we can accept the unset state
    // and let them come back). Simplest: just refetch, which will re-check the flag.
    onDone()
  }

  return (
    <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-8 space-y-6">
      <div className="flex items-center gap-2 text-blue-600">
        <ShieldBan size={18} />
        <span className="font-semibold text-sm uppercase tracking-wide">New: fail2ban protection</span>
      </div>

      <div className="space-y-3 text-sm text-gray-600">
        <p>
          Gopher now integrates with <strong>fail2ban</strong> to automatically ban IPs that repeatedly
          fail authentication. This step installs and configures it on your server.
        </p>
        <ul className="space-y-1.5 text-xs list-none pl-0">
          <li className="flex gap-2"><span className="text-green-500">✓</span><span>Monitors Gopher login events via systemd journal</span></li>
          <li className="flex gap-2"><span className="text-green-500">✓</span><span>Bans IPs after repeated failures via iptables</span></li>
          <li className="flex gap-2"><span className="text-green-500">✓</span><span>Configurable from the Security page</span></li>
        </ul>
      </div>

      <div className="flex gap-3">
        <button
          onClick={() => setShowLogs(true)}
          className="flex-1 bg-blue-600 text-white py-2.5 rounded-lg text-sm font-semibold hover:bg-blue-700 transition-colors"
        >
          Install fail2ban
        </button>
        <button
          onClick={handleSkip}
          disabled={skipping}
          className="flex items-center gap-1.5 px-4 py-2.5 border border-gray-300 text-gray-600 rounded-lg text-sm font-medium hover:bg-gray-50 transition-colors"
        >
          <SkipForward size={15} /> Skip for now
        </button>
      </div>

      <DeployLogModal
        isOpen={showLogs}
        onClose={() => setShowLogs(false)}
        onComplete={onDone}
        title="Installing fail2ban"
        onStart={() => localApi.setupFail2ban()}
        wsPath="/api/logs/ws"
        autoStart
      />
    </div>
  )
}

// ─── Main SetupPage ───────────────────────────────────────────────────────────
type SetupStep = 1 | 2 | 3 | 4 | 5

export default function SetupPage({ initialStep = 1 }: { initialStep?: SetupStep }) {
  const { refetch } = useAuth()
  const [step, setStep] = useState<SetupStep>(initialStep)

  const subtitle =
    step === 1 ? 'Create an admin password to get started'
    : step === 2 ? 'Set up local tunnel services'
    : step === 3 ? 'Configure firewall rules'
    : step === 5 ? 'Automatic IP banning for your server'
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
          : step === 3
          ? <FirewallStep onDone={refetch} />
          : step === 5
          ? <Fail2banStep onDone={refetch} />
          : <SSHKeyStep onDone={refetch} />
        }
      </div>
    </div>
  )
}
