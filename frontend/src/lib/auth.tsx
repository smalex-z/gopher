import { createContext, useContext, useEffect, useState } from 'react'
import client from '../api/client'

interface AuthState {
  isLoading: boolean
  isSetup: boolean
  isAuthenticated: boolean
  refetch: () => void
}

const AuthContext = createContext<AuthState>({
  isLoading: true,
  isSetup: false,
  isAuthenticated: false,
  refetch: () => {},
})

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [isLoading, setIsLoading] = useState(true)
  const [isSetup, setIsSetup] = useState(false)
  const [isAuthenticated, setIsAuthenticated] = useState(false)

  const fetchStatus = async () => {
    setIsLoading(true)
    try {
      const res = await client.get<{ data: { setup: boolean; authenticated: boolean } }>('/auth/status')
      setIsSetup(res.data.data.setup)
      setIsAuthenticated(res.data.data.authenticated)
    } catch {
      setIsSetup(false)
      setIsAuthenticated(false)
    } finally {
      setIsLoading(false)
    }
  }

  useEffect(() => { fetchStatus() }, [])

  return (
    <AuthContext.Provider value={{ isLoading, isSetup, isAuthenticated, refetch: fetchStatus }}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth() {
  return useContext(AuthContext)
}
