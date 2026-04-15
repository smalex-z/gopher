import { useQuery, useQueries } from '@tanstack/react-query'
import { machinesApi } from '../api/machines'
import { tunnelsApi } from '../api/tunnels'
import { localApi } from '../api/local'
import { vpsApi } from '../api/vps'
import type { Machine, Tunnel } from '../types'
import { useState } from 'react'

// ── Layout constants ──────────────────────────────────────────────────────────
const SVG_WIDTH = 940
const VPS_X = 16
const VPS_W = 235
const VPS_RIGHT = VPS_X + VPS_W         // 251 — right edge, where connector dots live
const MACHINE_X = 640                    // machine box left edge; connector dots live here
const MACHINE_W = 225
const ROW_H = 27                         // height per port row inside a box
const MACH_HEADER = 72                   // machine box header (name + host + user + divider)
const VPS_HEADER = 70                    // VPS header (title + host + wildcard+domain)
const LAN_HEADER_H = 26                  // LAN group label strip
const LAN_PAD = 10                       // inner padding inside LAN group box
const LAN_GAP = 18                       // gap between stacked LAN groups
const MACH_GAP = 12                      // gap between machines inside a LAN group
const V_PAD = 32

// ── Helpers ───────────────────────────────────────────────────────────────────
function isPrivateIP(ip: string): boolean {
  const p = ip.split('.').map(Number)
  if (p.length !== 4 || p.some(isNaN)) return false
  return (
    p[0] === 10 ||
    p[0] === 127 ||
    (p[0] === 172 && p[1] >= 16 && p[1] <= 31) ||
    (p[0] === 192 && p[1] === 168)
  )
}

function machineBoxH(tunnelCount: number): number {
  return MACH_HEADER + (tunnelCount > 0 ? tunnelCount * ROW_H + 6 : 0)
}

function lanGroupH(machineTunnelCounts: number[]): number {
  const machH = machineTunnelCounts.reduce((s, n) => s + machineBoxH(n), 0)
    + Math.max(0, machineTunnelCounts.length - 1) * MACH_GAP
  return LAN_HEADER_H + LAN_PAD + machH + LAN_PAD
}

function tunnelVpsRowH(t: Tunnel): number {
  return t.subdomain ? ROW_H + 13 : ROW_H
}

function vpsBoxH(groups: { tunnels: Tunnel[] }[]): number {
  let portH = 0
  for (const { tunnels } of groups) {
    if (tunnels.length === 0) continue
    portH += 16
    for (const t of tunnels) portH += tunnelVpsRowH(t)
    portH += 6
  }
  return VPS_HEADER + (portH > 0 ? 10 + portH : 0) + 10
}

function statusStyle(status: string) {
  if (status === 'active' || status === 'connected')
    return { fill: '#f0fdf4', stroke: '#16a34a', text: '#14532d', dot: '#22c55e' }
  if (status === 'pending' || status === 'connecting')
    return { fill: '#fefce8', stroke: '#ca8a04', text: '#92400e', dot: '#eab308' }
  // 'offline' or any unknown status
  return { fill: '#f9fafb', stroke: '#d1d5db', text: '#6b7280', dot: '#d1d5db' }
}

// ── Computed layout types ─────────────────────────────────────────────────────
interface MachLayout {
  machine: Machine
  tunnels: Tunnel[]
  top: number     // absolute y of top of machine box
  boxH: number
}
interface LanGroup {
  publicIP: string    // '' = unknown
  isNat: boolean
  layouts: MachLayout[]
  groupTop: number
  groupH: number
}
interface TunnelLine {
  tunnel: Tunnel
  vpsY: number
  machY: number
}

// ── Detail row ────────────────────────────────────────────────────────────────
function TunnelDetailRow({ tunnel, domain }: { tunnel: Tunnel; domain?: string }) {
  const hasUrl = !!tunnel.subdomain && !!domain
  return (
    <div className="flex items-center gap-2 py-2 border-t border-gray-100 first:border-t-0">
      <span className={`text-xs font-mono px-1.5 py-0.5 rounded uppercase font-semibold shrink-0 ${
        tunnel.transport === 'udp' ? 'bg-purple-50 text-purple-700' : 'bg-gray-100 text-gray-600'
      }`}>
        {tunnel.transport === 'udp' ? 'UDP' : tunnel.protocol}
      </span>
      <span className="text-sm text-gray-800 font-medium truncate flex-1">{tunnel.name}</span>
      <span className="text-xs font-mono text-indigo-700 bg-indigo-50 border border-indigo-200 rounded px-1.5 py-0.5 shrink-0">
        :{tunnel.rathole_port}
      </span>
      <span className="text-gray-300 text-xs">→</span>
      <span className="text-xs font-mono text-green-700 bg-green-50 border border-green-200 rounded px-1.5 py-0.5 shrink-0">
        :{tunnel.local_port}
      </span>
      <span className={`text-xs font-medium px-1.5 py-0.5 rounded shrink-0 ${
        tunnel.status === 'active' ? 'bg-green-100 text-green-700'
        : tunnel.status === 'idle' ? 'bg-amber-100 text-amber-700'
        : 'bg-gray-100 text-gray-500'
      }`}>{tunnel.status === 'idle' ? 'no service' : tunnel.status}</span>
      {hasUrl && domain && (
        <a href={`https://${tunnel.subdomain}.${domain}`} target="_blank" rel="noopener noreferrer"
          className="text-indigo-500 hover:text-indigo-700 text-xs font-mono truncate max-w-[160px] shrink-0">
          {tunnel.subdomain}.{domain}
        </a>
      )}
    </div>
  )
}

// ── Page ──────────────────────────────────────────────────────────────────────
export default function NetworkMapPage() {
  const [selectedId, setSelectedId] = useState<string | null>(null)

  const { data: machinesData } = useQuery({ queryKey: ['machines'], queryFn: () => machinesApi.list(), refetchInterval: 30000 })
  const { data: tunnelsData }  = useQuery({ queryKey: ['tunnels'],  queryFn: () => tunnelsApi.list(), refetchInterval: 30000 })
  const { data: localStatus }  = useQuery({ queryKey: ['local-status'], queryFn: () => localApi.status(), refetchInterval: 30000 })
  const { data: vpsData }      = useQuery({ queryKey: ['vps'], queryFn: () => vpsApi.get(), refetchInterval: 60000 })

  const machines: Machine[] = machinesData?.data ?? []
  const tunnels: Tunnel[]   = tunnelsData?.data ?? []
  const domain = localStatus?.domain ?? ''
  const vpsHost = vpsData?.data?.host ?? ''

  // Resolve VPS hostname → actual IP (no-op if it's already an IP)
  const { data: vpsIPData } = useQuery({
    queryKey: ['resolve-ip', vpsHost],
    queryFn: () => localApi.resolveIP(vpsHost),
    enabled: !!vpsHost,
    staleTime: 10 * 60 * 1000,
  })
  const vpsPublicIP = vpsIPData?.ip ?? vpsHost

  // Resolve bare domain and router.domain — if they match, bare domain points here
  // so we can use it directly; otherwise fall back to router.domain
  const routerHost = domain ? `router.${domain}` : ''
  const { data: domainIPData } = useQuery({
    queryKey: ['resolve-ip', domain],
    queryFn: () => localApi.resolveIP(domain),
    enabled: !!domain,
    staleTime: 10 * 60 * 1000,
  })
  const { data: routerIPData } = useQuery({
    queryKey: ['resolve-ip', routerHost],
    queryFn: () => localApi.resolveIP(routerHost),
    enabled: !!routerHost,
    staleTime: 10 * 60 * 1000,
  })
  const domainIP = domainIPData?.ip ?? ''
  const routerIP = routerIPData?.ip ?? ''
  const displayHost = domainIP && routerIP && domainIP === routerIP ? domain : (routerHost || domain)

  // Fetch network info (public IP, LAN IP, NAT) for each connected machine.
  // Results are cached 10 min; stored public_ip in Machine covers the offline case.
  const netInfoResults = useQueries({
    queries: machines.map(m => ({
      queryKey: ['network-info', m.id],
      queryFn: () => machinesApi.networkInfo(m.id),
      enabled: m.status === 'active' || m.status === 'connected',
      staleTime: 10 * 60 * 1000,
      retry: 1,
    })),
  })

  // Build a map: machineId → { public_ip, private_ip, is_nat }
  const netInfoMap = new Map<string, { public_ip: string; private_ip: string; is_nat: boolean }>()
  const isOnline = (m: Machine) => m.status === 'active' || m.status === 'connected'
  machines.forEach((m, i) => {
    const fresh = netInfoResults[i]?.data
    if (fresh && fresh.public_ip) {
      netInfoMap.set(m.id, { public_ip: fresh.public_ip, private_ip: fresh.private_ip, is_nat: fresh.is_nat })
    } else if (m.public_ip && isOnline(m)) {
      // Only use stale public_ip for machines that are currently online.
      // Offline machines fall back to a solo group so they don't appear inside
      // a LAN bracket that implies current reachability.
      netInfoMap.set(m.id, { public_ip: m.public_ip, private_ip: m.host ?? '', is_nat: isPrivateIP(m.host ?? '') })
    }
  })

  // ── Layout computation ──────────────────────────────────────────────────────
  const groups = machines.map(m => ({ machine: m, tunnels: tunnels.filter(t => t.machine_id === m.id) }))

  const vpsH = vpsBoxH(groups)

  // Group machines by shared public IP → LAN groups
  const lanGroupMap = new Map<string, { publicIP: string; isNat: boolean; machines: Machine[] }>()
  const lanGroupOrder: string[] = []  // keys in insertion order

  for (const { machine } of groups) {
    const info = netInfoMap.get(machine.id)
    const publicIP = info?.public_ip ?? ''
    const isNat = info?.is_nat ?? isPrivateIP(machine.host ?? '')
    const key = publicIP || `solo-${machine.id}`

    if (!lanGroupMap.has(key)) {
      lanGroupMap.set(key, { publicIP, isNat, machines: [] })
      lanGroupOrder.push(key)
    } else if (isNat) {
      // If any machine in the group has confirmed NAT, mark the whole group NAT.
      // Prevents a race where the first machine's host is a hostname (not RFC1918)
      // and the group gets temporarily labeled "direct".
      lanGroupMap.get(key)!.isNat = true
    }
    lanGroupMap.get(key)!.machines.push(machine)
  }

  // Compute LAN group heights and positions
  const computedLanGroups: LanGroup[] = []
  let totalMachAreaH = 0

  for (const key of lanGroupOrder) {
    const g = lanGroupMap.get(key)!
    const counts = g.machines.map(m => tunnels.filter(t => t.machine_id === m.id).length)
    const gH = lanGroupH(counts)
    computedLanGroups.push({
      publicIP: g.publicIP,
      isNat: g.isNat,
      layouts: [],   // filled below
      groupTop: 0,   // filled below
      groupH: gH,
    })
    totalMachAreaH += gH
  }
  totalMachAreaH += Math.max(0, computedLanGroups.length - 1) * LAN_GAP

  const svgH = Math.max(totalMachAreaH + V_PAD * 2, vpsH + V_PAD * 2, 260)

  // Fill in absolute positions
  const lanGroupsTop = (svgH - totalMachAreaH) / 2
  let curGroupY = lanGroupsTop
  const allMachLayouts: MachLayout[] = []  // flat list for VPS port cursor

  for (let gi = 0; gi < computedLanGroups.length; gi++) {
    const lg = computedLanGroups[gi]
    const key = lanGroupOrder[gi]
    const g = lanGroupMap.get(key)!

    lg.groupTop = curGroupY
    let curMachY = curGroupY + LAN_HEADER_H + LAN_PAD

    for (const machine of g.machines) {
      const mt = tunnels.filter(t => t.machine_id === machine.id)
      const bh = machineBoxH(mt.length)
      const ml: MachLayout = { machine, tunnels: mt, top: curMachY, boxH: bh }
      lg.layouts.push(ml)
      allMachLayouts.push(ml)
      curMachY += bh + MACH_GAP
    }

    curGroupY += lg.groupH + LAN_GAP
  }

  // VPS — centred
  const vpsTop = (svgH - vpsH) / 2

  // Precompute tunnel line endpoints so connector dots are pixel-perfect
  const portSectionStart = vpsTop + VPS_HEADER + 10
  const tunnelLines: TunnelLine[] = []
  let vpsCur = portSectionStart

  for (const { machine, tunnels: mt, top } of allMachLayouts) {
    if (mt.length === 0) continue
    vpsCur += 16  // group label
    mt.forEach((t, j) => {
      tunnelLines.push({
        tunnel: t,
        vpsY: vpsCur + ROW_H / 2,
        machY: top + MACH_HEADER + j * ROW_H + ROW_H / 2,
      })
      vpsCur += tunnelVpsRowH(t)
    })
    void machine
    vpsCur += 6
  }

  const selectedMachine = machines.find(m => m.id === selectedId) ?? null
  const selectedTunnels = selectedMachine ? tunnels.filter(t => t.machine_id === selectedMachine.id) : []
  const activeMachines  = machines.filter(m => m.status === 'active' || m.status === 'connected').length
  const totalPorts      = new Set(tunnels.map(t => t.rathole_port)).size
  const midX            = (VPS_RIGHT + MACHINE_X) / 2

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

      {/* Diagram */}
      <div className="bg-white rounded-xl border shadow-sm overflow-x-auto">
        <svg viewBox={`0 0 ${SVG_WIDTH} ${svgH}`} className="w-full" style={{ minHeight: 220 }}>
          <defs>
            <pattern id="dots" x="0" y="0" width="24" height="24" patternUnits="userSpaceOnUse">
              <circle cx="1" cy="1" r="1" fill="#e5e7eb" />
            </pattern>
          </defs>
          <rect width={SVG_WIDTH} height={svgH} fill="url(#dots)" />

          {/* ── Tunnel lines (behind everything) ─────────────────────────── */}
          {tunnelLines.map(({ tunnel, vpsY, machY }) => {
            const active   = tunnel.status === 'active'
            const idle = tunnel.status === 'idle'
            const isUdp = tunnel.transport === 'udp'
            const cy = (vpsY + machY) / 2
            const label = `:${tunnel.rathole_port}`
            const lw = label.length * 6.2 + 10
            // Degraded: tunnel IS connected so line is green, but pill is amber to
            // signal the service isn't responding on the client side.
            const lineColor  = (active || idle) ? (isUdp ? '#a855f7' : '#4ade80') : '#d1d5db'
            const pillFill   = active ? (isUdp ? '#faf5ff' : '#f0fdf4') : idle ? '#fffbeb' : '#f9fafb'
            const pillStroke = active ? (isUdp ? '#a855f7' : '#4ade80') : idle ? '#f59e0b' : '#d1d5db'
            const textColor  = active ? (isUdp ? '#7e22ce' : '#16a34a') : idle ? '#b45309' : '#6b7280'
            return (
              <g key={tunnel.id}>
                <path
                  d={`M ${VPS_RIGHT} ${vpsY} C ${midX} ${vpsY}, ${midX} ${machY}, ${MACHINE_X} ${machY}`}
                  fill="none"
                  stroke={lineColor}
                  strokeWidth={(active || idle) ? 2.5 : 1.5}
                  strokeDasharray={active || idle ? undefined : '5 4'}
                  opacity={0.9}
                />
                <g style={{ cursor: idle ? 'help' : 'default' }}>
                  {idle && (
                    <title>Tunnel connected — nothing is listening on port {tunnel.local_port} on the client machine. Start the service or check the configured local port.</title>
                  )}
                  <rect x={midX - lw / 2} y={cy - 9} width={lw} height={17} rx={4}
                    fill={pillFill} stroke={pillStroke} strokeWidth={1} />
                  <text x={midX} y={cy + 4} textAnchor="middle"
                    fontSize={9.5} fontFamily="ui-monospace,monospace" fontWeight={700}
                    fill={textColor}>
                    {label}
                  </text>
                </g>
              </g>
            )
          })}

          {/* ── LAN group boxes (behind machine boxes, in front of lines) ── */}
          {computedLanGroups.map((lg, gi) => {
            // Only show the outer LAN box when we know the public IP
            if (!lg.publicIP) return null
            const lx = MACHINE_X - 14
            const lw = MACHINE_W + 28
            const natColor = lg.isNat ? '#f59e0b' : '#6366f1'
            const natFill  = lg.isNat ? '#fffbeb' : '#eef2ff'
            const natBorder = lg.isNat ? '#fbbf24' : '#818cf8'

            return (
              <g key={`lan-${gi}`}>
                {/* Outer group box */}
                <rect x={lx} y={lg.groupTop} width={lw} height={lg.groupH}
                  rx={11} fill={natFill} stroke={natBorder}
                  strokeWidth={1.5} strokeDasharray={lg.isNat ? '6 3' : undefined}
                  opacity={0.5}
                />
                {/* Public IP badge in header */}
                <circle cx={lx + 14} cy={lg.groupTop + LAN_HEADER_H / 2} r={4}
                  fill={natColor} />
                <text x={lx + 24} y={lg.groupTop + LAN_HEADER_H / 2 + 4}
                  fontSize={9.5} fontFamily="ui-monospace,monospace" fontWeight={700}
                  fill={natColor}>
                  {lg.publicIP}
                </text>
                {lg.isNat && (
                  <text
                    x={lx + 24 + lg.publicIP.length * 6 + 6}
                    y={lg.groupTop + LAN_HEADER_H / 2 + 4}
                    fontSize={8.5} fontFamily="ui-sans-serif,system-ui,sans-serif"
                    fill="#d97706" opacity={0.85}>
                    NAT
                  </text>
                )}
              </g>
            )
          })}

          {/* ── VPS box ──────────────────────────────────────────────────── */}
          {domain ? (
            <g>
              <rect x={VPS_X - 4} y={vpsTop - 4} width={VPS_W + 8} height={vpsH + 8}
                rx={16} fill="none" stroke="#818cf8" strokeWidth={1.5} opacity={0.28} />
              <rect x={VPS_X} y={vpsTop} width={VPS_W} height={vpsH}
                rx={12} fill="#eef2ff" stroke="#6366f1" strokeWidth={2} />

              {/* Title */}
              <text x={VPS_X + VPS_W / 2} y={vpsTop + 17} textAnchor="middle"
                fontSize={10} fontWeight={700} fill="#4338ca" letterSpacing={1.5}
                fontFamily="ui-sans-serif,system-ui,sans-serif">
                VPS / SERVER
              </text>
              <circle cx={VPS_X + VPS_W - 14} cy={vpsTop + 13} r={5} fill="#22c55e" />

              {/* Public IP line (resolved) */}
              <text x={VPS_X + 12} y={vpsTop + 32} fontSize={11} fill="#4338ca"
                fontFamily="ui-monospace,monospace" fontWeight={600}>
                {vpsPublicIP.length > 25 ? vpsPublicIP.slice(0, 23) + '…' : vpsPublicIP}
              </text>
              {/* vpsHost (if it's a hostname different from the resolved IP, show both) */}
              {vpsHost && vpsHost !== vpsPublicIP && (
                <text x={VPS_X + 12} y={vpsTop + 46} fontSize={9.5} fill="#818cf8"
                  fontFamily="ui-monospace,monospace" opacity={0.8}>
                  {vpsHost.length > 28 ? vpsHost.slice(0, 26) + '…' : vpsHost}
                </text>
              )}
              {/* 0.0.0.0 · domain */}
              <text x={VPS_X + 12} y={vpsTop + (vpsHost && vpsHost !== vpsPublicIP ? 60 : 47)}
                fontSize={10} fill="#6366f1" fontFamily="ui-monospace,monospace">
                {(() => { const ip = routerIP || domainIP || '…'; const h = displayHost; return `${ip} · ${h.length > 22 ? h.slice(0, 20) + '…' : h}` })()}
              </text>

              {/* Port section divider */}
              {tunnelLines.length > 0 && (
                <line x1={VPS_X + 10} y1={portSectionStart - 6}
                  x2={VPS_X + VPS_W - 10} y2={portSectionStart - 6}
                  stroke="#c7d2fe" strokeWidth={1} strokeDasharray="3 2" />
              )}

              {/* VPS port rows — one per tunnel, grouped by machine */}
              {(() => {
                const rows: React.ReactNode[] = []
                let cur = portSectionStart

                for (const { machine, tunnels: mt } of allMachLayouts) {
                  if (mt.length === 0) continue
                  const s = statusStyle(machine.status)

                  rows.push(
                    <text key={`gh-${machine.id}`}
                      x={VPS_X + 12} y={cur + 11} fontSize={9} fontWeight={600}
                      fill={s.text} fontFamily="ui-sans-serif,system-ui,sans-serif" opacity={0.7}>
                      {machine.name.length > 22 ? machine.name.slice(0, 20) + '…' : machine.name}
                    </text>
                  )
                  cur += 16

                  for (const t of mt) {
                    const ry = cur + ROW_H / 2
                    const active = t.status === 'active'
                    const name = t.name.length > 16 ? t.name.slice(0, 14) + '…' : t.name
                    const url = t.subdomain && domain ? `${t.subdomain}.${domain}` : null
                    const urlLabel = url && url.length > 28 ? url.slice(0, 26) + '…' : url
                    rows.push(
                      <g key={`vr-${t.id}`}>
                        <text x={VPS_X + 12} y={ry + 4} fontSize={9.5} fill="#374151"
                          fontFamily="ui-monospace,monospace">{name}</text>
                        <text x={VPS_RIGHT - 16} y={ry + 4} textAnchor="end" fontSize={10}
                          fill={t.transport === 'udp' ? '#7e22ce' : '#3730a3'}
                          fontFamily="ui-monospace,monospace" fontWeight={700}>
                          :{t.rathole_port}
                        </text>
                        <circle cx={VPS_RIGHT} cy={ry} r={4.5}
                          fill={active ? '#22c55e' : '#d1d5db'} stroke="white" strokeWidth={1.5} />
                        {urlLabel && (
                          <text x={VPS_X + 14} y={cur + ROW_H + 9} fontSize={8.5} fill="#6366f1"
                            fontFamily="ui-monospace,monospace" opacity={0.9}>{urlLabel}</text>
                        )}
                      </g>
                    )
                    cur += tunnelVpsRowH(t)
                  }
                  cur += 6
                }
                return rows
              })()}
            </g>
          ) : (
            <g>
              <rect x={VPS_X} y={(svgH - 64) / 2} width={VPS_W} height={64}
                rx={10} fill="#f3f4f6" stroke="#d1d5db" strokeWidth={2} />
              <text x={VPS_X + VPS_W / 2} y={svgH / 2 + 5} textAnchor="middle"
                fontSize={12} fill="#6b7280" fontFamily="ui-sans-serif,system-ui,sans-serif">
                VPS (not configured)
              </text>
            </g>
          )}

          {/* ── Machine boxes ─────────────────────────────────────────────── */}
          {allMachLayouts.map(({ machine, tunnels: mt, top, boxH: bh }) => {
            const s = statusStyle(machine.status)
            const sel = selectedId === machine.id
            const info = netInfoMap.get(machine.id)
            const dHost = machine.host
              ? (machine.host.length > 20 ? machine.host.slice(0, 18) + '…' : machine.host)
              : null
            const showPrivateBadge = info && isPrivateIP(machine.host ?? '')
            const showPublicIP = info?.public_ip && info.public_ip !== machine.host

            return (
              <g key={machine.id}
                onClick={() => setSelectedId(sel ? null : machine.id)}
                style={{ cursor: 'pointer' }}>

                {sel && (
                  <rect x={MACHINE_X - 5} y={top - 5} width={MACHINE_W + 10} height={bh + 10}
                    rx={14} fill="none" stroke="#6366f1" strokeWidth={2.5} opacity={0.55} />
                )}
                <rect x={MACHINE_X} y={top} width={MACHINE_W} height={bh}
                  rx={9} fill={s.fill} stroke={s.stroke} strokeWidth={1.75} />
                <circle cx={MACHINE_X + MACHINE_W - 11} cy={top + 11} r={5} fill={s.dot} />

                {/* Name */}
                <text x={MACHINE_X + 12} y={top + 19} textAnchor="start"
                  fontSize={13} fontWeight={700} fill={s.text}
                  fontFamily="ui-sans-serif,system-ui,sans-serif">
                  {machine.name.length > 18 ? machine.name.slice(0, 16) + '…' : machine.name}
                </text>

                {/* Private IP / hostname */}
                {dHost && (
                  <g>
                    <text x={MACHINE_X + 12} y={top + 35} textAnchor="start"
                      fontSize={10.5} fill={s.text} fontFamily="ui-monospace,monospace" fontWeight={600}>
                      {dHost}
                    </text>
                    {showPrivateBadge && (
                      <text x={MACHINE_X + 12 + dHost.length * 6.3 + 5} y={top + 35}
                        fontSize={8.5} fill="#9ca3af" fontFamily="ui-sans-serif,system-ui,sans-serif"
                        fontStyle="italic">
                        private
                      </text>
                    )}
                  </g>
                )}

                {/* Public IP (if different from host) */}
                {showPublicIP && (
                  <text x={MACHINE_X + 12} y={top + 50} textAnchor="start"
                    fontSize={9.5} fill="#6366f1" fontFamily="ui-monospace,monospace">
                    {`↑ ${info!.public_ip}`}
                  </text>
                )}

                {/* Username */}
                <text x={MACHINE_X + 12}
                  y={top + (showPublicIP ? 63 : dHost ? 50 : 36)}
                  textAnchor="start" fontSize={10} fill={s.text}
                  fontFamily="ui-sans-serif,system-ui,sans-serif" opacity={0.72}>
                  @{machine.username.length > 16 ? machine.username.slice(0, 14) + '…' : machine.username}
                </text>

                {/* Header / port-rows divider */}
                {mt.length > 0 && (
                  <line x1={MACHINE_X + 8} y1={top + MACH_HEADER}
                    x2={MACHINE_X + MACHINE_W - 8} y2={top + MACH_HEADER}
                    stroke={s.stroke} strokeWidth={1} opacity={0.35} />
                )}

                {/* Port rows */}
                {mt.map((t, j) => {
                  const ry = top + MACH_HEADER + j * ROW_H + ROW_H / 2
                  const active = t.status === 'active'
                  const name = t.name.length > 13 ? t.name.slice(0, 11) + '…' : t.name
                  return (
                    <g key={`mr-${t.id}`}>
                      <circle cx={MACHINE_X} cy={ry} r={4.5}
                        fill={active ? '#22c55e' : '#d1d5db'} stroke="white" strokeWidth={1.5} />
                      <text x={MACHINE_X + 12} y={ry + 4} textAnchor="start"
                        fontSize={10} fill="#14532d" fontFamily="ui-monospace,monospace" fontWeight={700}>
                        :{t.local_port}
                      </text>
                      <text x={MACHINE_X + 55} y={ry + 4} textAnchor="start"
                        fontSize={9.5} fill="#374151" fontFamily="ui-monospace,monospace">
                        {name}
                      </text>
                      <text x={MACHINE_X + MACHINE_W - 10} y={ry + 4} textAnchor="end"
                        fontSize={9} fill={t.transport === 'udp' ? '#7e22ce' : s.text}
                        fontFamily="ui-sans-serif,system-ui,sans-serif" opacity={0.8}>
                        {t.transport === 'udp' ? 'UDP' : t.protocol.toUpperCase()}
                      </text>
                    </g>
                  )
                })}

                {mt.length === 0 && (
                  <text x={MACHINE_X + MACHINE_W / 2} y={top + MACH_HEADER + 8}
                    textAnchor="middle" fontSize={10} fill="#9ca3af"
                    fontFamily="ui-sans-serif,system-ui,sans-serif">
                    no tunnels
                  </text>
                )}
              </g>
            )
          })}

          {machines.length === 0 && (
            <text x={SVG_WIDTH / 2} y={svgH / 2 + 4} textAnchor="middle"
              fontSize={14} fill="#9ca3af" fontFamily="ui-sans-serif,system-ui,sans-serif">
              No machines registered yet
            </text>
          )}
        </svg>
      </div>

      {/* Selected machine detail */}
      {selectedMachine && (
        <div className="bg-white rounded-xl border shadow-sm overflow-hidden">
          <div className="px-5 py-4 flex items-center justify-between border-b bg-gray-50">
            <div className="flex items-center gap-3">
              <div className={`w-2.5 h-2.5 rounded-full shrink-0 ${
                selectedMachine.status === 'active' || selectedMachine.status === 'connected' ? 'bg-green-500'
                : selectedMachine.status === 'pending' || selectedMachine.status === 'connecting' ? 'bg-yellow-500'
                : 'bg-gray-300'}`} />
              <span className="font-bold text-gray-900">{selectedMachine.name}</span>
              {(() => {
                const info = netInfoMap.get(selectedMachine.id)
                const privateIP = info?.private_ip || (isPrivateIP(selectedMachine.host ?? '') ? selectedMachine.host : null)
                const directPort = selectedMachine.port && selectedMachine.port > 0 ? `:${selectedMachine.port}` : ''
                if (privateIP) return (
                  <span className="text-xs text-gray-600 font-mono bg-gray-100 px-2 py-0.5 rounded">
                    {privateIP}{directPort}
                  </span>
                )
                if (directPort) return (
                  <span className="text-xs text-gray-600 font-mono bg-gray-100 px-2 py-0.5 rounded">
                    {selectedMachine.host}{directPort}
                  </span>
                )
                return null
              })()}
              {netInfoMap.get(selectedMachine.id)?.public_ip && (
                <span className="text-xs text-indigo-600 font-mono bg-indigo-50 border border-indigo-200 px-2 py-0.5 rounded">
                  ↑ {netInfoMap.get(selectedMachine.id)!.public_ip}
                </span>
              )}
              <span className="text-sm text-gray-500">@{selectedMachine.username}</span>
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
            {selectedTunnels.length === 0
              ? <p className="text-sm text-gray-400 py-2">No tunnels configured for this machine.</p>
              : selectedTunnels.map(t => <TunnelDetailRow key={t.id} tunnel={t} domain={domain} />)
            }
          </div>
        </div>
      )}

      {/* Legend */}
      <div className="flex flex-wrap items-center gap-4 text-xs text-gray-500 bg-white border rounded-lg px-4 py-3">
        <span className="font-semibold text-gray-700">Legend:</span>
        <span className="flex items-center gap-1.5"><span className="w-3 h-3 rounded-full bg-green-500 inline-block" /> Connected</span>
        <span className="flex items-center gap-1.5"><span className="w-3 h-3 rounded-full bg-yellow-400 inline-block" /> Connecting</span>
        <span className="flex items-center gap-1.5"><span className="w-3 h-3 rounded-full bg-gray-300 inline-block" /> Offline</span>
        <span className="flex items-center gap-1.5"><span className="inline-block w-6 border-t-2 border-green-400" /> Tunnel active</span>
        <span className="flex items-center gap-1.5">
          <span className="inline-flex items-center gap-0.5">
            <span className="inline-block w-4 border-t-2 border-green-400" />
            <span className="text-[9px] font-bold px-1 rounded bg-amber-100 text-amber-700 border border-amber-300">:port</span>
          </span>
          Tunnel up, nothing listening (hover for details)
        </span>
        <span className="flex items-center gap-1.5"><span className="inline-block w-6 border-t-2 border-dashed border-gray-300" /> No client connected</span>
        <span className="flex items-center gap-1.5">
          <span className="inline-block w-4 h-3 rounded border-2 border-dashed border-yellow-400 bg-yellow-50 opacity-70" />
          Shared network (behind NAT)
        </span>
        <span className="flex items-center gap-1.5">
          <span className="inline-block w-4 h-3 rounded border-2 border-indigo-400 bg-indigo-50 opacity-70" />
          Shared network (public IP)
        </span>
        <span className="ml-auto text-gray-400 italic">Click a machine to view details</span>
      </div>
    </div>
  )
}
