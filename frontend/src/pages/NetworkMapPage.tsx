import { useQuery } from '@tanstack/react-query'
import { machinesApi } from '../api/machines'
import { tunnelsApi } from '../api/tunnels'
import { localApi } from '../api/local'
import { vpsApi } from '../api/vps'
import type { Machine, Tunnel } from '../types'
import { useState } from 'react'

const VPS_Y = 90
const MACHINES_Y = 350
const NODE_W = 148
const NODE_H = 62
const VPS_W = 172
const VPS_H = 68
const MIN_SPACING = 190
const SIDE_PAD = 110

function nodeStatusStyle(status: string) {
  if (status === 'active' || status === 'connected') {
    return { fill: '#f0fdf4', stroke: '#16a34a', textColor: '#14532d', dot: '#22c55e', lineColor: '#4ade80' }
  }
  if (status === 'pending' || status === 'connecting') {
    return { fill: '#fefce8', stroke: '#ca8a04', textColor: '#713f12', dot: '#eab308', lineColor: '#facc15' }
  }
  return { fill: '#f9fafb', stroke: '#9ca3af', textColor: '#4b5563', dot: '#d1d5db', lineColor: '#e5e7eb' }
}

function VPSNode({ x, y, domain, host }: { x: number; y: number; domain: string; host?: string }) {
  return (
    <g style={{ cursor: 'default' }}>
      {/* glow ring */}
      <rect
        x={x - VPS_W / 2 - 4} y={y - VPS_H / 2 - 4}
        width={VPS_W + 8} height={VPS_H + 8}
        rx={14} fill="none" stroke="#818cf8" strokeWidth={2} opacity={0.35}
      />
      {/* body */}
      <rect
        x={x - VPS_W / 2} y={y - VPS_H / 2}
        width={VPS_W} height={VPS_H}
        rx={10} fill="#eef2ff" stroke="#6366f1" strokeWidth={2}
      />
      {/* label */}
      <text x={x} y={y - 14} textAnchor="middle" fontSize={10} fontWeight={700} fill="#4338ca"
        fontFamily="ui-sans-serif,system-ui,sans-serif" letterSpacing={1.5}>
        VPS SERVER
      </text>
      {/* IP */}
      {host && (
        <text x={x} y={y + 2} textAnchor="middle" fontSize={11.5} fill="#4338ca"
          fontFamily="ui-monospace,monospace" fontWeight={600}>
          {host}
        </text>
      )}
      {/* domain */}
      <text x={x} y={y + (host ? 17 : 6)} textAnchor="middle" fontSize={10.5} fill="#6366f1"
        fontFamily="ui-monospace,monospace" opacity={0.8}>
        {domain.length > 24 ? domain.slice(0, 22) + '…' : domain}
      </text>
      {/* online dot */}
      <circle cx={x + VPS_W / 2 - 10} cy={y - VPS_H / 2 + 10} r={5} fill="#22c55e" />
    </g>
  )
}

function MachineNode({
  x, y, machine, selected, onClick,
}: {
  x: number; y: number; machine: Machine; selected: boolean; onClick: () => void
}) {
  const s = nodeStatusStyle(machine.status)
  const displayHost = machine.host
    ? (machine.host.length > 18 ? machine.host.slice(0, 16) + '…' : machine.host)
    : null

  return (
    <g onClick={onClick} style={{ cursor: 'pointer' }}>
      {selected && (
        <rect
          x={x - NODE_W / 2 - 5} y={y - NODE_H / 2 - 5}
          width={NODE_W + 10} height={NODE_H + 10}
          rx={13} fill="none" stroke="#6366f1" strokeWidth={2.5} opacity={0.6}
        />
      )}
      <rect
        x={x - NODE_W / 2} y={y - NODE_H / 2}
        width={NODE_W} height={NODE_H}
        rx={9} fill={s.fill} stroke={s.stroke} strokeWidth={1.75}
      />
      {/* status dot */}
      <circle cx={x + NODE_W / 2 - 9} cy={y - NODE_H / 2 + 9} r={5} fill={s.dot} />
      {/* machine name */}
      <text x={x} y={y - 12} textAnchor="middle" fontSize={12.5} fontWeight={700} fill={s.textColor}
        fontFamily="ui-sans-serif,system-ui,sans-serif">
        {machine.name.length > 16 ? machine.name.slice(0, 14) + '…' : machine.name}
      </text>
      {/* IP */}
      {displayHost && (
        <text x={x} y={y + 5} textAnchor="middle" fontSize={11} fill={s.textColor}
          fontFamily="ui-monospace,monospace" fontWeight={600}>
          {displayHost}
        </text>
      )}
      {/* username */}
      <text x={x} y={y + (displayHost ? 20 : 8)} textAnchor="middle" fontSize={10} fill={s.textColor}
        fontFamily="ui-sans-serif,system-ui,sans-serif" opacity={0.6}>
        @{machine.username}
      </text>
    </g>
  )
}

function PortBadge({ x, y, tunnel }: { x: number; y: number; tunnel: Tunnel }) {
  const isHTTP = tunnel.protocol === 'http'
  const bgColor = isHTTP ? '#eff6ff' : '#fffbeb'
  const borderColor = isHTTP ? '#93c5fd' : '#fcd34d'
  const textColor = isHTTP ? '#1d4ed8' : '#92400e'
  const label = `:${tunnel.rathole_port} → :${tunnel.local_port}`
  const w = label.length * 6.2 + 12
  const active = tunnel.status === 'active'

  return (
    <g opacity={active ? 1 : 0.55}>
      <rect x={x - w / 2} y={y - 9} width={w} height={18} rx={4}
        fill={bgColor} stroke={borderColor} strokeWidth={1} />
      <text x={x} y={y + 4} textAnchor="middle" fontSize={10} fill={textColor}
        fontFamily="ui-monospace,monospace" fontWeight={600}>
        {label}
      </text>
    </g>
  )
}

function ConnectionLine({
  mx, vpsX, tunnels,
}: {
  mx: number; vpsX: number; tunnels: Tunnel[]
}) {
  const my = MACHINES_Y - NODE_H / 2
  const vy = VPS_Y + VPS_H / 2
  const hasActive = tunnels.some(t => t.status === 'active')
  const lineColor = hasActive ? '#4ade80' : '#d1d5db'

  const cp1y = my - (my - vy) * 0.4
  const cp2y = vy + (my - vy) * 0.4
  const path = `M ${mx} ${my} C ${mx} ${cp1y}, ${vpsX} ${cp2y}, ${vpsX} ${vy}`

  // midpoint of cubic bezier at t=0.5
  const midX = (mx + vpsX) / 2
  const midY = (my + vy) / 2

  const BADGE_H = 22
  const totalH = tunnels.length * BADGE_H
  const startY = midY - totalH / 2 + BADGE_H / 2

  // if line is near vertical (machine close to VPS x), offset badges sideways
  const offsetX = Math.abs(mx - vpsX) < 60 ? 70 : 0

  return (
    <g>
      <path
        d={path} fill="none" stroke={lineColor}
        strokeWidth={hasActive ? 2.5 : 1.5}
        strokeDasharray={hasActive ? undefined : '5 4'}
        opacity={0.8}
      />
      {tunnels.map((t, i) => (
        <PortBadge
          key={t.id}
          x={midX + offsetX}
          y={startY + i * BADGE_H}
          tunnel={t}
        />
      ))}
      {tunnels.length === 0 && (
        <text x={midX} y={midY + 4} textAnchor="middle" fontSize={10} fill="#d1d5db"
          fontFamily="ui-sans-serif,system-ui,sans-serif">
          no tunnels
        </text>
      )}
    </g>
  )
}

function TunnelDetailRow({ tunnel, domain }: { tunnel: Tunnel; domain?: string }) {
  const isHTTP = tunnel.protocol === 'http' && tunnel.subdomain
  return (
    <div className="flex items-center gap-2 py-2 border-t border-gray-100 first:border-t-0">
      <span className={`text-xs font-mono px-1.5 py-0.5 rounded uppercase font-semibold shrink-0 ${tunnel.protocol === 'http' ? 'bg-blue-100 text-blue-700' : 'bg-amber-100 text-amber-700'}`}>
        {tunnel.protocol}
      </span>
      <span className="text-sm text-gray-800 font-medium truncate flex-1">{tunnel.name}</span>
      <span className="text-xs font-mono text-purple-700 bg-purple-50 border border-purple-200 rounded px-1.5 py-0.5 shrink-0">
        :{tunnel.rathole_port}
      </span>
      <span className="text-gray-300 text-xs">→</span>
      <span className="text-xs font-mono text-green-700 bg-green-50 border border-green-200 rounded px-1.5 py-0.5 shrink-0">
        :{tunnel.local_port}
      </span>
      <span className={`text-xs font-medium px-1.5 py-0.5 rounded shrink-0 ${tunnel.status === 'active' ? 'bg-green-100 text-green-700' : 'bg-gray-100 text-gray-500'}`}>
        {tunnel.status}
      </span>
      {isHTTP && domain && (
        <a
          href={`https://${tunnel.subdomain}.${domain}`}
          target="_blank"
          rel="noopener noreferrer"
          className="text-blue-500 hover:text-blue-700 text-xs truncate max-w-[130px] shrink-0"
        >
          {tunnel.subdomain}.{domain}
        </a>
      )}
    </div>
  )
}

export default function NetworkMapPage() {
  const [selectedId, setSelectedId] = useState<string | null>(null)

  const { data: machinesData } = useQuery({
    queryKey: ['machines'],
    queryFn: () => machinesApi.list(),
    refetchInterval: 30000,
  })
  const { data: tunnelsData } = useQuery({
    queryKey: ['tunnels'],
    queryFn: () => tunnelsApi.list(),
    refetchInterval: 30000,
  })
  const { data: localStatus } = useQuery({
    queryKey: ['local-status'],
    queryFn: () => localApi.status(),
    refetchInterval: 30000,
  })
  const { data: vpsData } = useQuery({
    queryKey: ['vps'],
    queryFn: () => vpsApi.get(),
    refetchInterval: 60000,
  })

  const machines: Machine[] = machinesData?.data ?? []
  const tunnels: Tunnel[] = tunnelsData?.data ?? []
  const domain = localStatus?.domain ?? ''
  const vpsHost = vpsData?.data?.host

  const selectedMachine = machines.find(m => m.id === selectedId) ?? null
  const selectedTunnels = selectedMachine ? tunnels.filter(t => t.machine_id === selectedMachine.id) : []

  // SVG layout
  const svgWidth = Math.max(900, machines.length * MIN_SPACING + SIDE_PAD * 2)
  const svgHeight = 460
  const vpsX = svgWidth / 2

  const machinePositions = machines.map((_, i) => {
    const span = svgWidth - SIDE_PAD * 2
    const x = machines.length === 1
      ? svgWidth / 2
      : SIDE_PAD + (span / (machines.length - 1)) * i
    return { x, y: MACHINES_Y }
  })

  const totalPorts = new Set(tunnels.map(t => t.rathole_port)).size
  const activeMachines = machines.filter(m => m.status === 'active' || m.status === 'connected').length

  return (
    <div className="space-y-5">
      {/* Header */}
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-bold text-gray-900">Network Map</h1>
          <p className="text-gray-500 mt-1">Visual topology of connected machines and tunnels</p>
        </div>
        <div className="flex gap-4 text-sm text-gray-500 pt-1">
          <span><strong className="text-gray-800">{activeMachines}</strong>/<strong className="text-gray-800">{machines.length}</strong> online</span>
          <span><strong className="text-gray-800">{tunnels.length}</strong> tunnels</span>
          <span><strong className="text-gray-800">{totalPorts}</strong> ports mapped</span>
        </div>
      </div>

      {/* SVG topology diagram */}
      <div className="bg-white rounded-xl border shadow-sm overflow-x-auto">
        <div style={{ minWidth: Math.min(svgWidth, 900) }}>
          <svg viewBox={`0 0 ${svgWidth} ${svgHeight}`} className="w-full" style={{ minHeight: 300 }}>
            <defs>
              <pattern id="dots" x="0" y="0" width="24" height="24" patternUnits="userSpaceOnUse">
                <circle cx="1" cy="1" r="1" fill="#e5e7eb" />
              </pattern>
            </defs>
            <rect width={svgWidth} height={svgHeight} fill="url(#dots)" />

            {/* Connection lines behind nodes */}
            {machines.map((machine, i) => (
              <ConnectionLine
                key={machine.id}
                mx={machinePositions[i].x}
                vpsX={vpsX}
                tunnels={tunnels.filter(t => t.machine_id === machine.id)}
              />
            ))}

            {/* VPS node */}
            {domain
              ? <VPSNode x={vpsX} y={VPS_Y} domain={domain} host={vpsHost} />
              : (
                <g>
                  <rect x={vpsX - VPS_W / 2} y={VPS_Y - VPS_H / 2} width={VPS_W} height={VPS_H}
                    rx={10} fill="#f3f4f6" stroke="#d1d5db" strokeWidth={2} />
                  <text x={vpsX} y={VPS_Y + 5} textAnchor="middle" fontSize={12} fill="#9ca3af"
                    fontFamily="ui-sans-serif,system-ui,sans-serif">
                    VPS (not configured)
                  </text>
                </g>
              )
            }

            {/* Machine nodes */}
            {machines.map((machine, i) => (
              <MachineNode
                key={machine.id}
                x={machinePositions[i].x}
                y={machinePositions[i].y}
                machine={machine}
                selected={selectedId === machine.id}
                onClick={() => setSelectedId(selectedId === machine.id ? null : machine.id)}
              />
            ))}

            {machines.length === 0 && (
              <text x={svgWidth / 2} y={svgHeight / 2 + 40} textAnchor="middle" fontSize={14}
                fill="#9ca3af" fontFamily="ui-sans-serif,system-ui,sans-serif">
                No machines registered yet
              </text>
            )}
          </svg>
        </div>
      </div>

      {/* Selected machine detail panel */}
      {selectedMachine && (
        <div className="bg-white rounded-xl border shadow-sm overflow-hidden">
          <div className="px-5 py-4 flex items-center justify-between border-b bg-gray-50">
            <div className="flex items-center gap-3">
              <div className={`w-2.5 h-2.5 rounded-full shrink-0 ${
                selectedMachine.status === 'active' || selectedMachine.status === 'connected'
                  ? 'bg-green-500'
                  : selectedMachine.status === 'pending' || selectedMachine.status === 'connecting'
                  ? 'bg-yellow-500'
                  : 'bg-gray-300'
              }`} />
              <span className="font-bold text-gray-900">{selectedMachine.name}</span>
              {selectedMachine.host && (
                <span className="text-sm text-gray-600 font-mono bg-gray-100 px-2 py-0.5 rounded">
                  {selectedMachine.host}{selectedMachine.port ? `:${selectedMachine.port}` : ''}
                </span>
              )}
              <span className="text-sm text-gray-400">@{selectedMachine.username}</span>
            </div>
            <div className="flex items-center gap-4 text-sm text-gray-500">
              {selectedMachine.tunnel_port > 0 && (
                <span className="font-mono text-indigo-700 bg-indigo-50 border border-indigo-200 px-2 py-0.5 rounded text-xs">
                  SSH :{selectedMachine.tunnel_port}
                </span>
              )}
              {selectedMachine.last_seen && (
                <span>last seen {new Date(selectedMachine.last_seen).toLocaleString()}</span>
              )}
              <button onClick={() => setSelectedId(null)} className="text-gray-400 hover:text-gray-600 text-lg leading-none">×</button>
            </div>
          </div>
          <div className="px-5 py-3">
            {selectedTunnels.length === 0 ? (
              <p className="text-sm text-gray-400 py-2">No tunnels configured for this machine.</p>
            ) : (
              selectedTunnels.map(t => (
                <TunnelDetailRow key={t.id} tunnel={t} domain={domain} />
              ))
            )}
          </div>
        </div>
      )}

      {/* Legend */}
      <div className="flex flex-wrap items-center gap-4 text-xs text-gray-500 bg-white border rounded-lg px-4 py-3">
        <span className="font-semibold text-gray-700">Legend:</span>
        <span className="flex items-center gap-1.5"><span className="w-3 h-3 rounded-full bg-green-500 inline-block" /> Connected</span>
        <span className="flex items-center gap-1.5"><span className="w-3 h-3 rounded-full bg-yellow-400 inline-block" /> Connecting</span>
        <span className="flex items-center gap-1.5"><span className="w-3 h-3 rounded-full bg-gray-300 inline-block" /> Offline</span>
        <span className="flex items-center gap-1.5"><span className="inline-block w-6 border-t-2 border-green-400" /> Active tunnels</span>
        <span className="flex items-center gap-1.5"><span className="inline-block w-6 border-t-2 border-dashed border-gray-300" /> No tunnels</span>
        <span className="flex items-center gap-1.5 text-blue-600"><span className="font-mono bg-blue-50 border border-blue-200 px-1 rounded">:8080→:3000</span> HTTP tunnel port mapping</span>
        <span className="flex items-center gap-1.5 text-amber-700"><span className="font-mono bg-amber-50 border border-amber-200 px-1 rounded">:9000→:22</span> TCP tunnel port mapping</span>
        <span className="ml-auto text-gray-400 italic">Click a machine to view tunnel details</span>
      </div>
    </div>
  )
}
