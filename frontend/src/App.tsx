import { BrowserRouter, Routes, Route, NavLink, Navigate, useLocation } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { LayoutDashboard, Server, Monitor, Network, LogOut, Map, RefreshCw, Key, Shield, ShieldCheck, ChevronDown, Activity } from 'lucide-react'
import { useState, useEffect, useCallback, useRef } from 'react'
import ToastContainer from './components/ToastContainer'
import AgentMigrationBanner from './components/AgentMigrationBanner'
import CustomServicesWarningBanner from './components/CustomServicesWarningBanner'
import DashboardPage from './pages/DashboardPage'
import VPSPage from './pages/VPSPage' // repurposed as Server Info page
import MachinesPage from './pages/MachinesPage'
import TunnelsPage from './pages/TunnelsPage'
import NetworkMapPage from './pages/NetworkMapPage'
import SSHKeysPage from './pages/SSHKeysPage'
import FirewallPage from './pages/FirewallPage'
import SecurityPage from './pages/SecurityPage'
import LogsPage from './pages/LogsPage'
import SetupPage from './pages/SetupPage'
import LoginPage from './pages/LoginPage'
import { AuthProvider, useAuth } from './lib/auth'
import { toast } from './lib/toast'
import client from './api/client'
import { updateApi, type UpdateInfo } from './api/update'

const queryClient = new QueryClient({ defaultOptions: { queries: { retry: 1 } } })

const navClass = ({ isActive }: { isActive: boolean }) =>
  `flex items-center gap-1.5 px-3 py-2 rounded-lg text-sm font-medium transition-colors ${
    isActive ? 'bg-blue-50 text-blue-700' : 'text-gray-600 hover:text-gray-900 hover:bg-gray-100'
  }`

function NavDropdown({ label, icon: Icon, items }: {
  label: string
  icon: React.ElementType
  items: { to: string; icon: React.ElementType; label: string }[]
}) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  const location = useLocation()
  const isActive = items.some(i => location.pathname.startsWith(i.to))

  return (
    <div ref={ref} className="relative" onMouseEnter={() => setOpen(true)} onMouseLeave={() => setOpen(false)}>
      <button
        className={`flex items-center gap-1.5 px-3 py-2 rounded-lg text-sm font-medium transition-colors ${
          isActive ? 'bg-blue-50 text-blue-700' : 'text-gray-600 hover:text-gray-900 hover:bg-gray-100'
        }`}
      >
        <Icon size={16} /> {label} <ChevronDown size={13} className={`transition-transform ${open ? 'rotate-180' : ''}`} />
      </button>
      {open && (
        <div className="absolute top-full left-0 mt-0 pt-1 bg-transparent z-50">
          <div className="bg-white border border-gray-200 rounded-xl shadow-lg py-1 min-w-[160px]">
            {items.map(item => (
              <NavLink
                key={item.to}
                to={item.to}
                onClick={() => setOpen(false)}
                className={({ isActive }) =>
                  `flex items-center gap-2 px-4 py-2 text-sm font-medium transition-colors ${
                    isActive ? 'text-blue-700 bg-blue-50' : 'text-gray-600 hover:text-gray-900 hover:bg-gray-50'
                  }`
                }
              >
                <item.icon size={15} /> {item.label}
              </NavLink>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

function AppShell() {
  const { isLoading, isSetup, isAuthenticated, localSetupDone, firewallConfigured, sshKeyConfigured, fail2banSetupDone, refetch } = useAuth()
  const [updateInfo, setUpdateInfo] = useState<UpdateInfo | null>(null)
  const [isUpdating, setIsUpdating] = useState(false)

  useEffect(() => {
    if (!isAuthenticated) return
    updateApi.check().then(setUpdateInfo).catch(() => {})
  }, [isAuthenticated])

  const handleUpdate = useCallback(async () => {
    setIsUpdating(true)
    try {
      await updateApi.apply()
      toast.info('Update applied — waiting for server to restart…')
      // Wait for the service to go down, then come back up
      await new Promise(r => setTimeout(r, 2000))
      for (let i = 0; i < 60; i++) {
        try {
          const r = await fetch('/api/status')
          if (r.ok) { window.location.reload(); return }
        } catch { /* server still restarting */ }
        await new Promise(r => setTimeout(r, 1000))
      }
      toast.error('Server did not restart in time — please refresh manually')
    } catch {
      toast.error('Update failed')
    }
    setIsUpdating(false)
  }, [])

  if (isLoading) {
    return (
      <div className="min-h-screen bg-gray-50 flex items-center justify-center">
        <span className="text-gray-400 text-sm">Loading…</span>
      </div>
    )
  }

  if (!isSetup) return <SetupPage initialStep={1} />
  if (!isAuthenticated) return <LoginPage />
  if (!localSetupDone) return <SetupPage initialStep={2} />
  if (!firewallConfigured) return <SetupPage initialStep={3} />
  if (!sshKeyConfigured) return <SetupPage initialStep={4} />
  if (!fail2banSetupDone) return <SetupPage initialStep={5} />

  const handleLogout = async () => {
    await client.post('/auth/logout').catch(() => {})
    await refetch()
  }

  return (
    <div className="min-h-screen bg-gray-50">
      <nav className="bg-white shadow-sm border-b sticky top-0 z-40">
        <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
          <div className="flex h-16 items-center justify-between">
            <div className="flex items-center gap-6">
              <img src="/gopher_banner.png" alt="Gopher" className="h-8 w-auto" />
              <div className="flex gap-1">
                <NavLink to="/" end className={navClass}><LayoutDashboard size={16} /> Dashboard</NavLink>
                <NavDropdown
                  label="Infrastructure"
                  icon={Server}
                  items={[
                    { to: '/vps', icon: Server, label: 'Server' },
                    { to: '/machines', icon: Monitor, label: 'Machines' },
                    { to: '/tunnels', icon: Network, label: 'Tunnels' },
                    { to: '/keys', icon: Key, label: 'SSH Keys' },
                  ]}
                />
                <NavDropdown
                  label="Security"
                  icon={ShieldCheck}
                  items={[
                    { to: '/firewall', icon: Shield, label: 'Firewall' },
                    { to: '/security', icon: ShieldCheck, label: 'Access Control' },
                    { to: '/logs', icon: Activity, label: 'Logs' },
                  ]}
                />
                <NavLink to="/network" className={navClass}><Map size={16} /> Network Map</NavLink>

              </div>
            </div>
            <div className="flex items-center gap-2">
              {updateInfo?.update_available && (
                <button
                  onClick={handleUpdate}
                  disabled={isUpdating}
                  title={`Update to ${updateInfo.latest_version}`}
                  className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium text-emerald-700 bg-emerald-50 hover:bg-emerald-100 border border-emerald-200 transition-colors disabled:opacity-60 disabled:cursor-not-allowed"
                >
                  <RefreshCw size={13} className={isUpdating ? 'animate-spin' : ''} />
                  {isUpdating ? 'Updating…' : `Update ${updateInfo.latest_version}`}
                </button>
              )}
              <button
                onClick={handleLogout}
                className="flex items-center gap-1.5 px-3 py-2 rounded-lg text-sm font-medium text-gray-600 hover:text-gray-900 hover:bg-gray-100 transition-colors"
              >
                <LogOut size={16} /> Sign out
              </button>
            </div>
          </div>
        </div>
      </nav>
      <main className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
        <AgentMigrationBanner />
        <CustomServicesWarningBanner />
        <Routes>
          <Route path="/" element={<DashboardPage />} />
          <Route path="/vps" element={<VPSPage />} />
          <Route path="/machines" element={<MachinesPage />} />
          <Route path="/tunnels" element={<TunnelsPage />} />
          <Route path="/network" element={<NetworkMapPage />} />
          <Route path="/keys" element={<SSHKeysPage />} />
          <Route path="/firewall" element={<FirewallPage />} />
          <Route path="/security" element={<SecurityPage />} />
          <Route path="/logs" element={<LogsPage />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </main>
    </div>
  )
}

function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <AuthProvider>
          <AppShell />
          <ToastContainer />
        </AuthProvider>
      </BrowserRouter>
    </QueryClientProvider>
  )
}

export default App
