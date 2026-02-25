import { useQuery } from '@tanstack/react-query'
import client from '../api/client'
import type { StatusData, ApiResponse } from '../types'

export default function DashboardPage() {
  const { data } = useQuery({
    queryKey: ['status'],
    queryFn: () => client.get<ApiResponse<StatusData>>('/status').then(r => r.data.data),
  })

  return (
    <div>
      <h1 className="text-2xl font-bold mb-6">Dashboard</h1>
      <div className="grid grid-cols-1 md:grid-cols-3 gap-6">
        <div className="bg-white rounded-lg shadow p-6">
          <h2 className="text-sm font-medium text-gray-500">VPS</h2>
          <p className="text-3xl font-bold mt-2">{data?.vps ? '✅ Connected' : '❌ Not configured'}</p>
        </div>
        <div className="bg-white rounded-lg shadow p-6">
          <h2 className="text-sm font-medium text-gray-500">Machines</h2>
          <p className="text-3xl font-bold mt-2">{data?.machines ?? 0}</p>
        </div>
        <div className="bg-white rounded-lg shadow p-6">
          <h2 className="text-sm font-medium text-gray-500">Tunnels</h2>
          <p className="text-3xl font-bold mt-2">{data?.tunnels ?? 0}</p>
        </div>
      </div>
    </div>
  )
}
