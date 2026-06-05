import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Activity, Cpu, MemoryStick, HardDrive, Clock, RefreshCw, AlertTriangle, LifeBuoy } from 'lucide-react'
import { machinesApi } from '../api/machines'
import { toast } from '../lib/toast'
import { relativeTime, formatBytes, formatDuration } from '../lib/time'
import Sparkline from './Sparkline'
import RecoveryModal from './RecoveryModal'
import type { Machine } from '../types'

// Per-machine health panel embedded in the Machines page expanded row.
// Shows what the agent's /status endpoint reports + the rolling uptime
// from the health-check history.
//
// Live agent metrics are fetched on-demand (only when the row is expanded
// and the agent is reachable). The summary is cheap and refreshes on the
// same 30s page cadence as the rest of the dashboard.
//
// Renders a "Test now" button that triggers an immediate health probe so
// the operator doesn't have to wait for the next scheduled poll.

interface Props {
  machine: Machine
}

export default function MachineHealthPanel({ machine }: Props) {
  const qc = useQueryClient()
  const agentReachable = !!machine.agent_installed && !!machine.agent_remote_port

  // Health summary — uptime % + sparkline data. Cheap (single DB query),
  // refresh on the page's existing cadence.
  const summaryQuery = useQuery({
    queryKey: ['machine-health', machine.id],
    queryFn: () => machinesApi.health(machine.id),
    refetchInterval: 30_000,
  })

  // Live agent status — one HTTP round-trip through the rathole back-channel.
  // Don't auto-refresh; the operator can hit "Test now" if they want fresh
  // data. Skipped entirely when the agent isn't reachable.
  const statusQuery = useQuery({
    queryKey: ['agent-status', machine.id],
    queryFn: () => machinesApi.agentStatus(machine.id),
    enabled: agentReachable,
  })

  const testNow = useMutation({
    mutationFn: () => machinesApi.runCheck(machine.id),
    onSuccess: () => {
      toast.success('Health check ran')
      qc.invalidateQueries({ queryKey: ['machine-health', machine.id] })
      qc.invalidateQueries({ queryKey: ['agent-status', machine.id] })
      qc.invalidateQueries({ queryKey: ['machines'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  // Recovery fallback chain:
  //   1. POST /recover — server-side push (agent → SSH-via-tunnel).
  //   2. If that fails (typically because the tunnel is fully down), open
  //      a modal with the manual script + instructions.
  // The button is shown whenever something is actually broken on this row:
  // the machine itself is offline, a config push is still pending from an
  // earlier failure, OR any single tunnel is offline. The last case covers
  // "machine reachable but tunnel-X's client section got lost" — the
  // canonical config push re-adds it. Idempotent for healthy-but-stale rows.
  const [recoveryModal, setRecoveryModal] = useState<{ open: boolean; reason: string }>({ open: false, reason: '' })
  const anyTunnelDown = (machine.tunnels ?? []).some(t => t.status === 'offline')
  const needsRecovery = machine.status === 'offline' || machine.config_push_pending === true || anyTunnelDown
  const recover = useMutation({
    mutationFn: () => machinesApi.recover(machine.id),
    onSuccess: (res) => {
      toast.success(res?.message || 'Config pushed — rathole reconnecting')
      qc.invalidateQueries({ queryKey: ['machine-health', machine.id] })
      qc.invalidateQueries({ queryKey: ['agent-status', machine.id] })
      qc.invalidateQueries({ queryKey: ['machines'] })
    },
    onError: (e: Error) => {
      // Both server-side push paths failed — the rathole tunnel is fully
      // down. Fall back to manual: open the modal with the script and
      // instructions. The error message is shown verbatim so the operator
      // can see why (disk full, timeout, etc.) and act on it.
      setRecoveryModal({ open: true, reason: e.message })
    },
  })

  const summary = summaryQuery.data
  const status = statusQuery.data

  // Memory + disk percentages straight from the agent's /status payload.
  const memUsedPct = status?.system && status.system.mem_total_kb > 0
    ? 100 - Math.round((status.system.mem_avail_kb / status.system.mem_total_kb) * 100)
    : null
  const diskUsedPct = status?.system && status.system.disk_total_bytes > 0
    ? 100 - Math.round((status.system.disk_free_bytes / status.system.disk_total_bytes) * 100)
    : null

  return (
    <div className="mt-3 border border-gray-200 rounded-lg bg-white p-3 space-y-3">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-1.5 text-xs font-semibold text-gray-700">
          <Activity size={12} className="text-blue-500" />
          Health & metrics
        </div>
        <div className="flex items-center gap-1.5">
          <button
            onClick={() => testNow.mutate()}
            disabled={testNow.isPending}
            className="flex items-center gap-1 px-2 py-0.5 text-xs border border-gray-300 rounded hover:bg-gray-50 disabled:opacity-50"
            title="Run a health check now instead of waiting for the next 60s poll"
          >
            <RefreshCw size={11} className={testNow.isPending ? 'animate-spin' : ''} />
            {testNow.isPending ? 'Testing…' : 'Test now'}
          </button>
          {needsRecovery && (
            <button
              onClick={() => recover.mutate()}
              disabled={recover.isPending}
              title="Try to repair this machine: agent push → SSH fallback → manual script. The button only appears when the machine is offline or has a deferred config push."
              className="flex items-center gap-1 px-2 py-0.5 text-xs border border-amber-300 bg-amber-50 text-amber-800 rounded hover:bg-amber-100 disabled:opacity-50"
            >
              <LifeBuoy size={11} className={recover.isPending ? 'animate-spin' : ''} />
              {recover.isPending ? 'Recovering…' : 'Recover'}
            </button>
          )}
        </div>
      </div>

      {/* Uptime + sparkline */}
      <div className="grid grid-cols-2 gap-3 text-xs">
        <div className="flex flex-col gap-0.5">
          <span className="text-gray-500">Uptime (24h)</span>
          <div className="flex items-baseline gap-2">
            <span className="text-base font-semibold text-gray-900">
              {summary?.uptime_percent != null ? `${summary.uptime_percent}%` : '—'}
            </span>
            {summary && summary.total_checks > 0 && (
              <span className="text-gray-400">
                {summary.ok_checks}/{summary.total_checks} ok
              </span>
            )}
          </div>
        </div>
        <div className="flex flex-col gap-0.5">
          <span className="text-gray-500">Recent checks</span>
          {summary && summary.recent.length > 0 ? (
            <Sparkline checks={summary.recent} title={`Last ${summary.recent.length} checks`} />
          ) : (
            <span className="text-gray-400">No data yet</span>
          )}
        </div>
      </div>

      {/* Last seen / latest error */}
      <div className="flex items-center justify-between text-xs">
        <span className="text-gray-500">
          Last check:{' '}
          <span className="text-gray-800">
            {summary?.latest ? relativeTime(summary.latest.checked_at) : '—'}
          </span>
        </span>
        {machine.agent_last_seen && (
          <span className="text-gray-500">
            Agent last seen:{' '}
            <span className="text-gray-800">{relativeTime(machine.agent_last_seen)}</span>
          </span>
        )}
      </div>
      {summary?.latest && !summary.latest.ok && summary.latest.error_msg && (
        <div className="flex items-start gap-1.5 text-xs px-2 py-1.5 bg-red-50 border border-red-200 rounded text-red-700">
          <AlertTriangle size={11} className="mt-0.5 shrink-0" />
          <span className="font-mono break-all">{summary.latest.error_msg}</span>
        </div>
      )}

      {/* Live agent metrics — only when the back-channel is up */}
      {agentReachable && (
        <div className="border-t border-gray-100 pt-3">
          {statusQuery.isLoading && (
            <div className="text-xs text-gray-400">Fetching live metrics…</div>
          )}
          {statusQuery.isError && (
            <div className="text-xs text-red-600">Could not reach agent: {(statusQuery.error as Error).message}</div>
          )}
          {status && (
            <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 text-xs">
              <Metric icon={<Cpu size={12} />} label="Load (1m / 5m / 15m)">
                {status.system
                  ? `${status.system.load_avg_1.toFixed(2)} / ${status.system.load_avg_5.toFixed(2)} / ${status.system.load_avg_15.toFixed(2)}`
                  : '—'}
              </Metric>
              <Metric icon={<MemoryStick size={12} />} label="Memory">
                {memUsedPct != null
                  ? `${memUsedPct}% of ${formatBytes(status.system.mem_total_kb * 1024)}`
                  : '—'}
              </Metric>
              <Metric icon={<HardDrive size={12} />} label="Disk">
                {diskUsedPct != null
                  ? `${diskUsedPct}% of ${formatBytes(status.system.disk_total_bytes)}`
                  : '—'}
              </Metric>
              <Metric icon={<Clock size={12} />} label="Agent uptime">
                {formatDuration(status.agent_uptime_seconds)}
              </Metric>
              <div className="col-span-2 sm:col-span-4 text-[11px] text-gray-400 truncate">
                {status.system.hostname && <>host {status.system.hostname}</>}
                {status.system.kernel && <> • kernel {status.system.kernel}</>}
                {status.restarts_served > 0 && <> • {status.restarts_served} rathole restart{status.restarts_served === 1 ? '' : 's'} served</>}
              </div>
            </div>
          )}
        </div>
      )}

      <RecoveryModal
        isOpen={recoveryModal.open}
        onClose={() => setRecoveryModal({ open: false, reason: '' })}
        machineID={machine.id}
        machineName={machine.name}
        reason={recoveryModal.reason}
      />
    </div>
  )
}

interface MetricProps {
  icon: React.ReactNode
  label: string
  children: React.ReactNode
}

function Metric({ icon, label, children }: MetricProps) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="flex items-center gap-1 text-gray-500">
        <span className="text-gray-400">{icon}</span>
        {label}
      </span>
      <span className="text-gray-900 font-medium tabular-nums">{children}</span>
    </div>
  )
}
