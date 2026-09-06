import { useQuery, useQueries } from '@tanstack/react-query'
import { machinesApi } from '../api/machines'
import { tunnelsApi } from '../api/tunnels'
import { localApi } from '../api/local'
import { vpsApi } from '../api/vps'
import type { Machine, Tunnel } from '../types'
import { statusHex, statusBg, statusPillClasses, statusBucket, isHealthyStatus } from '../lib/statusColor'
import { useState, useMemo, useRef, useEffect } from 'react'
import {
  Search, ZoomIn, ZoomOut, Maximize2,
  ArrowLeft, Globe, Shield, PowerOff, Network as NetworkIcon, ClipboardCopy,
} from 'lucide-react'

// ── Helpers ──────────────────────────────────────────────────────────────────
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

function isOnlineMachine(m: Machine): boolean {
  return isHealthyStatus(m.status)
}

// Delegates to lib/statusColor — was its own switch that fell through to
// neutral gray for offline/pending/config-error, so a down tunnel looked
// the same as one with nothing to report.
function tunnelStatusDot(status: string): string {
  return statusBg(status)
}

// ── Types ────────────────────────────────────────────────────────────────────
type NetInfo = { public_ip: string; private_ip: string; is_nat: boolean }

interface LanCardData {
  key: string
  publicIP: string         // '' for solo / unknown
  isNat: boolean
  machines: Machine[]
  isOffline: boolean       // true for the offline bucket
  // For the offline card: machines grouped by their last-known LAN.
  offlineSubGroups?: { lastKnownIP: string; isNat: boolean; machines: Machine[] }[]
}

// ── Page ─────────────────────────────────────────────────────────────────────
export default function NetworkMapPage() {
  const [searchTerm, setSearchTerm] = useState('')
  const [detailLanKey, setDetailLanKey] = useState<string | null>(null)

  const { data: machinesData } = useQuery({ queryKey: ['machines'], queryFn: () => machinesApi.list(), refetchInterval: 30000 })
  const { data: tunnelsData }  = useQuery({ queryKey: ['tunnels'],  queryFn: () => tunnelsApi.list(), refetchInterval: 30000 })
  const { data: localStatus }  = useQuery({ queryKey: ['local-status'], queryFn: () => localApi.status(), refetchInterval: 30000 })
  const { data: vpsData }      = useQuery({ queryKey: ['vps'], queryFn: () => vpsApi.get(), refetchInterval: 60000 })

  const machines: Machine[] = useMemo(() => machinesData?.data ?? [], [machinesData])
  const tunnels: Tunnel[]   = useMemo(() => tunnelsData?.data ?? [], [tunnelsData])
  const domain = localStatus?.domain ?? ''
  const vpsHost = vpsData?.data?.host ?? ''

  const { data: vpsIPData } = useQuery({
    queryKey: ['resolve-ip', vpsHost],
    queryFn: () => localApi.resolveIP(vpsHost),
    enabled: !!vpsHost,
    staleTime: 10 * 60 * 1000,
  })
  const vpsPublicIP = vpsIPData?.ip ?? vpsHost

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
  // The edge transport host is the backend's ServerHost (defaults to
  // router.<domain>); the IP queries above stay only to annotate the map node.
  const displayHost = localStatus?.server_host || routerHost || domain

  const netInfoResults = useQueries({
    queries: machines.map(m => ({
      queryKey: ['network-info', m.id],
      queryFn: () => machinesApi.networkInfo(m.id),
      enabled: isOnlineMachine(m),
      staleTime: 10 * 60 * 1000,
      retry: 1,
    })),
  })

  const netInfoMap = useMemo(() => {
    const map = new Map<string, NetInfo>()
    machines.forEach((m, i) => {
      const fresh = netInfoResults[i]?.data
      if (fresh && fresh.public_ip) {
        map.set(m.id, { public_ip: fresh.public_ip, private_ip: fresh.private_ip, is_nat: fresh.is_nat })
      } else if (m.public_ip && isOnlineMachine(m)) {
        map.set(m.id, { public_ip: m.public_ip, private_ip: m.host ?? '', is_nat: isPrivateIP(m.host ?? '') })
      }
    })
    return map
  }, [machines, netInfoResults])

  // ── Filter ─────────────────────────────────────────────────────────────────
  const filteredMachines = useMemo(() => {
    const term = searchTerm.trim().toLowerCase()
    if (!term) return machines
    return machines.filter(m => {
      if (m.name.toLowerCase().includes(term)) return true
      if ((m.host ?? '').toLowerCase().includes(term)) return true
      if ((m.username ?? '').toLowerCase().includes(term)) return true
      const mt = tunnels.filter(t => t.machine_id === m.id)
      return mt.some(t =>
        t.name.toLowerCase().includes(term)
        || (t.subdomain ?? '').toLowerCase().includes(term)
        || String(t.rathole_port) === term
        || String(t.local_port) === term
      )
    })
  }, [machines, tunnels, searchTerm])

  // ── Group machines into LAN cards ──────────────────────────────────────────
  // Online machines: bucketed by current public IP. Offline machines: collected
  // into a single "Offline" card, sub-grouped by their last-known public IP
  // (Machine.public_ip persists across reconnects so we always know where they came from).
  const lanCards: LanCardData[] = useMemo(() => {
    const onlineMap = new Map<string, { publicIP: string; isNat: boolean; machines: Machine[] }>()
    const onlineOrder: string[] = []
    const offlineByLastLan = new Map<string, { lastKnownIP: string; isNat: boolean; machines: Machine[] }>()

    for (const m of filteredMachines) {
      const online = isOnlineMachine(m)
      const info = netInfoMap.get(m.id)
      const publicIP = info?.public_ip ?? m.public_ip ?? ''
      const isNat = info?.is_nat ?? isPrivateIP(m.host ?? '')

      if (!online) {
        const key = publicIP || 'unknown'
        if (!offlineByLastLan.has(key)) {
          offlineByLastLan.set(key, { lastKnownIP: publicIP, isNat, machines: [] })
        }
        offlineByLastLan.get(key)!.machines.push(m)
        continue
      }

      const key = publicIP || `solo-${m.id}`
      if (!onlineMap.has(key)) {
        onlineMap.set(key, { publicIP, isNat, machines: [] })
        onlineOrder.push(key)
      } else if (isNat) {
        onlineMap.get(key)!.isNat = true
      }
      onlineMap.get(key)!.machines.push(m)
    }

    const cards: LanCardData[] = onlineOrder.map(key => {
      const g = onlineMap.get(key)!
      return {
        key,
        publicIP: g.publicIP,
        isNat: g.isNat,
        machines: g.machines,
        isOffline: false,
      }
    })

    if (offlineByLastLan.size > 0) {
      const subGroups = Array.from(offlineByLastLan.values())
      cards.push({
        key: '__offline__',
        publicIP: '',
        isNat: false,
        machines: subGroups.flatMap(g => g.machines),
        isOffline: true,
        offlineSubGroups: subGroups,
      })
    }

    return cards
  }, [filteredMachines, netInfoMap])

  // Small-system shortcut: if there's only one LAN, skip the overview entirely
  // and render the detail view as the default.
  const onlyLan = lanCards.length === 1 ? lanCards[0] : null
  const explicitDetail = detailLanKey ? lanCards.find(c => c.key === detailLanKey) ?? null : null
  const detailCard = explicitDetail ?? onlyLan
  const inDetail = !!detailCard
  const showBackButton = inDetail && !onlyLan

  // Header stats
  const activeMachines = machines.filter(isOnlineMachine).length
  const totalPorts = new Set(tunnels.map(t => t.rathole_port)).size

  // ── Render ─────────────────────────────────────────────────────────────────
  return (
    <div className="space-y-5">
      {/* Header */}
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-bold text-gray-900">Network Map</h1>
          <p className="text-gray-500 mt-1">
            {inDetail
              ? 'Topology view for a single network'
              : 'Networks reachable through this server'}
          </p>
        </div>
        <div className="flex gap-4 text-sm text-gray-500 pt-1">
          <span><strong className="text-gray-800">{activeMachines}</strong>/<strong className="text-gray-800">{machines.length}</strong> online</span>
          <span><strong className="text-gray-800">{tunnels.length}</strong> tunnels</span>
          <span><strong className="text-gray-800">{totalPorts}</strong> ports mapped</span>
        </div>
      </div>

      {/* Toolbar */}
      <div className="flex items-center gap-2 flex-wrap">
        {showBackButton && (
          <button
            onClick={() => setDetailLanKey(null)}
            className="px-3 py-2 text-sm border border-gray-300 rounded-lg hover:bg-gray-50 flex items-center gap-1.5 text-gray-700"
          >
            <ArrowLeft size={14} /> All networks
          </button>
        )}
        <div className="relative flex-1 max-w-sm">
          <Search size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
          <input
            type="text"
            value={searchTerm}
            onChange={e => setSearchTerm(e.target.value)}
            placeholder="Search machines, tunnels, ports..."
            className="w-full pl-9 pr-3 py-2 border border-gray-300 rounded-lg text-sm focus:ring-2 focus:ring-blue-500 focus:outline-none"
          />
        </div>
      </div>

      {detailCard ? (
        <DetailView
          card={detailCard}
          tunnels={tunnels}
          netInfoMap={netInfoMap}
          domain={domain}
          vpsPublicIP={vpsPublicIP}
          vpsHost={vpsHost}
          domainIP={domainIP}
          routerIP={routerIP}
          displayHost={displayHost}
        />
      ) : (
        <OverviewSVG
          cards={lanCards}
          tunnels={tunnels}
          totalMachines={machines.length}
          searchTerm={searchTerm}
          onOpen={(key) => setDetailLanKey(key)}
          vpsPublicIP={vpsPublicIP}
          vpsHost={vpsHost}
          domainIP={domainIP}
          routerIP={routerIP}
          displayHost={displayHost}
          domain={domain}
        />
      )}
    </div>
  )
}

// ── Overview SVG: VPS on left, LAN subsections on right with curves ─────────
const OV_SVG_W = 980
const OV_VPS_X = 16
const OV_VPS_W = 250
const OV_VPS_RIGHT = OV_VPS_X + OV_VPS_W
const OV_LAN_X = 660
const OV_LAN_W = 305
const OV_LAN_HEADER_H = 44
const OV_LAN_MACHINE_ROW_H = 24
const OV_LAN_PAD_Y = 10
const OV_LAN_GAP = 14
const OV_VPS_BOX_H = 110
const OV_V_PAD = 32

function ovLanBoxH(machineCount: number): number {
  if (machineCount === 0) return OV_LAN_HEADER_H + 28
  return OV_LAN_HEADER_H + machineCount * OV_LAN_MACHINE_ROW_H + OV_LAN_PAD_Y * 2
}

function OverviewSVG({
  cards, tunnels, totalMachines, searchTerm, onOpen,
  vpsPublicIP, vpsHost, domainIP, routerIP, displayHost, domain,
}: {
  cards: LanCardData[]
  tunnels: Tunnel[]
  totalMachines: number
  searchTerm: string
  onOpen: (key: string) => void
  vpsPublicIP: string
  vpsHost: string
  domainIP: string
  routerIP: string
  displayHost: string
  domain: string
}) {
  const [transform, setTransform] = useState({ scale: 1, tx: 0, ty: 0 })
  const [isDragging, setIsDragging] = useState(false)
  const svgRef = useRef<SVGSVGElement>(null)
  const svgHRef = useRef(0)
  const dragRef = useRef<{ clientX: number; clientY: number; startTx: number; startTy: number; moved: boolean } | null>(null)

  // Layout LAN boxes vertically on the right
  const layouts = cards.map((card) => ({ card, top: 0, h: ovLanBoxH(card.machines.length) }))
  const lansTotalH = layouts.reduce((s, l) => s + l.h, 0) + Math.max(0, layouts.length - 1) * OV_LAN_GAP
  const svgH = Math.max(lansTotalH + OV_V_PAD * 2, OV_VPS_BOX_H + OV_V_PAD * 2, 280)
  svgHRef.current = svgH

  const lansTop = (svgH - lansTotalH) / 2
  let cur = lansTop
  for (const l of layouts) {
    l.top = cur
    cur += l.h + OV_LAN_GAP
  }

  const vpsTop = (svgH - OV_VPS_BOX_H) / 2
  const vpsCenterY = vpsTop + OV_VPS_BOX_H / 2

  // For curve label distribution, vary vpsY a bit per LAN so curves don't all
  // emanate from the same point when there are many LANs.
  const vpsAnchorTop = vpsTop + 30
  const vpsAnchorBot = vpsTop + OV_VPS_BOX_H - 14
  const vpsAnchorY = (i: number) => {
    if (layouts.length <= 1) return vpsCenterY
    const t = layouts.length === 1 ? 0.5 : i / (layouts.length - 1)
    return vpsAnchorTop + t * (vpsAnchorBot - vpsAnchorTop)
  }

  const midX = (OV_VPS_RIGHT + OV_LAN_X) / 2

  // Pan/zoom
  const zoomIn = () => setTransform(t => ({ ...t, scale: Math.min(t.scale * 1.25, 5) }))
  const zoomOut = () => setTransform(t => ({ ...t, scale: Math.max(t.scale * 0.8, 0.3) }))
  const resetView = () => setTransform({ scale: 1, tx: 0, ty: 0 })

  useEffect(() => {
    const svg = svgRef.current
    if (!svg) return
    const handler = (e: WheelEvent) => {
      e.preventDefault()
      const rect = svg.getBoundingClientRect()
      const sx = (e.clientX - rect.left) * (OV_SVG_W / rect.width)
      const sy = (e.clientY - rect.top) * (svgHRef.current / rect.height)
      const factor = e.deltaY > 0 ? 0.9 : 1.1
      setTransform(t => {
        const newScale = Math.max(0.3, Math.min(5, t.scale * factor))
        const ratio = newScale / t.scale
        return { scale: newScale, tx: sx - (sx - t.tx) * ratio, ty: sy - (sy - t.ty) * ratio }
      })
    }
    svg.addEventListener('wheel', handler, { passive: false })
    return () => svg.removeEventListener('wheel', handler)
  }, [])

  const onPointerDown = (e: React.PointerEvent<SVGSVGElement>) => {
    if ((e.target as Element).closest?.('[data-clickable]')) return
    e.currentTarget.setPointerCapture(e.pointerId)
    dragRef.current = { clientX: e.clientX, clientY: e.clientY, startTx: transform.tx, startTy: transform.ty, moved: false }
    setIsDragging(true)
  }
  const onPointerMove = (e: React.PointerEvent<SVGSVGElement>) => {
    const drag = dragRef.current
    if (!drag) return
    const dx = e.clientX - drag.clientX
    const dy = e.clientY - drag.clientY
    if (Math.hypot(dx, dy) > 3) drag.moved = true
    const rect = e.currentTarget.getBoundingClientRect()
    const svgDx = dx * (OV_SVG_W / rect.width)
    const svgDy = dy * (svgHRef.current / rect.height)
    const newTx = drag.startTx + svgDx
    const newTy = drag.startTy + svgDy
    setTransform(t => ({ ...t, tx: newTx, ty: newTy }))
  }
  const onPointerUp = () => {
    dragRef.current = null
    setIsDragging(false)
  }

  if (totalMachines === 0) {
    return (
      <div className="bg-white rounded-xl border shadow-sm p-12 text-center text-gray-500">
        <NetworkIcon className="w-12 h-12 mx-auto mb-3 text-gray-300" />
        <p className="text-base font-semibold text-gray-700">No machines registered yet</p>
        <p className="text-sm mt-1 text-gray-400">Bootstrap a machine to see it here.</p>
      </div>
    )
  }
  if (cards.length === 0 && searchTerm.trim()) {
    return (
      <div className="bg-white rounded-xl border shadow-sm p-12 text-center text-gray-500">
        No machines match "<span className="font-mono">{searchTerm}</span>"
      </div>
    )
  }

  return (
    <div className="bg-white rounded-xl border shadow-sm overflow-hidden">
      {/* Mini-toolbar */}
      <div className="px-4 py-2 border-b bg-gray-50 flex items-center justify-between text-xs text-gray-500">
        <span>Click a network to drill in · scroll to zoom · drag empty space to pan</span>
        <div className="flex items-center gap-1 border border-gray-300 rounded-lg px-1 py-0.5 bg-white">
          <button onClick={zoomOut} className="p-1 hover:bg-gray-100 rounded text-gray-600" title="Zoom out"><ZoomOut size={12} /></button>
          <span className="text-xs text-gray-500 px-1.5 font-mono w-10 text-center">{Math.round(transform.scale * 100)}%</span>
          <button onClick={zoomIn} className="p-1 hover:bg-gray-100 rounded text-gray-600" title="Zoom in"><ZoomIn size={12} /></button>
          <div className="w-px h-4 bg-gray-200 mx-0.5" />
          <button onClick={resetView} className="p-1 hover:bg-gray-100 rounded text-gray-600" title="Reset view"><Maximize2 size={12} /></button>
        </div>
      </div>

      <svg
        ref={svgRef}
        viewBox={`0 0 ${OV_SVG_W} ${svgH}`}
        className="w-full select-none"
        style={{ minHeight: 280, cursor: isDragging ? 'grabbing' : 'grab', touchAction: 'none' }}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerUp}
      >
        <defs>
          <pattern id="ov-dots" x="0" y="0" width="24" height="24" patternUnits="userSpaceOnUse">
            <circle cx="1" cy="1" r="1" fill="#e5e7eb" />
          </pattern>
        </defs>
        <rect width={OV_SVG_W} height={svgH} fill="url(#ov-dots)" />

        <g transform={`translate(${transform.tx} ${transform.ty}) scale(${transform.scale})`}>
          {/* Curves — VPS to each LAN, labelled with tunnel count */}
          {layouts.map((l, i) => {
            const lanCenterY = l.top + l.h / 2
            const tunnelCount = tunnels.filter(t => l.card.machines.some(m => m.id === t.machine_id)).length
            const aggStatus = tunnelAggStatus(l.card, tunnels)
            const hasUdp = tunnels.some(t => t.transport === 'udp' && l.card.machines.some(m => m.id === t.machine_id))
            const lineColor = aggStatus === 'active' ? (hasUdp ? '#a855f7' : '#4ade80')
              : aggStatus === 'idle' ? '#f59e0b' : '#d1d5db'
            const pillFill = aggStatus === 'active' ? (hasUdp ? '#faf5ff' : '#f0fdf4')
              : aggStatus === 'idle' ? '#fffbeb' : '#f9fafb'
            const pillStroke = aggStatus === 'active' ? (hasUdp ? '#a855f7' : '#4ade80')
              : aggStatus === 'idle' ? '#f59e0b' : '#d1d5db'
            const pillText = aggStatus === 'active' ? (hasUdp ? '#7e22ce' : '#16a34a')
              : aggStatus === 'idle' ? '#b45309' : '#6b7280'

            const vpsY = vpsAnchorY(i)
            const cy = (vpsY + lanCenterY) / 2
            const label = tunnelCount === 0 ? 'no tunnels' : `${tunnelCount} tunnel${tunnelCount === 1 ? '' : 's'}`
            const lw = label.length * 5.6 + 14

            return (
              <g key={`curve-${l.card.key}`}>
                <path
                  d={`M ${OV_VPS_RIGHT} ${vpsY} C ${midX} ${vpsY}, ${midX} ${lanCenterY}, ${OV_LAN_X} ${lanCenterY}`}
                  fill="none"
                  stroke={lineColor}
                  strokeWidth={aggStatus === 'offline' ? 1.5 : 3}
                  strokeDasharray={aggStatus === 'offline' ? '5 4' : undefined}
                  opacity={0.85}
                />
                <rect x={midX - lw / 2} y={cy - 9} width={lw} height={17} rx={8.5}
                  fill={pillFill} stroke={pillStroke} strokeWidth={1.25} />
                <text x={midX} y={cy + 4} textAnchor="middle"
                  fontSize={9.5} fontFamily="ui-sans-serif,system-ui,sans-serif" fontWeight={700}
                  fill={pillText}>
                  {label}
                </text>
              </g>
            )
          })}

          {/* VPS box */}
          {(domain || vpsHost) ? (
            <g>
              <rect x={OV_VPS_X - 4} y={vpsTop - 4} width={OV_VPS_W + 8} height={OV_VPS_BOX_H + 8}
                rx={16} fill="none" stroke="#818cf8" strokeWidth={1.5} opacity={0.28} />
              <rect x={OV_VPS_X} y={vpsTop} width={OV_VPS_W} height={OV_VPS_BOX_H}
                rx={12} fill="#eef2ff" stroke="#6366f1" strokeWidth={2} />
              <text x={OV_VPS_X + OV_VPS_W / 2} y={vpsTop + 17} textAnchor="middle"
                fontSize={10} fontWeight={700} fill="#4338ca" letterSpacing={1.5}
                fontFamily="ui-sans-serif,system-ui,sans-serif">
                VPS / SERVER
              </text>
              <circle cx={OV_VPS_X + OV_VPS_W - 14} cy={vpsTop + 13} r={5} fill="#22c55e" />
              <text x={OV_VPS_X + 12} y={vpsTop + 36} fontSize={11} fill="#4338ca"
                fontFamily="ui-monospace,monospace" fontWeight={600}>
                {(vpsPublicIP || 'unconfigured').length > 25
                  ? (vpsPublicIP || '').slice(0, 23) + '…'
                  : (vpsPublicIP || 'unconfigured')}
              </text>
              {vpsHost && vpsHost !== vpsPublicIP && (
                <text x={OV_VPS_X + 12} y={vpsTop + 50} fontSize={9.5} fill="#818cf8"
                  fontFamily="ui-monospace,monospace" opacity={0.8}>
                  {vpsHost.length > 28 ? vpsHost.slice(0, 26) + '…' : vpsHost}
                </text>
              )}
              <text x={OV_VPS_X + 12} y={vpsTop + (vpsHost && vpsHost !== vpsPublicIP ? 64 : 51)}
                fontSize={10} fill="#6366f1" fontFamily="ui-monospace,monospace">
                {(() => {
                  const ip = routerIP || domainIP || ''
                  const h = displayHost
                  return h ? `${ip ? ip + ' · ' : ''}${h.length > 22 ? h.slice(0, 20) + '…' : h}` : ''
                })()}
              </text>
              <text x={OV_VPS_X + 12} y={vpsTop + OV_VPS_BOX_H - 10}
                fontSize={9.5} fill="#6366f1" fontFamily="ui-sans-serif,system-ui,sans-serif"
                fontWeight={600} opacity={0.8}>
                {tunnels.length} tunnels · {layouts.length} {layouts.length === 1 ? 'network' : 'networks'}
              </text>
            </g>
          ) : (
            <g>
              <rect x={OV_VPS_X} y={vpsTop} width={OV_VPS_W} height={OV_VPS_BOX_H}
                rx={10} fill="#f3f4f6" stroke="#d1d5db" strokeWidth={2} />
              <text x={OV_VPS_X + OV_VPS_W / 2} y={vpsCenterY + 5} textAnchor="middle"
                fontSize={12} fill="#6b7280" fontFamily="ui-sans-serif,system-ui,sans-serif">
                VPS not configured
              </text>
            </g>
          )}

          {/* LAN subsection boxes — clickable */}
          {layouts.map(l => {
            const { card, top, h } = l
            const isOffline = card.isOffline
            const accent = isOffline ? '#6b7280' : card.isNat ? '#f59e0b' : '#6366f1'
            const headerFill = isOffline ? '#f3f4f6' : card.isNat ? '#fffbeb' : '#eef2ff'
            const dashed = isOffline || card.isNat
            const titleText = isOffline
              ? 'Offline'
              : card.publicIP || `solo · ${card.machines[0]?.name ?? ''}`
            const cardTunnelCount = tunnels.filter(t => card.machines.some(m => m.id === t.machine_id)).length
            return (
              <g key={`lan-${card.key}`}
                data-clickable="lan"
                style={{ cursor: 'pointer' }}
                onClick={() => onOpen(card.key)}>
                <title>Click to view {titleText}</title>

                {/* Outer box */}
                <rect x={OV_LAN_X} y={top} width={OV_LAN_W} height={h}
                  rx={11} fill="white" stroke={accent} strokeWidth={2}
                  strokeDasharray={dashed ? '6 3' : undefined} />

                {/* Header strip */}
                <rect x={OV_LAN_X} y={top} width={OV_LAN_W} height={OV_LAN_HEADER_H}
                  rx={11} fill={headerFill} stroke="none" />
                <rect x={OV_LAN_X} y={top + OV_LAN_HEADER_H - 12} width={OV_LAN_W} height={12}
                  fill={headerFill} stroke="none" />
                <line x1={OV_LAN_X} y1={top + OV_LAN_HEADER_H} x2={OV_LAN_X + OV_LAN_W} y2={top + OV_LAN_HEADER_H}
                  stroke={accent} strokeWidth={1} opacity={0.4} />

                {/* Header content */}
                <circle cx={OV_LAN_X + 16} cy={top + OV_LAN_HEADER_H / 2} r={5} fill={accent} />
                <text x={OV_LAN_X + 30} y={top + 19} fontSize={12} fontWeight={700}
                  fill={accent} fontFamily="ui-monospace,monospace">
                  {titleText.length > 26 ? titleText.slice(0, 24) + '…' : titleText}
                </text>
                <text x={OV_LAN_X + 30} y={top + 35} fontSize={9.5}
                  fill={accent} fontFamily="ui-sans-serif,system-ui,sans-serif" opacity={0.8}>
                  {card.machines.length} machine{card.machines.length === 1 ? '' : 's'} · {cardTunnelCount} tunnel{cardTunnelCount === 1 ? '' : 's'}
                </text>
                {card.isNat && !isOffline && (
                  <text x={OV_LAN_X + OV_LAN_W - 14} y={top + 19} textAnchor="end"
                    fontSize={9} fontWeight={700} fill="#d97706" letterSpacing={1}
                    fontFamily="ui-sans-serif,system-ui,sans-serif">
                    NAT
                  </text>
                )}
                <text x={OV_LAN_X + OV_LAN_W - 14} y={top + 35} textAnchor="end"
                  fontSize={9} fill={accent} opacity={0.55}
                  fontFamily="ui-sans-serif,system-ui,sans-serif">
                  click to open ›
                </text>

                {/* Machine rows */}
                {card.machines.length === 0 && (
                  <text x={OV_LAN_X + OV_LAN_W / 2} y={top + OV_LAN_HEADER_H + 18}
                    textAnchor="middle" fontSize={10} fill="#9ca3af"
                    fontFamily="ui-sans-serif,system-ui,sans-serif" fontStyle="italic">
                    no machines
                  </text>
                )}
                {card.machines.map((m, i) => {
                  const ry = top + OV_LAN_HEADER_H + OV_LAN_PAD_Y + i * OV_LAN_MACHINE_ROW_H + OV_LAN_MACHINE_ROW_H / 2
                  const mt = tunnels.filter(t => t.machine_id === m.id)
                  const dotColor = statusHex(m.status)
                  const lastSeenSub = isOffline && card.offlineSubGroups
                    ? card.offlineSubGroups.find(g => g.machines.includes(m))?.lastKnownIP
                    : undefined
                  return (
                    <g key={`m-${m.id}`}>
                      <circle cx={OV_LAN_X + 16} cy={ry} r={4} fill={dotColor} />
                      <text x={OV_LAN_X + 28} y={ry + 4} fontSize={11}
                        fill="#1f2937" fontFamily="ui-sans-serif,system-ui,sans-serif" fontWeight={600}>
                        {m.name.length > 22 ? m.name.slice(0, 20) + '…' : m.name}
                      </text>
                      <text x={OV_LAN_X + OV_LAN_W - 14} y={ry + 4} textAnchor="end"
                        fontSize={9.5} fill="#6b7280" fontFamily="ui-monospace,monospace">
                        {mt.length === 0 ? '—' : `${mt.length} port${mt.length === 1 ? '' : 's'}`}
                      </text>
                      {lastSeenSub && (
                        <text x={OV_LAN_X + 28 + (m.name.length > 22 ? 20 : m.name.length) * 6.4 + 6}
                          y={ry + 4} fontSize={8.5} fill="#9ca3af"
                          fontFamily="ui-monospace,monospace" fontStyle="italic">
                          ← {lastSeenSub}
                        </text>
                      )}
                    </g>
                  )
                })}
              </g>
            )
          })}
        </g>
      </svg>
    </div>
  )
}

function tunnelAggStatus(card: LanCardData, tunnels: Tunnel[]): 'active' | 'idle' | 'offline' {
  const cardTunnels = tunnels.filter(t => card.machines.some(m => m.id === t.machine_id))
  // isHealthyStatus also counts 'connected' (up, upstream silent — normal
  // for speak-first apps like MySQL/Minecraft) as active. Without this, a
  // LAN whose tunnels are ALL healthy-but-silent fell through both checks
  // and rendered identically to a LAN with zero working tunnels.
  if (cardTunnels.some(t => isHealthyStatus(t.status))) return 'active'
  if (cardTunnels.some(t => t.status === 'idle')) return 'idle'
  return 'offline'
}

// ── Detail (drill-down): VPS on left, machines on right, bezier curves ──────
const SVG_WIDTH = 940
const VPS_X = 16
const VPS_W = 235
const VPS_RIGHT = VPS_X + VPS_W
const MACHINE_X = 640
const MACHINE_W = 225
const ROW_H = 27
const MACH_HEADER = 72
const VPS_HEADER = 70
const MACH_GAP = 12
const V_PAD = 32

// Machine boxes on the detail map. machine.status is only ever
// pending/connected/offline, so this used to treat "offline" as the generic
// fallback — the same neutral gray as a machine with no data at all, instead
// of a visually distinct "down" style like StatusBadge gives it everywhere
// else.
function statusStyleSVG(status: string) {
  if (status === 'active' || status === 'connected')
    return { fill: '#f0fdf4', stroke: '#16a34a', text: '#14532d', dot: '#22c55e' }
  if (status === 'pending' || status === 'connecting')
    return { fill: '#fefce8', stroke: '#ca8a04', text: '#92400e', dot: '#eab308' }
  if (status === 'offline' || status === 'failed' || status === 'error')
    return { fill: '#fef2f2', stroke: '#dc2626', text: '#7f1d1d', dot: '#ef4444' }
  return { fill: '#f9fafb', stroke: '#d1d5db', text: '#6b7280', dot: '#d1d5db' }
}

function machineBoxH(tunnelCount: number): number {
  return MACH_HEADER + (tunnelCount > 0 ? tunnelCount * ROW_H + 6 : 0)
}

function tunnelVpsRowH(t: Tunnel): number {
  return t.subdomain ? ROW_H + 13 : ROW_H
}

function vpsBoxH(machineGroups: { tunnels: Tunnel[] }[]): number {
  let portH = 0
  for (const { tunnels } of machineGroups) {
    if (tunnels.length === 0) continue
    portH += 16
    for (const t of tunnels) portH += tunnelVpsRowH(t)
    portH += 6
  }
  return VPS_HEADER + (portH > 0 ? 10 + portH : 0) + 10
}

function DetailView({
  card, tunnels, netInfoMap, domain, vpsPublicIP, vpsHost, domainIP, routerIP, displayHost,
}: {
  card: LanCardData
  tunnels: Tunnel[]
  netInfoMap: Map<string, NetInfo>
  domain: string
  vpsPublicIP: string
  vpsHost: string
  domainIP: string
  routerIP: string
  displayHost: string
}) {
  const [transform, setTransform] = useState({ scale: 1, tx: 0, ty: 0 })
  const [isDragging, setIsDragging] = useState(false)
  const svgRef = useRef<SVGSVGElement>(null)
  const svgHRef = useRef(0)
  const dragRef = useRef<{ clientX: number; clientY: number; startTx: number; startTy: number; moved: boolean } | null>(null)

  const machinesInCard = card.machines
  const machineGroups = machinesInCard.map(m => ({ machine: m, tunnels: tunnels.filter(t => t.machine_id === m.id) }))

  const vpsH = vpsBoxH(machineGroups)
  const machinesAreaH = machineGroups.reduce((s, g) => s + machineBoxH(g.tunnels.length), 0)
    + Math.max(0, machineGroups.length - 1) * MACH_GAP
  const svgH = Math.max(machinesAreaH + V_PAD * 2, vpsH + V_PAD * 2, 260)
  svgHRef.current = svgH

  const machinesTop = (svgH - machinesAreaH) / 2
  const machineLayouts: { machine: Machine; tunnels: Tunnel[]; top: number; boxH: number }[] = []
  let curMachY = machinesTop
  for (const g of machineGroups) {
    const bh = machineBoxH(g.tunnels.length)
    machineLayouts.push({ machine: g.machine, tunnels: g.tunnels, top: curMachY, boxH: bh })
    curMachY += bh + MACH_GAP
  }

  const vpsTop = (svgH - vpsH) / 2
  const portSectionStart = vpsTop + VPS_HEADER + 10

  // Tunnel lines
  const tunnelLines: { tunnel: Tunnel; vpsY: number; machY: number }[] = []
  let vpsCur = portSectionStart
  for (const { tunnels: mt, top } of machineLayouts) {
    if (mt.length === 0) continue
    vpsCur += 16
    mt.forEach((t, j) => {
      tunnelLines.push({
        tunnel: t,
        vpsY: vpsCur + ROW_H / 2,
        machY: top + MACH_HEADER + j * ROW_H + ROW_H / 2,
      })
      vpsCur += tunnelVpsRowH(t)
    })
    vpsCur += 6
  }

  const midX = (VPS_RIGHT + MACHINE_X) / 2

  // Pan/zoom
  const zoomIn = () => setTransform(t => ({ ...t, scale: Math.min(t.scale * 1.25, 5) }))
  const zoomOut = () => setTransform(t => ({ ...t, scale: Math.max(t.scale * 0.8, 0.3) }))
  const resetView = () => setTransform({ scale: 1, tx: 0, ty: 0 })

  useEffect(() => {
    const svg = svgRef.current
    if (!svg) return
    const handler = (e: WheelEvent) => {
      e.preventDefault()
      const rect = svg.getBoundingClientRect()
      const sx = (e.clientX - rect.left) * (SVG_WIDTH / rect.width)
      const sy = (e.clientY - rect.top) * (svgHRef.current / rect.height)
      const factor = e.deltaY > 0 ? 0.9 : 1.1
      setTransform(t => {
        const newScale = Math.max(0.3, Math.min(5, t.scale * factor))
        const ratio = newScale / t.scale
        return { scale: newScale, tx: sx - (sx - t.tx) * ratio, ty: sy - (sy - t.ty) * ratio }
      })
    }
    svg.addEventListener('wheel', handler, { passive: false })
    return () => svg.removeEventListener('wheel', handler)
  }, [])

  const onPointerDown = (e: React.PointerEvent<SVGSVGElement>) => {
    e.currentTarget.setPointerCapture(e.pointerId)
    dragRef.current = { clientX: e.clientX, clientY: e.clientY, startTx: transform.tx, startTy: transform.ty, moved: false }
    setIsDragging(true)
  }
  const onPointerMove = (e: React.PointerEvent<SVGSVGElement>) => {
    const drag = dragRef.current
    if (!drag) return
    const dx = e.clientX - drag.clientX
    const dy = e.clientY - drag.clientY
    if (Math.hypot(dx, dy) > 3) drag.moved = true
    const rect = e.currentTarget.getBoundingClientRect()
    const svgDx = dx * (SVG_WIDTH / rect.width)
    const svgDy = dy * (svgHRef.current / rect.height)
    const newTx = drag.startTx + svgDx
    const newTy = drag.startTy + svgDy
    setTransform(t => ({ ...t, tx: newTx, ty: newTy }))
  }
  const onPointerUp = () => {
    dragRef.current = null
    setIsDragging(false)
  }

  const cardTitle = card.isOffline
    ? 'Offline machines'
    : card.publicIP || `solo · ${card.machines[0]?.name ?? ''}`

  return (
    <>
      {/* Title strip for the LAN being viewed */}
      <div className={`bg-white rounded-xl border shadow-sm px-4 py-3 flex items-center justify-between ${
        card.isOffline ? 'border-gray-300' : card.isNat ? 'border-amber-300' : 'border-indigo-300'
      }`}>
        <div className="flex items-center gap-3">
          {card.isOffline
            ? <PowerOff size={18} className="text-gray-500" />
            : card.isNat
            ? <Shield size={18} className="text-amber-600" />
            : <Globe size={18} className="text-indigo-600" />}
          <span className="font-mono font-bold text-base text-gray-800">{cardTitle}</span>
          {card.isNat && (
            <span className="text-xs font-semibold uppercase tracking-wider text-amber-700 bg-amber-100 px-1.5 py-0.5 rounded">NAT</span>
          )}
          {!card.isOffline && card.publicIP && (
            <button
              onClick={() => navigator.clipboard.writeText(card.publicIP)}
              className="text-gray-400 hover:text-gray-600"
              title="Copy IP">
              <ClipboardCopy size={13} />
            </button>
          )}
        </div>
        <div className="flex items-center gap-3 text-sm text-gray-500">
          <span><strong className="text-gray-700">{card.machines.length}</strong> machine{card.machines.length === 1 ? '' : 's'}</span>
          <span>·</span>
          <span><strong className="text-gray-700">{tunnelLines.length}</strong> tunnel{tunnelLines.length === 1 ? '' : 's'}</span>
          <div className="ml-3 flex items-center gap-1 border border-gray-300 rounded-lg px-1 py-0.5 bg-white">
            <button onClick={zoomOut} className="p-1 hover:bg-gray-100 rounded text-gray-600" title="Zoom out"><ZoomOut size={12} /></button>
            <span className="text-xs text-gray-500 px-1.5 font-mono w-10 text-center">{Math.round(transform.scale * 100)}%</span>
            <button onClick={zoomIn} className="p-1 hover:bg-gray-100 rounded text-gray-600" title="Zoom in"><ZoomIn size={12} /></button>
            <div className="w-px h-4 bg-gray-200 mx-0.5" />
            <button onClick={resetView} className="p-1 hover:bg-gray-100 rounded text-gray-600" title="Reset view"><Maximize2 size={12} /></button>
          </div>
        </div>
      </div>

      <div className="bg-white rounded-xl border shadow-sm overflow-hidden">
        <svg
          ref={svgRef}
          viewBox={`0 0 ${SVG_WIDTH} ${svgH}`}
          className="w-full select-none"
          style={{ minHeight: 220, cursor: isDragging ? 'grabbing' : 'grab', touchAction: 'none' }}
          onPointerDown={onPointerDown}
          onPointerMove={onPointerMove}
          onPointerUp={onPointerUp}
          onPointerCancel={onPointerUp}
        >
          <defs>
            <pattern id="dots" x="0" y="0" width="24" height="24" patternUnits="userSpaceOnUse">
              <circle cx="1" cy="1" r="1" fill="#e5e7eb" />
            </pattern>
          </defs>
          <rect width={SVG_WIDTH} height={svgH} fill="url(#dots)" />

          <g transform={`translate(${transform.tx} ${transform.ty}) scale(${transform.scale})`}>
            {/* Tunnel lines */}
            {tunnelLines.map(({ tunnel, vpsY, machY }) => {
              // healthy = isHealthyStatus (active OR connected — up, upstream
              // silent, normal for speak-first apps like Minecraft/MySQL).
              // Was `tunnel.status === 'active'` only, so a healthy
              // 'connected' tunnel's own connector line rendered dashed grey
              // — visually identical to a dead one.
              const healthy = isHealthyStatus(tunnel.status)
              const idle = statusBucket(tunnel.status) === 'idle'
              const isUdp = tunnel.transport === 'udp'
              const cy = (vpsY + machY) / 2
              const label = `:${tunnel.rathole_port}`
              const lw = label.length * 6.2 + 10
              const lineColor  = (healthy || idle) ? (isUdp ? '#a855f7' : '#4ade80') : '#d1d5db'
              const pillFill   = healthy ? (isUdp ? '#faf5ff' : '#f0fdf4') : idle ? '#fffbeb' : '#f9fafb'
              const pillStroke = healthy ? (isUdp ? '#a855f7' : '#4ade80') : idle ? '#f59e0b' : '#d1d5db'
              const textColor  = healthy ? (isUdp ? '#7e22ce' : '#16a34a') : idle ? '#b45309' : '#6b7280'
              return (
                <g key={tunnel.id}>
                  <path
                    d={`M ${VPS_RIGHT} ${vpsY} C ${midX} ${vpsY}, ${midX} ${machY}, ${MACHINE_X} ${machY}`}
                    fill="none"
                    stroke={lineColor}
                    strokeWidth={(healthy || idle) ? 2.5 : 1.5}
                    strokeDasharray={healthy || idle ? undefined : '5 4'}
                    opacity={0.9}
                  />
                  <rect x={midX - lw / 2} y={cy - 9} width={lw} height={17} rx={4}
                    fill={pillFill} stroke={pillStroke} strokeWidth={1} />
                  <text x={midX} y={cy + 4} textAnchor="middle"
                    fontSize={9.5} fontFamily="ui-monospace,monospace" fontWeight={700}
                    fill={textColor}>
                    {label}
                  </text>
                </g>
              )
            })}

            {/* VPS box */}
            {domain ? (
              <g>
                <rect x={VPS_X - 4} y={vpsTop - 4} width={VPS_W + 8} height={vpsH + 8}
                  rx={16} fill="none" stroke="#818cf8" strokeWidth={1.5} opacity={0.28} />
                <rect x={VPS_X} y={vpsTop} width={VPS_W} height={vpsH}
                  rx={12} fill="#eef2ff" stroke="#6366f1" strokeWidth={2} />

                <text x={VPS_X + VPS_W / 2} y={vpsTop + 17} textAnchor="middle"
                  fontSize={10} fontWeight={700} fill="#4338ca" letterSpacing={1.5}
                  fontFamily="ui-sans-serif,system-ui,sans-serif">
                  VPS / SERVER
                </text>
                <circle cx={VPS_X + VPS_W - 14} cy={vpsTop + 13} r={5} fill="#22c55e" />

                <text x={VPS_X + 12} y={vpsTop + 32} fontSize={11} fill="#4338ca"
                  fontFamily="ui-monospace,monospace" fontWeight={600}>
                  {vpsPublicIP.length > 25 ? vpsPublicIP.slice(0, 23) + '…' : vpsPublicIP}
                </text>
                {vpsHost && vpsHost !== vpsPublicIP && (
                  <text x={VPS_X + 12} y={vpsTop + 46} fontSize={9.5} fill="#818cf8"
                    fontFamily="ui-monospace,monospace" opacity={0.8}>
                    {vpsHost.length > 28 ? vpsHost.slice(0, 26) + '…' : vpsHost}
                  </text>
                )}
                <text x={VPS_X + 12} y={vpsTop + (vpsHost && vpsHost !== vpsPublicIP ? 60 : 47)}
                  fontSize={10} fill="#6366f1" fontFamily="ui-monospace,monospace">
                  {(() => { const ip = routerIP || domainIP || '…'; const h = displayHost; return `${ip} · ${h.length > 22 ? h.slice(0, 20) + '…' : h}` })()}
                </text>

                {tunnelLines.length > 0 && (
                  <line x1={VPS_X + 10} y1={portSectionStart - 6}
                    x2={VPS_X + VPS_W - 10} y2={portSectionStart - 6}
                    stroke="#c7d2fe" strokeWidth={1} strokeDasharray="3 2" />
                )}

                {(() => {
                  const rows: React.ReactNode[] = []
                  let cur = portSectionStart
                  for (const { machine, tunnels: mt } of machineLayouts) {
                    if (mt.length === 0) continue
                    const s = statusStyleSVG(machine.status)
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
                          {/* statusHex — was its own ad hoc mapping that treated 'idle'
                              as healthy-green and lumped 'connected'/'offline'/everything
                              else into one indigo color, disagreeing with the machine-side
                              endpoint of this exact same tunnel drawn below. */}
                          <circle cx={VPS_RIGHT} cy={ry} r={4.5}
                            fill={statusHex(t.status)}
                            stroke="white" strokeWidth={1.5} />
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

            {/* Machine boxes */}
            {machineLayouts.map(({ machine, tunnels: mt, top, boxH: bh }) => {
              const s = statusStyleSVG(machine.status)
              const info = netInfoMap.get(machine.id)
              const dHost = machine.host
                ? (machine.host.length > 20 ? machine.host.slice(0, 18) + '…' : machine.host)
                : null
              const showPrivateBadge = info && isPrivateIP(machine.host ?? '')
              const showPublicIP = info?.public_ip && info.public_ip !== machine.host

              return (
                <g key={machine.id}>
                  <rect x={MACHINE_X} y={top} width={MACHINE_W} height={bh}
                    rx={9} fill={s.fill} stroke={s.stroke} strokeWidth={1.75} />
                  <circle cx={MACHINE_X + MACHINE_W - 11} cy={top + 11} r={5} fill={s.dot} />

                  <text x={MACHINE_X + 12} y={top + 19} textAnchor="start"
                    fontSize={13} fontWeight={700} fill={s.text}
                    fontFamily="ui-sans-serif,system-ui,sans-serif">
                    {machine.name.length > 18 ? machine.name.slice(0, 16) + '…' : machine.name}
                  </text>

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

                  {showPublicIP && (
                    <text x={MACHINE_X + 12} y={top + 50} textAnchor="start"
                      fontSize={9.5} fill="#6366f1" fontFamily="ui-monospace,monospace">
                      {`↑ ${info!.public_ip}`}
                    </text>
                  )}

                  <text x={MACHINE_X + 12}
                    y={top + (showPublicIP ? 63 : dHost ? 50 : 36)}
                    textAnchor="start" fontSize={10} fill={s.text}
                    fontFamily="ui-sans-serif,system-ui,sans-serif" opacity={0.72}>
                    @{machine.username.length > 16 ? machine.username.slice(0, 14) + '…' : machine.username}
                  </text>

                  {mt.length > 0 && (
                    <line x1={MACHINE_X + 8} y1={top + MACH_HEADER}
                      x2={MACHINE_X + MACHINE_W - 8} y2={top + MACH_HEADER}
                      stroke={s.stroke} strokeWidth={1} opacity={0.35} />
                  )}

                  {mt.map((t, j) => {
                    const ry = top + MACH_HEADER + j * ROW_H + ROW_H / 2
                    // statusHex — must match the VPS-side endpoint of this same
                    // tunnel (drawn earlier); this used to be a separate ad hoc
                    // mapping that disagreed with it for 'connected'/'offline'.
                    const tDot = statusHex(t.status)
                    const name = t.name.length > 13 ? t.name.slice(0, 11) + '…' : t.name
                    return (
                      <g key={`mr-${t.id}`}>
                        <circle cx={MACHINE_X} cy={ry} r={4.5}
                          fill={tDot} stroke="white" strokeWidth={1.5} />
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
          </g>
        </svg>

        <div className="px-4 py-2 border-t bg-gray-50 text-xs text-gray-500">
          Scroll to zoom · drag to pan
        </div>
      </div>

      {/* Tunnel index for this LAN */}
      <div className="bg-white rounded-xl border shadow-sm overflow-hidden">
        <div className="px-4 py-2.5 border-b bg-gray-50 text-xs font-semibold text-gray-600 uppercase tracking-wide">
          Tunnels in this network
        </div>
        <div className="divide-y">
          {tunnelLines.length === 0 && (
            <div className="px-4 py-4 text-sm text-gray-400">No tunnels configured.</div>
          )}
          {tunnelLines.map(({ tunnel }) => (
            <TunnelRow key={tunnel.id} tunnel={tunnel} domain={domain} />
          ))}
        </div>
      </div>
    </>
  )
}

function TunnelRow({ tunnel, domain }: { tunnel: Tunnel; domain: string }) {
  const hasUrl = !!tunnel.subdomain && !!domain
  return (
    <div className="flex items-center gap-2 px-4 py-2 text-sm">
      <span className={`w-2 h-2 rounded-full shrink-0 ${tunnelStatusDot(tunnel.status)}`} />
      <span className={`text-xs font-mono px-1.5 py-0.5 rounded uppercase font-semibold shrink-0 ${
        tunnel.transport === 'udp' ? 'bg-purple-50 text-purple-700' : 'bg-gray-100 text-gray-600'
      }`}>
        {tunnel.transport === 'udp' ? 'UDP' : tunnel.protocol}
      </span>
      <span className="text-gray-800 font-medium truncate flex-1">{tunnel.name}</span>
      <span className="text-xs font-mono text-indigo-700 bg-indigo-50 border border-indigo-200 rounded px-1.5 py-0.5 shrink-0">
        :{tunnel.rathole_port}
      </span>
      <span className="text-gray-300 text-xs">→</span>
      <span className="text-xs font-mono text-green-700 bg-green-50 border border-green-200 rounded px-1.5 py-0.5 shrink-0">
        :{tunnel.local_port}
      </span>
      <span className={`text-xs font-medium px-1.5 py-0.5 rounded shrink-0 ${statusPillClasses(tunnel.status)}`}>
        {tunnel.status === 'idle' ? 'no service' : tunnel.status}
      </span>
      {hasUrl && (
        <a href={`https://${tunnel.subdomain}.${domain}`} target="_blank" rel="noopener noreferrer"
          className="text-indigo-500 hover:text-indigo-700 text-xs font-mono truncate max-w-[180px] shrink-0">
          {tunnel.subdomain}.{domain}
        </a>
      )}
    </div>
  )
}
