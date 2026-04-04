import { createContext, useContext, useEffect, useState } from 'react'
import client from '../api/client'

interface AuthState {
  isLoading: boolean
  isSetup: boolean
  isAuthenticated: boolean
  localSetupDone: boolean
  firewallConfigured: boolean
  refetch: () => void
}

const AuthContext = createContext<AuthState>({
  isLoading: true,
  isSetup: false,
  isAuthenticated: false,
  localSetupDone: false,
  firewallConfigured: true,
  refetch: () => {},
})

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [isLoading, setIsLoading] = useState(true)
  const [isSetup, setIsSetup] = useState(false)
  const [isAuthenticated, setIsAuthenticated] = useState(false)
  const [localSetupDone, setLocalSetupDone] = useState(false)
  const [firewallConfigured, setFirewallConfigured] = useState(true)

  const fetchStatus = async () => {
    setIsLoading(true)
    try {
      const [authRes, localRes] = await Promise.all([
        client.get<{ data: { setup: boolean; authenticated: boolean } }>('/auth/status'),
        client.get<{ data: { local_setup_done: boolean; firewall_mode: string } }>('/local/status').catch(() => ({ data: { data: { local_setup_done: false, firewall_mode: 'none' } } })),
      ])
      setIsSetup(authRes.data.data.setup)
      setIsAuthenticated(authRes.data.data.authenticated)
      setLocalSetupDone(localRes.data.data.local_setup_done)
      setFirewallConfigured(localRes.data.data.firewall_mode !== '')
    } catch {
      setIsSetup(false)
      setIsAuthenticated(false)
      setLocalSetupDone(false)
      setFirewallConfigured(true)
    } finally {
      setIsLoading(false)
    }
  }

  useEffect(() => { fetchStatus() }, [])

  return (
    <AuthContext.Provider value={{ isLoading, isSetup, isAuthenticated, localSetupDone, firewallConfigured, refetch: fetchStatus }}>
      {children}
    </AuthContext.Provider>
  )
}

// eslint-disable-next-line react-refresh/only-export-components
export function useAuth() {
  return useContext(AuthContext)
}
