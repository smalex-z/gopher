import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Activity, Search, RefreshCw } from 'lucide-react'
import { eventsApi, type ActivityEvent, type EventSeverity, type EventSource } from '../api/events'

const ALL_SOURCES: EventSource[] = ['auth', 'machine', 'tunnel', 'health', 'firewall', 'system']
const ALL_SEVERITIES: EventSeverity[] = ['info', 'warn', 'error', 'critical']

const PAGE_SIZE = 100

const severityStyles: Record<EventSeverity, { dot: string; pill: string }> = {
  info:     { dot: 'bg-blue-400',  pill: 'bg-blue-50 text-blue-700 border-blue-200' },
  warn:     { dot: 'bg-amber-400', pill: 'bg-amber-50 text-amber-700 border-amber-200' },
  error:    { dot: 'bg-red-400',   pill: 'bg-red-50 text-red-700 border-red-200' },
  critical: { dot: 'bg-red-600',   pill: 'bg-red-100 text-red-800 border-red-300' },
}

const sourceStyles: Record<EventSource, string> = {
  auth:     'bg-purple-50 text-purple-700 border-purple-200',
  machine:  'bg-blue-50 text-blue-700 border-blue-200',
  tunnel:   'bg-emerald-50 text-emerald-700 border-emerald-200',
  health:   'bg-cyan-50 text-cyan-700 border-cyan-200',
  firewall: 'bg-orange-50 text-orange-700 border-orange-200',
  system:   'bg-gray-50 text-gray-600 border-gray-200',
}

function formatTime(iso: string) {
  try {
    return new Date(iso).toLocaleString(undefined, { dateStyle: 'short', timeStyle: 'medium' })
  } catch {
    return iso
  }
}

const selectClass =
  'h-9 px-3 pr-8 text-sm rounded-lg border border-gray-200 bg-white text-gray-700 focus:border-gray-400 focus:ring-0 cursor-pointer'

export default function LogsPage() {
  const [source, setSource] = useState<EventSource | ''>('')
  const [minSeverity, setMinSeverity] = useState<EventSeverity | ''>('')
  const [search, setSearch] = useState('')

  const [before, setBefore] = useState<string | undefined>(undefined)
  const [pages, setPages] = useState<ActivityEvent[][]>([])

  const filterKey = useMemo(
    () => JSON.stringify({ source, minSeverity, search }),
    [source, minSeverity, search],
  )

  const { data, isLoading, isFetching, error, refetch } = useQuery({
    queryKey: ['events', filterKey, before],
    queryFn: () => eventsApi.list({
      sources: source ? [source] : undefined,
      minSeverity: minSeverity || undefined,
      q: search.trim() || undefined,
      before,
      limit: PAGE_SIZE,
    }),
    refetchInterval: before ? false : 15_000,
  })

  useEffect(() => {
    setPages([])
    setBefore(undefined)
  }, [filterKey])

  const visibleEvents = useMemo(() => {
    const all = pages.flat()
    if (data?.events) all.push(...data.events)
    const seen = new Set<string>()
    return all.filter(e => {
      if (seen.has(e.id)) return false
      seen.add(e.id)
      return true
    })
  }, [pages, data])

  const total = data?.total ?? 0

  const loadOlder = () => {
    if (!data?.events?.length) return
    const oldest = data.events[data.events.length - 1]
    setPages(prev => [...prev, data.events])
    setBefore(oldest.created_at)
  }

  const hasMore = (data?.events?.length ?? 0) === PAGE_SIZE

  return (
    <div className="space-y-6">
      <header className="flex items-end justify-between gap-4 flex-wrap">
        <div>
          <h1 className="text-2xl font-bold text-gray-900 flex items-center gap-2">
            <Activity className="text-gray-400" /> Logs
          </h1>
          <p className="text-sm text-gray-500 mt-1">
            Audit log + system events. Auth, machine lifecycle, tunnel changes, health transitions.
          </p>
        </div>
      </header>

      <div className="bg-white rounded-2xl shadow-sm border border-gray-200">
        {/* ── Filter bar ──────────────────────────────────────────────── */}
        <div className="flex items-center gap-3 px-4 py-3 border-b border-gray-100">
          <select
            value={source}
            onChange={e => setSource(e.target.value as EventSource | '')}
            className={selectClass}
          >
            <option value="">All sources</option>
            {ALL_SOURCES.map(s => <option key={s} value={s}>{s}</option>)}
          </select>

          <select
            value={minSeverity}
            onChange={e => setMinSeverity(e.target.value as EventSeverity | '')}
            className={selectClass}
          >
            <option value="">All severities</option>
            {ALL_SEVERITIES.map(s => <option key={s} value={s}>{s}+</option>)}
          </select>

          <div className="relative flex-1 min-w-[200px]">
            <Search size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
            <input
              type="text"
              value={search}
              onChange={e => setSearch(e.target.value)}
              placeholder="Search message, resource, or kind"
              className="w-full h-9 pl-9 pr-3 text-sm rounded-lg border border-gray-200 focus:border-gray-400 focus:ring-0"
            />
          </div>

          <span className="text-xs text-gray-400 whitespace-nowrap">
            {isLoading ? 'loading…' : `${visibleEvents.length.toLocaleString()} of ${total.toLocaleString()}`}
          </span>

          <button
            onClick={() => { setPages([]); setBefore(undefined); refetch() }}
            className="flex items-center gap-1.5 h-9 px-3 text-sm rounded-lg border border-gray-200 text-gray-600 hover:bg-gray-50"
          >
            <RefreshCw size={14} className={isFetching ? 'animate-spin' : ''} />
            Refresh
          </button>
        </div>

        {/* ── Table ───────────────────────────────────────────────────── */}
        {error && <p className="px-6 py-4 text-sm text-red-500">Failed to load events</p>}
        {!error && !isLoading && visibleEvents.length === 0 && (
          <p className="px-6 py-8 text-sm text-gray-400 text-center">No events match the current filters.</p>
        )}

        {visibleEvents.length > 0 && (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-gray-100 bg-gray-50/50">
                  <th className="text-left py-2 px-4 font-medium text-gray-500 text-xs whitespace-nowrap">Time</th>
                  <th className="text-left py-2 px-4 font-medium text-gray-500 text-xs">Severity</th>
                  <th className="text-left py-2 px-4 font-medium text-gray-500 text-xs">Source</th>
                  <th className="text-left py-2 px-4 font-medium text-gray-500 text-xs">Kind</th>
                  <th className="text-left py-2 px-4 font-medium text-gray-500 text-xs">Message</th>
                  <th className="text-left py-2 px-4 font-medium text-gray-500 text-xs">Resource</th>
                  <th className="text-left py-2 px-4 font-medium text-gray-500 text-xs">IP</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-50">
                {visibleEvents.map(ev => {
                  const sev = severityStyles[ev.severity] ?? severityStyles.info
                  const src = sourceStyles[ev.source] ?? sourceStyles.system
                  return (
                    <tr key={ev.id} className="hover:bg-gray-50">
                      <td className="py-2 px-4 text-gray-400 text-xs whitespace-nowrap font-mono">{formatTime(ev.created_at)}</td>
                      <td className="py-2 px-4">
                        <span className={`inline-flex items-center gap-1.5 text-xs font-medium px-2 py-0.5 rounded border ${sev.pill}`}>
                          <span className={`w-1.5 h-1.5 rounded-full ${sev.dot}`} />
                          {ev.severity}
                        </span>
                      </td>
                      <td className="py-2 px-4">
                        <span className={`inline-flex text-xs font-medium px-2 py-0.5 rounded border ${src}`}>
                          {ev.source}
                        </span>
                      </td>
                      <td className="py-2 px-4 text-xs text-gray-600 font-mono whitespace-nowrap">{ev.kind}</td>
                      <td className="py-2 px-4 text-gray-700">{ev.message}</td>
                      <td className="py-2 px-4 text-xs text-gray-500">{ev.resource_name || '—'}</td>
                      <td className="py-2 px-4 font-mono text-xs text-gray-600">{ev.ip || '—'}</td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}

        {hasMore && (
          <div className="px-4 py-3 border-t border-gray-100 flex justify-center">
            <button
              onClick={loadOlder}
              disabled={isFetching}
              className="text-sm px-4 py-1.5 rounded-lg border border-gray-200 text-gray-600 hover:bg-gray-50 disabled:opacity-50"
            >
              {isFetching ? 'Loading…' : 'Load older'}
            </button>
          </div>
        )}
      </div>
    </div>
  )
}
