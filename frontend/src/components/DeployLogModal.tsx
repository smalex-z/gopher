import { useEffect, useRef, useState } from 'react'

interface Props {
  isOpen: boolean
  onClose: () => void
  title: string
  onStart: () => Promise<void>
  autoStart?: boolean
}

type Status = 'idle' | 'connecting' | 'running' | 'complete' | 'error'

export default function DeployLogModal({ isOpen, onClose, title, onStart }: Props) {
  const [status, setStatus] = useState<Status>('idle')
  const [logs, setLogs] = useState<string[]>([])
  const wsRef = useRef<WebSocket | null>(null)
  const logEndRef = useRef<HTMLDivElement>(null)
  const onStartRef = useRef(onStart)

  useEffect(() => {
    onStartRef.current = onStart
  })

  useEffect(() => {
    if (!isOpen) {
      wsRef.current?.close()
      wsRef.current = null
      setStatus('idle')
      setLogs([])
      return
    }

    setStatus('connecting')
    setLogs([])
    const wsProto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const ws = new WebSocket(`${wsProto}//${window.location.host}/api/logs/ws`)
    wsRef.current = ws

    ws.onopen = () => {
      setStatus('running')
      onStartRef.current().catch(err => {
        setLogs(prev => [...prev, `Error: ${err instanceof Error ? err.message : String(err)}`])
        setStatus('error')
      })
    }

    ws.onmessage = (e: MessageEvent) => {
      // Sentinel broadcast by the server when the operation is complete.
      if (e.data === '\x00DONE') {
        setStatus('complete')
        ws.close()
        return
      }
      setLogs(prev => [...prev, e.data as string])
    }

    ws.onclose = () => {
      setStatus(s => s === 'running' ? 'complete' : s)
    }

    ws.onerror = () => {
      setStatus('error')
    }

    return () => {
      ws.close()
    }
  }, [isOpen])

  useEffect(() => {
    logEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [logs])

  if (!isOpen) return null

  const canClose = status !== 'connecting' && status !== 'running'

  return (
    <div className="fixed inset-0 bg-black/60 z-50 flex items-center justify-center p-4">
      <div className="bg-white rounded-xl shadow-2xl w-full max-w-2xl">
        <div className="flex items-center justify-between p-4 border-b">
          <h2 className="text-lg font-semibold">{title}</h2>
          <div className="flex items-center gap-3">
            <span className={`text-sm px-2 py-1 rounded-full ${
              status === 'connecting' ? 'bg-yellow-100 text-yellow-700' :
              status === 'running' ? 'bg-blue-100 text-blue-700' :
              status === 'complete' ? 'bg-green-100 text-green-700' :
              status === 'error' ? 'bg-red-100 text-red-700' :
              'bg-gray-100 text-gray-700'
            }`}>
              {status === 'connecting' ? 'Connecting...' :
               status === 'running' ? 'Running...' :
               status === 'complete' ? 'Complete' :
               status === 'error' ? 'Error' : 'Idle'}
            </span>
          </div>
        </div>
        <div className="p-4">
          <pre className="bg-black text-green-400 font-mono text-sm p-4 h-64 overflow-y-auto rounded-lg">
            {logs.join('\n') || (status === 'connecting' ? 'Connecting to log stream...' : '')}
            <div ref={logEndRef} />
          </pre>
        </div>
        <div className="flex justify-end p-4 border-t">
          <button
            onClick={onClose}
            disabled={!canClose}
            className="px-4 py-2 bg-gray-600 text-white rounded-lg hover:bg-gray-700 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            Close
          </button>
        </div>
      </div>
    </div>
  )
}
