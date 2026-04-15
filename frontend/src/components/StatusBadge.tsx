interface Props { status: string; className?: string }

export default function StatusBadge({ status, className = '' }: Props) {
  const s = status.toLowerCase()
  let color = 'bg-gray-400'
  if (s === 'active' || s === 'connected') color = 'bg-green-500'
  else if (s === 'idle') color = 'bg-amber-400'
  else if (s === 'pending' || s === 'connecting') color = 'bg-yellow-500'
  else if (s === 'inactive' || s === 'disconnected') color = 'bg-gray-400'
  else if (s === 'failed' || s === 'error') color = 'bg-red-500'

  return (
    <span className={`inline-flex items-center gap-1.5 text-sm ${className}`}>
      <span className={`inline-block w-2 h-2 rounded-full ${color}`} />
      <span className="capitalize">{status}</span>
    </span>
  )
}
