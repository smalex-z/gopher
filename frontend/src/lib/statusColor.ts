// Single source of truth for status -> color across the app.
//
// Found during a QA sweep: this exact "map every known status to a color,
// default the rest to neutral" switch was independently reimplemented in
// StatusBadge, DashboardPage's Dot, and three places in NetworkMapPage —
// each missing different statuses. offline/idle/provisioning/config-error
// fell through to a neutral default everywhere except StatusBadge, so a
// machine or tunnel that was genuinely down could render identically to
// "nothing to report" depending which page you looked at, and one tunnel's
// two connector-line endpoints on the network map could even disagree with
// each other in the same render. Every consumer of a backend status string
// for color MUST go through here instead of adding another local switch.
//
// Backend vocabulary this covers (see internal/service/monitor.go,
// tunnel.go, health.go):
//   machine.status: pending | connected | offline
//   tunnel.status:  inactive | provisioning | active | connected | idle |
//                   offline | "config-error: <detail>"

export type StatusBucket =
  | 'active' // serving traffic / machine reachable
  | 'connected' // tunnel up, upstream silent — still healthy, lighter tint than active
  | 'idle' // tunnel up, nothing listening locally — caution
  | 'pending' // provisioning/connecting — not yet known good or bad
  | 'inactive' // never brought up / explicitly disconnected — neutral, not urgent
  | 'down' // offline/failed/error/config-error — must always read as bad
  | 'unknown' // anything not in the vocabulary above

const TAILWIND_BG: Record<StatusBucket, string> = {
  active: 'bg-green-500',
  connected: 'bg-green-300',
  idle: 'bg-amber-400',
  pending: 'bg-yellow-500',
  inactive: 'bg-gray-400',
  down: 'bg-red-500',
  unknown: 'bg-gray-300',
}

// Hex equivalents for raw SVG fill/stroke attributes (NetworkMapPage), kept
// in lockstep with TAILWIND_BG above so a dot rendered as a CSS class and one
// rendered as an SVG circle never disagree about the same status.
const HEX: Record<StatusBucket, string> = {
  active: '#22c55e',
  connected: '#4ade80', // lighter tint of the same green, mirrors bg-green-300
  idle: '#f59e0b',
  pending: '#eab308',
  inactive: '#9ca3af',
  down: '#ef4444',
  unknown: '#d1d5db',
}

export function statusBucket(status: string): StatusBucket {
  const s = (status || '').toLowerCase()
  if (s === 'active') return 'active'
  if (s === 'connected') return 'connected'
  if (s === 'idle') return 'idle'
  if (s === 'pending' || s === 'connecting' || s === 'provisioning') return 'pending'
  if (s === 'inactive' || s === 'disconnected') return 'inactive'
  if (s === 'offline' || s === 'failed' || s === 'error' || s.startsWith('config-error')) return 'down'
  return 'unknown'
}

// True for a tunnel status that means "working fine" — active (confirmed
// responding) and connected (up, upstream silent — normal for speak-first
// apps like MySQL/Minecraft) are both healthy. Use this anywhere that needs
// a plain "is this OK" boolean instead of a full bucket (e.g. an "N/M
// healthy" count) so healthy-but-silent tunnels don't get undercounted.
export function isHealthyStatus(status: string): boolean {
  const b = statusBucket(status)
  return b === 'active' || b === 'connected'
}

export function statusBg(status: string): string {
  return TAILWIND_BG[statusBucket(status)]
}

export function statusHex(status: string): string {
  return HEX[statusBucket(status)]
}

// Paired bg-*-100/text-*-700 classes for pill-style text badges (as opposed
// to the solid-color dots above) — same bucket, different shade convention.
// Found missing its own consumer during a live bug report: NetworkMapPage's
// "Tunnels in this network" row had a THIRD ad hoc status->color switch (the
// dot next to it already went through statusBg) that only handled
// active/idle explicitly, so connected — and offline/config-error, the same
// gap — fell through to grey.
const PILL: Record<StatusBucket, string> = {
  active: 'bg-green-100 text-green-700',
  connected: 'bg-green-100 text-green-700', // same pill treatment as active — both healthy
  idle: 'bg-amber-100 text-amber-700',
  pending: 'bg-yellow-100 text-yellow-700',
  inactive: 'bg-gray-100 text-gray-500',
  down: 'bg-red-100 text-red-700',
  unknown: 'bg-gray-100 text-gray-500',
}

export function statusPillClasses(status: string): string {
  return PILL[statusBucket(status)]
}
