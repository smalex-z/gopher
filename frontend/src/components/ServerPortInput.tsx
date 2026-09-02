import { useEffect, useRef, useState } from 'react'
import { tunnelsApi } from '../api/tunnels'

export interface PortCheck { port: number; available: boolean; reason: string }

interface Props {
  value: number | null
  onChange: (port: number | null) => void
  /** Empty input allowed (server auto-assigns). */
  optional?: boolean
  placeholder?: string
  /** Host adornment rendered before the input ("host:"). */
  prefix?: string
  /** Suspend availability checks (e.g. while a create request is in flight). */
  paused?: boolean
  /** A port the backend's own allocator (nextPort) already handed out —
   *  reported available without re-asking until the operator changes it. */
  skipCheckFor?: number | null
  /** Conflict the parent already knows from loaded data (DB rows in cache);
   *  rendered with the same styling as a failed availability check. */
  externalConflict?: string
  /** Mirrors every availability result up for submit gating. */
  onCheck?: (check: PortCheck | null) => void
}

// Shared input for a rathole server port on the VPS — used by tunnel create
// and the bootstrap modal's SSH port so both get identical validation. Owns
// the client-side range gate (1024–65535, the same bounds the backend's
// ValidatePort enforces: below 1024 is privileged) and the debounced
// /tunnels/check-port probe. The browser can't probe the VPS itself, so we
// ask the API as the operator types — that catches ports held by a live
// process (rathole's 2333, Caddy, the dashboard) that no client-side check
// can see, before submit instead of at rathole bind time.
export default function ServerPortInput({ value, onChange, optional, placeholder, prefix, paused, skipCheckFor, externalConflict, onCheck }: Props) {
  const [check, setCheck] = useState<PortCheck | null>(null)

  // Kept in a ref so an inline callback prop doesn't re-fire the debounce
  // effect on every parent render.
  const onCheckRef = useRef(onCheck)
  onCheckRef.current = onCheck

  const inRange = value !== null && value >= 1024 && value <= 65535
  const outOfRange = value !== null && value > 0 && !inRange

  useEffect(() => {
    const report = (c: PortCheck | null) => { setCheck(c); onCheckRef.current?.(c) }
    if (paused || value === null || !inRange) { report(null); return }
    if (skipCheckFor === value) { report({ port: value, available: true, reason: '' }); return }
    let cancelled = false
    const timer = setTimeout(() => {
      tunnelsApi.checkPort(value)
        .then(res => { if (!cancelled) report({ port: value, available: res.available, reason: res.reason }) })
        .catch(() => { if (!cancelled) report(null) })
    }, 350)
    return () => { cancelled = true; clearTimeout(timer) }
  }, [value, inRange, paused, skipCheckFor])

  const conflict = externalConflict
    || (check && check.port === value && !check.available ? check.reason : '')
  const showError = outOfRange || Boolean(conflict)

  return (
    <div>
      <div className="flex items-center gap-2">
        {prefix && (
          <span className="text-sm text-gray-500 font-mono shrink-0 truncate max-w-[160px]" title={prefix}>{prefix}:</span>
        )}
        <input
          type="number"
          min={1024}
          max={65535}
          value={value ?? ''}
          placeholder={placeholder}
          onChange={e => onChange(e.target.value === '' ? null : Number(e.target.value))}
          className={`flex-1 w-full rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 border ${
            showError ? 'border-red-400 focus:ring-red-400 bg-red-50' : 'border-gray-300 focus:ring-blue-500'
          }`}
        />
      </div>
      {outOfRange && (
        <p className="text-xs text-red-600 mt-1 flex items-center gap-1">
          ⚠ Use a port between 1024 and 65535 — below 1024 is privileged{optional ? '; leave empty to auto-assign' : ''}.
        </p>
      )}
      {!outOfRange && conflict && (
        <p className="text-xs text-red-600 mt-1 flex items-center gap-1">⚠ {conflict}</p>
      )}
    </div>
  )
}
