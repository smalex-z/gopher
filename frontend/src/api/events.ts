import client from './client'
import type { ActivityEvent, EventSeverity, EventSource } from './local'

export type { ActivityEvent, EventSeverity, EventSource }

export interface EventsQuery {
  sources?: EventSource[]
  severity?: EventSeverity
  minSeverity?: EventSeverity
  resourceId?: string
  q?: string
  since?: string  // ISO string
  until?: string
  before?: string // cursor
  limit?: number
}

export interface EventsPage {
  events: ActivityEvent[]
  total: number
}

export const eventsApi = {
  list: (query: EventsQuery = {}) => {
    const params = new URLSearchParams()
    if (query.sources && query.sources.length > 0) params.set('source', query.sources.join(','))
    if (query.severity) params.set('severity', query.severity)
    if (query.minSeverity) params.set('min_severity', query.minSeverity)
    if (query.resourceId) params.set('resource_id', query.resourceId)
    if (query.q) params.set('q', query.q)
    if (query.since) params.set('since', query.since)
    if (query.until) params.set('until', query.until)
    if (query.before) params.set('before', query.before)
    if (query.limit) params.set('limit', String(query.limit))

    const qs = params.toString()
    const url = qs ? `/events?${qs}` : '/events'
    return client.get<{ data: EventsPage }>(url).then(r => r.data.data)
  },
}
