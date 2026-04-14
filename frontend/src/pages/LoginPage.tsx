import { useRef, useState } from 'react'
import { Eye, EyeOff, ShieldCheck } from 'lucide-react'
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
    <div className="min-h-screen bg-gray-50 flex items-center justify-center px-4">
      <div className="w-full max-w-sm">
        <div className="text-center mb-8">
          <span className="text-5xl">🐹</span>
          <h1 className="mt-4 text-3xl font-bold text-gray-900">Gopher</h1>
          <p className="mt-2 text-gray-500">Sign in to continue</p>
        </div>

        <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-8">
          {!needsTOTP ? (
            <form onSubmit={handlePasswordSubmit} className="space-y-5">
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Admin password</label>
                <div className="relative">
                  <input
                    type={showPw ? 'text' : 'password'}
                    value={password}
                    onChange={e => setPassword(e.target.value)}
                    placeholder="Enter your password"
                    className="w-full border border-gray-300 rounded-lg px-3 py-2 pr-10 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
                    required
                    autoFocus
                  />
                  <button
                    type="button"
                    className="absolute right-2.5 top-2.5 text-gray-400 hover:text-gray-600"
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
                className="w-full bg-blue-600 text-white py-2.5 rounded-lg text-sm font-semibold hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
              >
                {loading ? 'Signing in…' : 'Sign in'}
              </button>
            </form>
          ) : (
            <form onSubmit={handleTOTPSubmit} className="space-y-5">
              <div className="flex items-center gap-2 text-blue-600 mb-2">
                <ShieldCheck size={18} />
                <span className="font-semibold text-sm">Two-factor authentication</span>
              </div>
              <p className="text-sm text-gray-500">
                Enter the 6-digit code from your authenticator app, or one of your backup codes.
              </p>
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Authentication code</label>
                <input
                  ref={totpRef}
                  type="text"
                  inputMode="numeric"
                  value={totpCode}
                  onChange={e => setTotpCode(e.target.value.replace(/\s/g, ''))}
                  placeholder="000000"
                  maxLength={10}
                  className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm text-center tracking-widest font-mono focus:outline-none focus:ring-2 focus:ring-blue-500"
                  required
                  autoComplete="one-time-code"
                />
              </div>
              <button
                type="submit"
                disabled={loading || !totpCode}
                className="w-full bg-blue-600 text-white py-2.5 rounded-lg text-sm font-semibold hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
              >
                {loading ? 'Verifying…' : 'Verify'}
              </button>
              <button
                type="button"
                onClick={() => { setNeedsTOTP(false); setPendingToken(''); setTotpCode('') }}
                className="w-full text-sm text-gray-500 hover:text-gray-700"
              >
                ← Back to password
              </button>
            </form>
          )}
        </div>
      </div>
    </div>
  )
}
