import { useQuery } from '@tanstack/react-query'
import { tunnelsApi } from '../api/tunnels'
import { relativeTime } from '../lib/time'

// Compact uptime cell for the Tunnels page.
//
// Visible: a single colored percentage. Green ≥99, amber ≥95, red below.
// One line of text, no sparkline. The full-fidelity sparkline lives on
// the Machines page health panel where there's vertical space for it.
//
// Hover: native title attribute renders the breakdown — uptime %, ok/total,
// last-checked relative + absolute, latest error if a failure. Survives
// the table wrapper's `overflow-hidden` (which would clip a custom
// popover) and matches the dashboard's existing hover patterns.

interface Props {
  tunnelId: string
}

export default function TunnelHealthCell({ tunnelId }: Props) {
  const { data, isLoading } = useQuery({
    queryKey: ['tunnel-health', tunnelId],
    queryFn: () => tunnelsApi.health(tunnelId),
    refetchInterval: 30_000,
    retry: 1,
  })

  if (isLoading) {
    return <span className="text-xs text-gray-300">…</span>
  }
  if (!data || data.total_checks === 0) {
    return <span className="text-xs text-gray-400" title="No health data yet — first check lands within 30s.">—</span>
  }

  const pct = data.uptime_percent
  const colorClass =
    pct == null ? 'text-gray-400'
      : pct >= 99 ? 'text-green-600'
      : pct >= 95 ? 'text-amber-600'
      : 'text-red-600'

  const lastRel = data.latest ? relativeTime(data.latest.checked_at) : '—'
  const lastAbs = data.latest ? new Date(data.latest.checked_at).toLocaleString() : ''
  const tooltipLines = [
    `Uptime (24h): ${pct != null ? `${pct}%` : '—'}`,
    `${data.ok_checks}/${data.total_checks} checks ok`,
    `Last check: ${lastRel}${lastAbs ? ` (${lastAbs})` : ''}`,
  ]
  if (data.latest && !data.latest.ok && data.latest.error_msg) {
    tooltipLines.push(`Error: ${data.latest.error_msg}`)
  }

  return (
    <span
      className={`text-xs font-semibold tabular-nums cursor-help ${colorClass}`}
      title={tooltipLines.join('\n')}
    >
      {pct != null ? `${pct}%` : '—'}
    </span>
  )
}
