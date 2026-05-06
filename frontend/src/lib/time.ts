// Relative-time formatting for "last seen 2m ago" style UI strings.
// Single-purpose helper so the various pages stay consistent.

export function relativeTime(input: string | null | undefined): string {
  if (!input) return '—'
  const ts = Date.parse(input)
  if (Number.isNaN(ts)) return '—'
  const diffMs = Date.now() - ts
  if (diffMs < 0) return 'just now'

  const sec = Math.floor(diffMs / 1000)
  if (sec < 5) return 'just now'
  if (sec < 60) return `${sec}s ago`
  const min = Math.floor(sec / 60)
  if (min < 60) return `${min}m ago`
  const hr = Math.floor(min / 60)
  if (hr < 24) return `${hr}h ago`
  const days = Math.floor(hr / 24)
  if (days < 30) return `${days}d ago`
  // Beyond ~a month, an absolute date is more useful than "12mo ago".
  return new Date(ts).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

// formatBytes turns raw byte counts into human-readable strings
// (1.2 GB, 540 MB, etc.). Used for the agent's disk metrics.
export function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)))
  const value = bytes / Math.pow(1024, i)
  return `${value.toFixed(value >= 100 ? 0 : 1)} ${units[i]}`
}

// formatDuration turns seconds into a compact uptime-style string
// (3d 4h 12m, 2h 5m, 13m, etc.).
export function formatDuration(seconds: number): string {
  if (seconds < 60) return `${seconds}s`
  const min = Math.floor(seconds / 60)
  if (min < 60) return `${min}m`
  const hr = Math.floor(min / 60)
  const remMin = min % 60
  if (hr < 24) return remMin > 0 ? `${hr}h ${remMin}m` : `${hr}h`
  const days = Math.floor(hr / 24)
  const remHr = hr % 24
  return remHr > 0 ? `${days}d ${remHr}h` : `${days}d`
}
