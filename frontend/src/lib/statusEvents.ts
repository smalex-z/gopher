import { useEffect } from 'react'
import { useQueryClient } from '@tanstack/react-query'

interface StatusEvent {
  kind: 'machine' | 'tunnel'
  id: string
  status: string
}

// Live status push channel. The backend broadcasts a StatusEvent whenever a
// machine/tunnel status actually transitions (or a row is created/deleted);
// we invalidate the matching query cache so badges update on push instead of
// waiting out the poll interval. The existing refetchIntervals stay as the
// fallback for anything the socket misses (dropped events, reconnect gaps).
export function useStatusEvents(enabled: boolean) {
  const qc = useQueryClient()

  useEffect(() => {
    if (!enabled) return

    let ws: WebSocket | null = null
    let stopped = false
    let attempt = 0
    let reconnectTimer: number | undefined

    const connect = () => {
      const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
      ws = new WebSocket(`${proto}://${window.location.host}/api/status/ws`)

      ws.onopen = () => {
        attempt = 0
        // Catch up on anything that changed while we were disconnected.
        qc.invalidateQueries({ queryKey: ['machines'] })
        qc.invalidateQueries({ queryKey: ['tunnels'] })
      }

      ws.onmessage = e => {
        let ev: StatusEvent
        try { ev = JSON.parse(e.data) } catch { return }
        if (ev.kind === 'machine') qc.invalidateQueries({ queryKey: ['machines'] })
        else if (ev.kind === 'tunnel') qc.invalidateQueries({ queryKey: ['tunnels'] })
      }

      ws.onclose = () => {
        if (stopped) return
        // Capped exponential backoff: 1s, 2s, 4s, … 30s.
        attempt += 1
        const delay = Math.min(30_000, 1000 * 2 ** Math.min(attempt - 1, 5))
        reconnectTimer = window.setTimeout(connect, delay)
      }
    }

    connect()
    return () => {
      stopped = true
      if (reconnectTimer !== undefined) clearTimeout(reconnectTimer)
      ws?.close()
    }
  }, [enabled, qc])
}
