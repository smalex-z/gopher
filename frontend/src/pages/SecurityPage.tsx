import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  ShieldCheck, ShieldOff, KeyRound, RefreshCw, Copy, Check,
  Ban, Trash2, Plus, AlertTriangle, Activity,
} from 'lucide-react'
import client from '../api/client'
import { securityApi, type AuditEvent, type Fail2banStatus, type Fail2banConfig } from '../api/security'
import { toast } from '../lib/toast'

// ─── TOTP types + hook ───────────────────────────────────────────────────────

interface TOTPStatus {
  enabled: boolean
  backup_codes_remaining: number
}

interface EnrollData {
  secret: string
  qr_data_url: string
}

function useTOTPStatus() {
  return useQuery<TOTPStatus>({
    queryKey: ['totp-status'],
    queryFn: () => client.get<{ data: TOTPStatus }>('/auth/2fa/status').then(r => r.data.data),
  })
}

// ─── Enrollment flow ─────────────────────────────────────────────────────────

function EnrollPanel({ onDone }: { onDone: () => void }) {
  const [step, setStep] = useState<'qr' | 'confirm' | 'codes'>('qr')
  const [enrollData, setEnrollData] = useState<EnrollData | null>(null)
  const [code, setCode] = useState('')
  const [backupCodes, setBackupCodes] = useState<string[]>([])
  const [copied, setCopied] = useState(false)
  const [loading, setLoading] = useState(false)

  const startEnroll = async () => {
    setLoading(true)
    try {
      const res = await client.post<{ data: EnrollData }>('/auth/2fa/enroll')
      setEnrollData(res.data.data)
      setStep('qr')
    } catch {
      toast.error('Failed to start enrollment')
    } finally {
      setLoading(false)
    }
  }

  const confirmEnroll = async (e: React.FormEvent) => {
    e.preventDefault()
    setLoading(true)
    try {
      const res = await client.post<{ data: { backup_codes: string[] } }>('/auth/2fa/confirm', { code })
      setBackupCodes(res.data.data.backup_codes)
      setStep('codes')
    } catch {
      toast.error('Invalid code — check your authenticator app and try again')
      setCode('')
    } finally {
      setLoading(false)
    }
  }

  const copyAllCodes = () => {
    navigator.clipboard.writeText(backupCodes.join('\n')).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }

  if (!enrollData && step === 'qr') {
    return (
      <div className="space-y-4">
        <p className="text-sm text-gray-600">
          Two-factor authentication adds a second layer of security. You'll need an authenticator app
          like <strong>Google Authenticator</strong>, <strong>1Password</strong>, or <strong>Authy</strong>.
        </p>
        <button
          onClick={startEnroll}
          disabled={loading}
          className="bg-blue-600 text-white px-4 py-2 rounded-lg text-sm font-semibold hover:bg-blue-700 disabled:opacity-50 transition-colors"
        >
          {loading ? 'Generating…' : 'Set up 2FA'}
        </button>
      </div>
    )
  }

  if (step === 'qr' && enrollData) {
    return (
      <div className="space-y-4">
        <p className="text-sm text-gray-600">
          Scan this QR code with your authenticator app, then enter the 6-digit code to confirm.
        </p>
        <div className="flex justify-center">
          <img src={enrollData.qr_data_url} alt="TOTP QR code" className="rounded-lg border border-gray-200" width={200} height={200} />
        </div>
        <details className="text-xs text-gray-500">
          <summary className="cursor-pointer hover:text-gray-700">Can't scan? Enter the key manually</summary>
          <code className="block mt-2 bg-gray-50 border border-gray-200 rounded px-3 py-2 font-mono break-all select-all">
            {enrollData.secret}
          </code>
        </details>
        <form onSubmit={confirmEnroll} className="space-y-3">
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">Verification code</label>
            <input
              type="text"
              inputMode="numeric"
              value={code}
              onChange={e => setCode(e.target.value.replace(/\s/g, ''))}
              placeholder="000000"
              maxLength={6}
              className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm text-center tracking-widest font-mono focus:outline-none focus:ring-2 focus:ring-blue-500"
              required
              autoFocus
              autoComplete="one-time-code"
            />
          </div>
          <div className="flex gap-2">
            <button
              type="submit"
              disabled={loading || code.length < 6}
              className="flex-1 bg-blue-600 text-white py-2 rounded-lg text-sm font-semibold hover:bg-blue-700 disabled:opacity-50 transition-colors"
            >
              {loading ? 'Verifying…' : 'Enable 2FA'}
            </button>
            <button
              type="button"
              onClick={() => { setEnrollData(null); setCode('') }}
              className="px-4 py-2 border border-gray-300 text-gray-600 rounded-lg text-sm hover:bg-gray-50 transition-colors"
            >
              Cancel
            </button>
          </div>
        </form>
      </div>
    )
  }

  if (step === 'codes') {
    return (
      <div className="space-y-4">
        <div className="flex items-start gap-2 bg-amber-50 border border-amber-200 rounded-lg p-4 text-sm text-amber-800">
          <KeyRound size={16} className="mt-0.5 shrink-0" />
          <div>
            <strong>Save your backup codes now.</strong> Each code can only be used once and they won't be shown again.
            Store them somewhere safe (password manager, printed paper).
          </div>
        </div>
        <div className="bg-gray-50 border border-gray-200 rounded-lg p-4 font-mono text-sm grid grid-cols-2 gap-1">
          {backupCodes.map(c => <span key={c}>{c}</span>)}
        </div>
        <div className="flex gap-2">
          <button
            onClick={copyAllCodes}
            className="flex items-center gap-1.5 px-3 py-2 border border-gray-300 text-gray-600 rounded-lg text-sm hover:bg-gray-50 transition-colors"
          >
            {copied ? <Check size={14} className="text-green-600" /> : <Copy size={14} />}
            {copied ? 'Copied!' : 'Copy all'}
          </button>
          <button
            onClick={onDone}
            className="flex-1 bg-blue-600 text-white py-2 rounded-lg text-sm font-semibold hover:bg-blue-700 transition-colors"
          >
            Done — 2FA is enabled
          </button>
        </div>
      </div>
    )
  }

  return null
}

// ─── Disable panel ────────────────────────────────────────────────────────────

function DisablePanel({ onDone }: { onDone: () => void }) {
  const [code, setCode] = useState('')
  const [loading, setLoading] = useState(false)

  const handleDisable = async (e: React.FormEvent) => {
    e.preventDefault()
    setLoading(true)
    try {
      await client.post('/auth/2fa/disable', { code })
      toast.success('2FA disabled')
      onDone()
    } catch {
      toast.error('Invalid code')
      setCode('')
    } finally {
      setLoading(false)
    }
  }

  return (
    <form onSubmit={handleDisable} className="space-y-3">
      <p className="text-sm text-gray-600">Enter your current TOTP code or a backup code to disable 2FA.</p>
      <input
        type="text"
        inputMode="numeric"
        value={code}
        onChange={e => setCode(e.target.value.replace(/\s/g, ''))}
        placeholder="000000 or backup code"
        maxLength={11}
        className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm text-center tracking-widest font-mono focus:outline-none focus:ring-2 focus:ring-red-400"
        required
        autoFocus
        autoComplete="one-time-code"
      />
      <div className="flex gap-2">
        <button
          type="submit"
          disabled={loading || !code}
          className="flex-1 bg-red-600 text-white py-2 rounded-lg text-sm font-semibold hover:bg-red-700 disabled:opacity-50 transition-colors"
        >
          {loading ? 'Disabling…' : 'Disable 2FA'}
        </button>
        <button type="button" onClick={onDone} className="px-4 py-2 border border-gray-300 text-gray-600 rounded-lg text-sm hover:bg-gray-50 transition-colors">
          Cancel
        </button>
      </div>
    </form>
  )
}

// ─── Regenerate backup codes panel ───────────────────────────────────────────

function RegeneratePanel({ onDone }: { onDone: () => void }) {
  const [code, setCode] = useState('')
  const [newCodes, setNewCodes] = useState<string[]>([])
  const [copied, setCopied] = useState(false)
  const [loading, setLoading] = useState(false)

  const handleRegenerate = async (e: React.FormEvent) => {
    e.preventDefault()
    setLoading(true)
    try {
      const res = await client.post<{ data: { backup_codes: string[] } }>('/auth/2fa/backup-codes/regenerate', { code })
      setNewCodes(res.data.data.backup_codes)
    } catch {
      toast.error('Invalid code')
      setCode('')
    } finally {
      setLoading(false)
    }
  }

  const copyAll = () => {
    navigator.clipboard.writeText(newCodes.join('\n')).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }

  if (newCodes.length > 0) {
    return (
      <div className="space-y-4">
        <div className="flex items-start gap-2 bg-amber-50 border border-amber-200 rounded-lg p-4 text-sm text-amber-800">
          <KeyRound size={16} className="mt-0.5 shrink-0" />
          <strong>Your old codes are now invalid. Save these new ones.</strong>
        </div>
        <div className="bg-gray-50 border border-gray-200 rounded-lg p-4 font-mono text-sm grid grid-cols-2 gap-1">
          {newCodes.map(c => <span key={c}>{c}</span>)}
        </div>
        <div className="flex gap-2">
          <button onClick={copyAll} className="flex items-center gap-1.5 px-3 py-2 border border-gray-300 text-gray-600 rounded-lg text-sm hover:bg-gray-50 transition-colors">
            {copied ? <Check size={14} className="text-green-600" /> : <Copy size={14} />}
            {copied ? 'Copied!' : 'Copy all'}
          </button>
          <button onClick={onDone} className="flex-1 bg-blue-600 text-white py-2 rounded-lg text-sm font-semibold hover:bg-blue-700 transition-colors">
            Done
          </button>
        </div>
      </div>
    )
  }

  return (
    <form onSubmit={handleRegenerate} className="space-y-3">
      <p className="text-sm text-gray-600">
        This invalidates all existing backup codes. Enter your current TOTP code to confirm.
      </p>
      <input
        type="text"
        inputMode="numeric"
        value={code}
        onChange={e => setCode(e.target.value.replace(/\s/g, ''))}
        placeholder="000000"
        maxLength={6}
        className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm text-center tracking-widest font-mono focus:outline-none focus:ring-2 focus:ring-blue-500"
        required
        autoFocus
        autoComplete="one-time-code"
      />
      <div className="flex gap-2">
        <button type="submit" disabled={loading || !code} className="flex-1 bg-blue-600 text-white py-2 rounded-lg text-sm font-semibold hover:bg-blue-700 disabled:opacity-50 transition-colors">
          {loading ? 'Regenerating…' : 'Regenerate codes'}
        </button>
        <button type="button" onClick={onDone} className="px-4 py-2 border border-gray-300 text-gray-600 rounded-lg text-sm hover:bg-gray-50 transition-colors">
          Cancel
        </button>
      </div>
    </form>
  )
}

// ─── Audit log section ────────────────────────────────────────────────────────

function eventBadge(event: string) {
  switch (event) {
    case 'LOGIN_SUCCESS':
      return <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-green-100 text-green-700">Login OK</span>
    case 'LOGIN_SUCCESS_BACKUP_CODE':
      return <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-green-100 text-green-700">Login OK (backup code)</span>
    case 'LOGIN_FAILED':
      return <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-red-100 text-red-700">Login failed</span>
    case 'LOGIN_RATE_LIMITED':
      return <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-orange-100 text-orange-700">Rate limited</span>
    case 'LOGIN_FAILED_TOTP':
      return <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-red-100 text-red-700">2FA failed</span>
    case 'LOGIN_FAILED_2FA_EXPIRED':
      return <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-orange-100 text-orange-700">2FA expired</span>
    default:
      return <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-gray-100 text-gray-600">{event}</span>
  }
}

function formatTime(iso: string) {
  try {
    return new Date(iso).toLocaleString(undefined, { dateStyle: 'short', timeStyle: 'medium' })
  } catch {
    return iso
  }
}

function AuditLogSection() {
  const { data: events, isLoading, error } = useQuery<AuditEvent[]>({
    queryKey: ['security-audit-log'],
    queryFn: securityApi.auditLog,
    refetchInterval: 15_000,
  })

  return (
    <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-6">
      <div className="flex items-center gap-2 mb-4">
        <Activity size={18} className="text-gray-400" />
        <h2 className="font-semibold text-gray-900">Auth event log</h2>
        <span className="ml-auto text-xs text-gray-400">last 200 events · auto-refreshes</span>
      </div>

      {isLoading && <p className="text-sm text-gray-400">Loading…</p>}
      {error && <p className="text-sm text-red-500">Failed to load logs</p>}
      {events && events.length === 0 && <p className="text-sm text-gray-400">No events yet.</p>}

      {events && events.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-gray-100">
                <th className="text-left py-2 pr-4 font-medium text-gray-500 text-xs">Time</th>
                <th className="text-left py-2 pr-4 font-medium text-gray-500 text-xs">Event</th>
                <th className="text-left py-2 font-medium text-gray-500 text-xs">IP</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {events.map((ev, i) => (
                <tr key={i} className="hover:bg-gray-50">
                  <td className="py-2 pr-4 text-gray-400 text-xs whitespace-nowrap font-mono">{formatTime(ev.time)}</td>
                  <td className="py-2 pr-4">{eventBadge(ev.event)}</td>
                  <td className="py-2 font-mono text-xs text-gray-600">{ev.ip || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

// ─── Fail2ban status section ──────────────────────────────────────────────────

function Fail2banSection() {
  const qc = useQueryClient()
  const { data, isLoading, error } = useQuery<Fail2banStatus>({
    queryKey: ['fail2ban-status'],
    queryFn: securityApi.fail2banStatus,
    refetchInterval: 30_000,
  })

  const unbanMutation = useMutation({
    mutationFn: ({ jail, ip }: { jail: string; ip: string }) => securityApi.unbanIP(jail, ip),
    onSuccess: () => {
      toast.success('IP unbanned')
      qc.invalidateQueries({ queryKey: ['fail2ban-status'] })
    },
    onError: () => toast.error('Failed to unban IP'),
  })

  if (isLoading) {
    return (
      <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-6">
        <h2 className="font-semibold text-gray-900 mb-2">Fail2ban</h2>
        <p className="text-sm text-gray-400">Loading…</p>
      </div>
    )
  }

  if (error || !data) {
    return (
      <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-6">
        <h2 className="font-semibold text-gray-900 mb-2">Fail2ban</h2>
        <p className="text-sm text-red-500">Failed to load fail2ban status</p>
      </div>
    )
  }

  if (!data.available) {
    return (
      <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-6">
        <h2 className="font-semibold text-gray-900 mb-1">Fail2ban</h2>
        <p className="text-sm text-gray-500">fail2ban is not installed. It is automatically installed during the local setup wizard.</p>
      </div>
    )
  }

  if (!data.running) {
    return (
      <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-6">
        <div className="flex items-center gap-2 mb-1">
          <AlertTriangle size={16} className="text-amber-500" />
          <h2 className="font-semibold text-gray-900">Fail2ban</h2>
        </div>
        <p className="text-sm text-amber-600">fail2ban is installed but could not be started. Check system logs for details.</p>
      </div>
    )
  }

  return (
    <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-6 space-y-5">
      <div className="flex items-center gap-2">
        <Ban size={18} className="text-gray-400" />
        <h2 className="font-semibold text-gray-900">Fail2ban</h2>
        <span className="ml-1 text-xs text-green-600 bg-green-50 border border-green-200 px-2 py-0.5 rounded-full">running</span>
      </div>

      {(!data.jails || data.jails.length === 0) && (
        <p className="text-sm text-gray-400">No jails active.</p>
      )}

      {data.jails && data.jails.map(jail => (
        <div key={jail.name} className="border border-gray-100 rounded-xl p-4 space-y-3">
          <div className="flex items-center justify-between">
            <div>
              <span className="font-medium text-gray-800 text-sm font-mono">{jail.name}</span>
              <div className="flex gap-4 mt-1 text-xs text-gray-500">
                <span>Currently failed: <strong className="text-gray-700">{jail.currently_failed}</strong></span>
                <span>Total failed: <strong className="text-gray-700">{jail.total_failed}</strong></span>
                <span>Banned: <strong className={jail.currently_banned > 0 ? 'text-red-600' : 'text-gray-700'}>{jail.currently_banned}</strong></span>
              </div>
            </div>
          </div>

          {jail.banned_ips && jail.banned_ips.length > 0 ? (
            <div className="space-y-1">
              <p className="text-xs font-medium text-gray-500 uppercase tracking-wide">Banned IPs</p>
              <div className="space-y-1">
                {jail.banned_ips.map(ip => (
                  <div key={ip} className="flex items-center justify-between bg-red-50 border border-red-100 rounded-lg px-3 py-1.5">
                    <span className="font-mono text-sm text-red-700">{ip}</span>
                    <button
                      onClick={() => unbanMutation.mutate({ jail: jail.name, ip })}
                      disabled={unbanMutation.isPending}
                      className="text-xs text-red-600 hover:text-red-800 font-medium hover:underline disabled:opacity-50"
                    >
                      Unban
                    </button>
                  </div>
                ))}
              </div>
            </div>
          ) : (
            <p className="text-xs text-gray-400">No IPs currently banned in this jail.</p>
          )}
        </div>
      ))}
    </div>
  )
}

// ─── Whitelist + config section ───────────────────────────────────────────────

function Fail2banConfigSection() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery<Fail2banConfig>({
    queryKey: ['fail2ban-config'],
    queryFn: securityApi.getFail2banConfig,
  })

  const [newIP, setNewIP] = useState('')
  const [cfgForm, setCfgForm] = useState<{ max_retry: string; find_time: string; ban_time: string } | null>(null)

  // Initialise form when data loads
  const form = cfgForm ?? (data ? {
    max_retry: String(data.max_retry),
    find_time: String(data.find_time),
    ban_time: String(data.ban_time),
  } : null)

  const addWhitelist = useMutation({
    mutationFn: () => securityApi.addWhitelistIP(newIP.trim()),
    onSuccess: () => {
      toast.success('IP added to whitelist')
      setNewIP('')
      qc.invalidateQueries({ queryKey: ['fail2ban-config'] })
    },
    onError: () => toast.error('Failed to add IP'),
  })

  const removeWhitelist = useMutation({
    mutationFn: (ip: string) => securityApi.removeWhitelistIP(ip),
    onSuccess: () => {
      toast.success('IP removed from whitelist')
      qc.invalidateQueries({ queryKey: ['fail2ban-config'] })
    },
    onError: () => toast.error('Failed to remove IP'),
  })

  const saveConfig = useMutation({
    mutationFn: () => {
      if (!form) throw new Error('no form')
      return securityApi.saveFail2banConfig({
        max_retry: parseInt(form.max_retry, 10),
        find_time: parseInt(form.find_time, 10),
        ban_time: parseInt(form.ban_time, 10),
      })
    },
    onSuccess: () => {
      toast.success('Fail2ban config saved')
      setCfgForm(null)
      qc.invalidateQueries({ queryKey: ['fail2ban-config'] })
      qc.invalidateQueries({ queryKey: ['fail2ban-status'] })
    },
    onError: () => toast.error('Failed to save config'),
  })

  if (isLoading || !data || !form) {
    return (
      <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-6">
        <h2 className="font-semibold text-gray-900 mb-2">Fail2ban configuration</h2>
        <p className="text-sm text-gray-400">{isLoading ? 'Loading…' : 'Not available'}</p>
      </div>
    )
  }

  const isDirty = form.max_retry !== String(data.max_retry)
    || form.find_time !== String(data.find_time)
    || form.ban_time !== String(data.ban_time)

  return (
    <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-6 space-y-6">
      <h2 className="font-semibold text-gray-900">Fail2ban configuration</h2>

      {/* Whitelist */}
      <div className="space-y-3">
        <div>
          <h3 className="text-sm font-medium text-gray-700">IP whitelist</h3>
          <p className="text-xs text-gray-400 mt-0.5">IPs in this list are never banned (ignoreip). Add your home/office IP to avoid locking yourself out.</p>
        </div>

        {data.ignore_ips && data.ignore_ips.length > 0 ? (
          <div className="space-y-1">
            {data.ignore_ips.map(ip => (
              <div key={ip} className="flex items-center justify-between bg-gray-50 border border-gray-200 rounded-lg px-3 py-1.5">
                <span className="font-mono text-sm text-gray-700">{ip}</span>
                <button
                  onClick={() => removeWhitelist.mutate(ip)}
                  disabled={removeWhitelist.isPending}
                  className="text-gray-400 hover:text-red-500 transition-colors disabled:opacity-40"
                  title="Remove"
                >
                  <Trash2 size={14} />
                </button>
              </div>
            ))}
          </div>
        ) : (
          <p className="text-xs text-gray-400">No IPs whitelisted.</p>
        )}

        <form
          onSubmit={e => { e.preventDefault(); addWhitelist.mutate() }}
          className="flex gap-2"
        >
          <input
            type="text"
            value={newIP}
            onChange={e => setNewIP(e.target.value)}
            placeholder="1.2.3.4 or 10.0.0.0/8"
            className="flex-1 border border-gray-300 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
          />
          <button
            type="submit"
            disabled={!newIP.trim() || addWhitelist.isPending}
            className="flex items-center gap-1.5 px-3 py-2 bg-blue-600 text-white rounded-lg text-sm font-semibold hover:bg-blue-700 disabled:opacity-50 transition-colors"
          >
            <Plus size={14} /> Add
          </button>
        </form>
      </div>

      <div className="border-t border-gray-100" />

      {/* Thresholds */}
      <div className="space-y-4">
        <div>
          <h3 className="text-sm font-medium text-gray-700">Ban thresholds</h3>
          <p className="text-xs text-gray-400 mt-0.5">Changes are written to <code className="font-mono">/etc/fail2ban/jail.d/gopher.conf</code> and fail2ban is reloaded.</p>
        </div>

        <div className="grid grid-cols-3 gap-4">
          <div>
            <label className="block text-xs font-medium text-gray-600 mb-1">Max retries</label>
            <input
              type="number"
              min={1}
              value={form.max_retry}
              onChange={e => setCfgForm({ ...form, max_retry: e.target.value })}
              className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
            />
            <p className="text-xs text-gray-400 mt-1">failures before ban</p>
          </div>
          <div>
            <label className="block text-xs font-medium text-gray-600 mb-1">Find time (s)</label>
            <input
              type="number"
              min={10}
              value={form.find_time}
              onChange={e => setCfgForm({ ...form, find_time: e.target.value })}
              className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
            />
            <p className="text-xs text-gray-400 mt-1">window to count failures</p>
          </div>
          <div>
            <label className="block text-xs font-medium text-gray-600 mb-1">Ban time (s)</label>
            <input
              type="number"
              min={10}
              value={form.ban_time}
              onChange={e => setCfgForm({ ...form, ban_time: e.target.value })}
              className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
            />
            <p className="text-xs text-gray-400 mt-1">-1 = permanent</p>
          </div>
        </div>

        {isDirty && (
          <div className="flex justify-end">
            <button
              onClick={() => saveConfig.mutate()}
              disabled={saveConfig.isPending}
              className="px-4 py-2 bg-blue-600 text-white rounded-lg text-sm font-semibold hover:bg-blue-700 disabled:opacity-50 transition-colors"
            >
              {saveConfig.isPending ? 'Saving…' : 'Save changes'}
            </button>
          </div>
        )}
      </div>
    </div>
  )
}

// ─── Main page ────────────────────────────────────────────────────────────────

type Panel = 'none' | 'enroll' | 'disable' | 'regenerate'

export default function SecurityPage() {
  const qc = useQueryClient()
  const { data: status, isLoading } = useTOTPStatus()
  const [panel, setPanel] = useState<Panel>('none')

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ['totp-status'] })
    setPanel('none')
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold text-gray-900">Security</h1>
        <p className="text-sm text-gray-500 mt-1">Authentication, access control, and intrusion monitoring.</p>
      </div>

      {/* 2FA card */}
      <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-6 space-y-5">
        <div className="flex items-start justify-between">
          <div className="flex items-center gap-3">
            {status?.enabled
              ? <ShieldCheck size={22} className="text-green-500 shrink-0" />
              : <ShieldOff size={22} className="text-gray-400 shrink-0" />}
            <div>
              <h2 className="font-semibold text-gray-900">Two-factor authentication</h2>
              <p className="text-xs text-gray-500 mt-0.5">
                {isLoading ? '…' : status?.enabled
                  ? `Enabled — ${status.backup_codes_remaining} backup code${status.backup_codes_remaining !== 1 ? 's' : ''} remaining`
                  : 'Disabled — only your password is required to sign in'}
              </p>
            </div>
          </div>

          {!isLoading && panel === 'none' && (
            <div className="flex gap-2 shrink-0">
              {status?.enabled ? (
                <>
                  <button
                    onClick={() => setPanel('regenerate')}
                    className="flex items-center gap-1.5 px-3 py-1.5 border border-gray-300 text-gray-600 rounded-lg text-xs font-medium hover:bg-gray-50 transition-colors"
                  >
                    <RefreshCw size={13} /> Backup codes
                  </button>
                  <button
                    onClick={() => setPanel('disable')}
                    className="flex items-center gap-1.5 px-3 py-1.5 border border-red-200 text-red-600 rounded-lg text-xs font-medium hover:bg-red-50 transition-colors"
                  >
                    <ShieldOff size={13} /> Disable
                  </button>
                </>
              ) : (
                <button
                  onClick={() => setPanel('enroll')}
                  className="flex items-center gap-1.5 px-3 py-1.5 bg-blue-600 text-white rounded-lg text-xs font-semibold hover:bg-blue-700 transition-colors"
                >
                  <ShieldCheck size={13} /> Enable 2FA
                </button>
              )}
            </div>
          )}
        </div>

        {panel === 'enroll' && <EnrollPanel onDone={invalidate} />}
        {panel === 'disable' && <DisablePanel onDone={invalidate} />}
        {panel === 'regenerate' && <RegeneratePanel onDone={invalidate} />}
      </div>

      {/* Rate limiting info */}
      <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-6">
        <h2 className="font-semibold text-gray-900 mb-1">Login protection</h2>
        <p className="text-sm text-gray-500">
          Login attempts are rate-limited to <strong>10 per IP per 5 minutes</strong>. The counter resets on
          successful login. Repeated failures are logged and picked up by <strong>fail2ban</strong> for
          automatic IP banning.
        </p>
      </div>

      {/* Auth event log */}
      <AuditLogSection />

      {/* Fail2ban live status */}
      <Fail2banSection />

      {/* Whitelist + config */}
      <Fail2banConfigSection />
    </div>
  )
}
