import { useState } from 'react'
import { Lock, Eye, EyeOff } from 'lucide-react'
import client from '../api/client'
import { useAuth } from '../lib/auth'
import { toast } from '../lib/toast'

export default function SetupPage() {
  const { refetch } = useAuth()
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [showPw, setShowPw] = useState(false)
  const [loading, setLoading] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (password.length < 8) {
      toast.error('Password must be at least 8 characters')
      return
    }
    if (password !== confirm) {
      toast.error('Passwords do not match')
      return
    }
    setLoading(true)
    try {
      await client.post('/auth/setup', { password })
      toast.success('Admin account created — welcome to Gopher!')
      await refetch()
    } catch (err: any) {
      toast.error(err.response?.data?.error ?? 'Setup failed')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen bg-gray-50 flex items-center justify-center px-4">
      <div className="w-full max-w-md">
        <div className="text-center mb-8">
          <span className="text-5xl">🐹</span>
          <h1 className="mt-4 text-3xl font-bold text-gray-900">Welcome to Gopher</h1>
          <p className="mt-2 text-gray-500">Create an admin password to get started</p>
        </div>

        <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-8">
          <div className="flex items-center gap-2 mb-6 text-blue-600">
            <Lock size={18} />
            <span className="font-semibold text-sm uppercase tracking-wide">First-time setup</span>
          </div>

          <form onSubmit={handleSubmit} className="space-y-5">
            <div>
              <label className="block text-sm font-medium text-gray-700 mb-1">Admin password</label>
              <div className="relative">
                <input
                  type={showPw ? 'text' : 'password'}
                  value={password}
                  onChange={e => setPassword(e.target.value)}
                  placeholder="Minimum 8 characters"
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

            <button
              type="submit"
              disabled={loading || !password || !confirm}
              className="w-full bg-blue-600 text-white py-2.5 rounded-lg text-sm font-semibold hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
            >
              {loading ? 'Setting up…' : 'Create admin account'}
            </button>
          </form>
        </div>
      </div>
    </div>
  )
}
