import { useRef, useState } from 'react'
import { Eye, EyeOff, ShieldCheck, Lock } from 'lucide-react'
import client from '../api/client'
import { useAuth } from '../lib/auth'
import { toast } from '../lib/toast'

export default function LoginPage() {
  const { refetch } = useAuth()
  const [password, setPassword] = useState('')
  const [showPw, setShowPw] = useState(false)
  const [loading, setLoading] = useState(false)

  // 2FA step
  const [needsTOTP, setNeedsTOTP] = useState(false)
  const [pendingToken, setPendingToken] = useState('')
  const [totpCode, setTotpCode] = useState('')
  const totpRef = useRef<HTMLInputElement>(null)

  const handlePasswordSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setLoading(true)
    try {
      const res = await client.post<{ data: { needs_2fa?: boolean; pending_token?: string; message?: string } }>(
        '/auth/login', { password }
      )
      if (res.data.data.needs_2fa) {
        setPendingToken(res.data.data.pending_token ?? '')
        setNeedsTOTP(true)
        setTimeout(() => totpRef.current?.focus(), 50)
      } else {
        await refetch()
      }
    } catch (err: unknown) {
      const status = (err as { response?: { status?: number } }).response?.status
      if (status === 429) {
        toast.error('Too many attempts — please wait before trying again')
      } else {
        toast.error('Invalid password')
      }
      setPassword('')
    } finally {
      setLoading(false)
    }
  }

  const handleTOTPSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setLoading(true)
    try {
      await client.post('/auth/login/2fa', { pending_token: pendingToken, code: totpCode })
      await refetch()
    } catch {
      toast.error('Invalid code')
      setTotpCode('')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen bg-gradient-to-b from-gray-50 to-gray-100 flex items-center justify-center px-4">
      <div className="w-full max-w-sm">
        <div className="bg-white rounded-2xl shadow-xl ring-1 ring-gray-200/70 p-8">
          <div className="text-center mb-8">
            <img src="/gopher_banner.png" alt="Gopher" className="mx-auto h-14 w-auto" />
            <h1 className="mt-6 text-xl font-semibold text-gray-900">Welcome back</h1>
            <p className="mt-1 text-sm text-gray-500">Sign in to your Gopher dashboard</p>
          </div>
          {!needsTOTP ? (
            <form onSubmit={handlePasswordSubmit} className="space-y-5">
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1.5">Admin password</label>
                <div className="relative">
                  <Lock size={16} className="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
                  <input
                    type={showPw ? 'text' : 'password'}
                    value={password}
                    onChange={e => setPassword(e.target.value)}
                    placeholder="Enter your password"
                    className="w-full rounded-xl border border-gray-300 bg-gray-50 pl-10 pr-10 py-2.5 text-sm text-gray-900 placeholder:text-gray-400 transition focus:border-blue-500 focus:bg-white focus:outline-none focus:ring-2 focus:ring-blue-500/30"
                    required
                    autoFocus
                  />
                  <button
                    type="button"
                    className="absolute right-3 top-1/2 -translate-y-1/2 text-gray-400 hover:text-gray-600"
                    onClick={() => setShowPw(p => !p)}
                    tabIndex={-1}
                  >
                    {showPw ? <EyeOff size={16} /> : <Eye size={16} />}
                  </button>
                </div>
              </div>
              <button
                type="submit"
                disabled={loading || !password}
                className="w-full rounded-xl bg-blue-600 py-2.5 text-sm font-semibold text-white shadow-sm shadow-blue-600/20 transition hover:bg-blue-700 hover:shadow-blue-600/30 disabled:cursor-not-allowed disabled:opacity-50 disabled:shadow-none"
              >
                {loading ? 'Signing in…' : 'Sign in'}
              </button>
            </form>
          ) : (
            <form onSubmit={handleTOTPSubmit} className="space-y-5">
              <div className="flex items-center gap-2 text-blue-600">
                <ShieldCheck size={18} />
                <span className="font-semibold text-sm">Two-factor authentication</span>
              </div>
              <p className="text-sm text-gray-500">
                Enter the 6-digit code from your authenticator app, or one of your backup codes.
              </p>
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1.5">Authentication code</label>
                <input
                  ref={totpRef}
                  type="text"
                  inputMode="numeric"
                  value={totpCode}
                  onChange={e => setTotpCode(e.target.value.replace(/\s/g, ''))}
                  placeholder="000000"
                  maxLength={10}
                  className="w-full rounded-xl border border-gray-300 bg-gray-50 px-3 py-2.5 text-center text-lg font-mono tracking-[0.4em] text-gray-900 placeholder:text-gray-300 transition focus:border-blue-500 focus:bg-white focus:outline-none focus:ring-2 focus:ring-blue-500/30"
                  required
                  autoComplete="one-time-code"
                />
              </div>
              <button
                type="submit"
                disabled={loading || !totpCode}
                className="w-full rounded-xl bg-blue-600 py-2.5 text-sm font-semibold text-white shadow-sm shadow-blue-600/20 transition hover:bg-blue-700 hover:shadow-blue-600/30 disabled:cursor-not-allowed disabled:opacity-50 disabled:shadow-none"
              >
                {loading ? 'Verifying…' : 'Verify'}
              </button>
              <button
                type="button"
                onClick={() => { setNeedsTOTP(false); setPendingToken(''); setTotpCode('') }}
                className="w-full text-sm text-gray-500 hover:text-gray-700 transition"
              >
                ← Back to password
              </button>
            </form>
          )}
        </div>
        <p className="mt-6 text-center text-xs text-gray-400">Self-hosted · Open source</p>
      </div>
    </div>
  )
}
