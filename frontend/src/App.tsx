import { BrowserRouter, Routes, Route, NavLink, Navigate } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { LayoutDashboard, Server, Monitor, Network, Activity, LogOut } from 'lucide-react'
import ToastContainer from './components/ToastContainer'
import DashboardPage from './pages/DashboardPage'
import VPSPage from './pages/VPSPage' // repurposed as Server Info page
import MachinesPage from './pages/MachinesPage'
import TunnelsPage from './pages/TunnelsPage'
import StatusPage from './pages/StatusPage'
import SetupPage from './pages/SetupPage'
import LoginPage from './pages/LoginPage'
import { AuthProvider, useAuth } from './lib/auth'
import client from './api/client'

const queryClient = new QueryClient({ defaultOptions: { queries: { retry: 1 } } })

const navClass = ({ isActive }: { isActive: boolean }) =>
  `flex items-center gap-1.5 px-3 py-2 rounded-lg text-sm font-medium transition-colors ${
    isActive ? 'bg-blue-50 text-blue-700' : 'text-gray-600 hover:text-gray-900 hover:bg-gray-100'
  }`

function AppShell() {
  const { isLoading, isSetup, isAuthenticated, refetch } = useAuth()

  if (isLoading) {
    return (
      <div className="min-h-screen bg-gray-50 flex items-center justify-center">
        <span className="text-gray-400 text-sm">Loading…</span>
      </div>
    )
  }

  if (!isSetup) return <SetupPage />
  if (!isAuthenticated) return <LoginPage />

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
              <span className="text-xl font-bold text-blue-600 flex items-center gap-2">🐹 Gopher</span>
              <div className="flex gap-1">
                <NavLink to="/" end className={navClass}><LayoutDashboard size={16} /> Dashboard</NavLink>
                <NavLink to="/vps" className={navClass}><Server size={16} /> Server</NavLink>
                <NavLink to="/machines" className={navClass}><Monitor size={16} /> Machines</NavLink>
                <NavLink to="/tunnels" className={navClass}><Network size={16} /> Tunnels</NavLink>
                <NavLink to="/status" className={navClass}><Activity size={16} /> Status</NavLink>
              </div>
            </div>
            <button
              onClick={handleLogout}
              className="flex items-center gap-1.5 px-3 py-2 rounded-lg text-sm font-medium text-gray-600 hover:text-gray-900 hover:bg-gray-100 transition-colors"
            >
              <LogOut size={16} /> Sign out
            </button>
          </div>
        </div>
      </nav>
      <main className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
        <Routes>
          <Route path="/" element={<DashboardPage />} />
          <Route path="/vps" element={<VPSPage />} />
          <Route path="/machines" element={<MachinesPage />} />
          <Route path="/tunnels" element={<TunnelsPage />} />
          <Route path="/status" element={<StatusPage />} />
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
