import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { ShieldAlert } from 'lucide-react'
import { machinesApi } from '../api/machines'

// AgentMigrationBanner appears on every page (rendered inside AppShell) when
// at least one machine still needs the gopher-agent rolled out. Clicking it
// lands on /machines, where each machine has an "Install agent" button.
//
// Polls every 60s — the cost of being slightly stale is just a brief banner
// after a successful install.
export default function AgentMigrationBanner() {
  const { data } = useQuery({
    queryKey: ['agent-pending'],
    queryFn: machinesApi.pendingAgents,
    refetchInterval: 60_000,
    // Fail silently — the banner is opportunistic, not load-bearing.
    retry: false,
  })

  const pending = data?.data ?? []
  if (pending.length === 0) return null

  return (
    <div className="mb-6 bg-amber-50 border border-amber-200 rounded-xl px-4 py-3 flex items-center justify-between gap-4">
      <div className="flex items-start gap-3 min-w-0">
        <ShieldAlert size={18} className="text-amber-600 mt-0.5 shrink-0" />
        <div className="min-w-0">
          <div className="text-sm font-semibold text-amber-900">
            {pending.length} machine{pending.length === 1 ? '' : 's'} need{pending.length === 1 ? 's' : ''} the gopher-agent rolled out
          </div>
          <p className="text-xs text-amber-800 mt-0.5">
            Without the agent, Gopher falls back to a TCP probe for health and can't auto-restart rathole on failure.
            Health monitoring + auto-recovery only run on machines with the agent installed.
          </p>
        </div>
      </div>
      <Link
        to="/machines"
        className="shrink-0 px-3 py-1.5 bg-amber-600 hover:bg-amber-700 text-white text-xs font-semibold rounded-lg transition-colors"
      >
        Open Machines →
      </Link>
    </div>
  )
}
