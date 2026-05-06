import { useQuery } from '@tanstack/react-query'
import { tunnelsApi } from '../api/tunnels'
import { relativeTime } from '../lib/time'
import Sparkline from './Sparkline'

// Compact uptime + sparkline for the Tunnels page table cell. Each tunnel
// fetches its own /health endpoint — small enough that a per-row query is
// acceptable; cached and refreshed at the page cadence.
//
// Renders three things stacked tight:
//   1. Uptime % (24h rolling)
//   2. Tiny sparkline of the most recent 30 checks
//   3. Relative-time "last checked" label

interface Props {
  tunnelId: string
}

export default function TunnelHealthCell({ tunnelId }: Props) {
  const { data, isLoading } = useQuery({
    queryKey: ['tunnel-health', tunnelId],
    queryFn: () => tunnelsApi.health(tunnelId),
    refetchInterval: 30_000,
    // Don't retry aggressively on a single tunnel's failure; let the
    // page-level retry budget handle network blips.
    retry: 1,
  })

  if (isLoading) {
    return <span className="text-xs text-gray-300">…</span>
  }
  if (!data || data.total_checks === 0) {
    return <span className="text-xs text-gray-400">—</span>
  }

  const pct = data.uptime_percent
  const colorClass =
    pct == null ? 'text-gray-400'
      : pct >= 99 ? 'text-green-600'
      : pct >= 95 ? 'text-amber-600'
      : 'text-red-600'

  return (
    <div className="flex flex-col gap-0.5 min-w-[80px]">
      <div className="flex items-baseline gap-1.5">
        <span className={`text-xs font-semibold tabular-nums ${colorClass}`}>
          {pct != null ? `${pct}%` : '—'}
        </span>
        <span className="text-[10px] text-gray-400">24h</span>
      </div>
      <Sparkline checks={data.recent} width={70} height={10} title={`${data.ok_checks}/${data.total_checks} ok`} />
      {data.latest && (
        <span className="text-[10px] text-gray-400" title={new Date(data.latest.checked_at).toLocaleString()}>
          {relativeTime(data.latest.checked_at)}
        </span>
      )}
    </div>
  )
}
