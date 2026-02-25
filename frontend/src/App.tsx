import { BrowserRouter, Routes, Route, NavLink } from 'react-router-dom'
import DashboardPage from './pages/DashboardPage'
import VPSPage from './pages/VPSPage'
import MachinesPage from './pages/MachinesPage'
import TunnelsPage from './pages/TunnelsPage'

function App() {
  return (
    <BrowserRouter>
      <div className="min-h-screen bg-gray-50">
        <nav className="bg-white shadow-sm border-b">
          <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
            <div className="flex h-16 items-center justify-between">
              <div className="flex items-center gap-8">
                <span className="text-xl font-bold text-blue-600">🐹 Gopher</span>
                <div className="flex gap-4">
                  <NavLink to="/" end className={({ isActive }) => isActive ? 'text-blue-600 font-medium' : 'text-gray-600 hover:text-gray-900'}>Dashboard</NavLink>
                  <NavLink to="/vps" className={({ isActive }) => isActive ? 'text-blue-600 font-medium' : 'text-gray-600 hover:text-gray-900'}>VPS</NavLink>
                  <NavLink to="/machines" className={({ isActive }) => isActive ? 'text-blue-600 font-medium' : 'text-gray-600 hover:text-gray-900'}>Machines</NavLink>
                  <NavLink to="/tunnels" className={({ isActive }) => isActive ? 'text-blue-600 font-medium' : 'text-gray-600 hover:text-gray-900'}>Tunnels</NavLink>
                </div>
              </div>
            </div>
          </div>
        </nav>
        <main className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
          <Routes>
            <Route path="/" element={<DashboardPage />} />
            <Route path="/vps" element={<VPSPage />} />
            <Route path="/machines" element={<MachinesPage />} />
            <Route path="/tunnels" element={<TunnelsPage />} />
          </Routes>
        </main>
      </div>
    </BrowserRouter>
  )
}

export default App
