import type { HealthCheck } from '../types'

interface SparklineProps {
  checks: HealthCheck[]      // newest-first; rendered left = oldest, right = newest
  width?: number
  height?: number
  /** Show on hover. Useful for compact rows. */
  title?: string
}

// Sparkline renders the most recent N health checks as a row of small
// rectangles — green = OK, red = failure. No interpolation, no axes; this
// is a pulse-of-life indicator, not a chart.
//
// We render right-aligned (newest at the right edge) so the operator's
// eye lands on the latest state first. When fewer than `slots` checks
// are available, the left side stays empty rather than stretching the
// existing data — distinguishes "young / not enough data" from "noisy".
export default function Sparkline({ checks, width = 90, height = 14, title }: SparklineProps) {
  const slots = 30
  const slotWidth = width / slots
  const padding = 1

  // checks is newest-first; reverse so index 0 = oldest, slots-1 = newest.
  const ordered = checks.slice(0, slots).reverse()
  const offset = slots - ordered.length

  return (
    <svg
      width={width}
      height={height}
      role="img"
      aria-label={title ?? 'Recent health checks'}
      className="inline-block align-middle"
    >
      {title && <title>{title}</title>}
      {ordered.map((c, i) => {
        const x = (offset + i) * slotWidth + padding / 2
        const w = Math.max(1, slotWidth - padding)
        const fill = c.ok ? '#10b981' : '#ef4444' // emerald-500 / red-500
        return <rect key={c.id || i} x={x} y={0} width={w} height={height} fill={fill} rx={1.5} />
      })}
    </svg>
  )
}
