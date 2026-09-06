import { statusBg } from '../lib/statusColor'

interface Props { status: string; className?: string }

export default function StatusBadge({ status, className = '' }: Props) {
  // Color mapping lives in lib/statusColor.ts — the single source every
  // status-derived color in the app must use (see that file's header for why).
  const color = statusBg(status)

  return (
    <span className={`inline-flex items-center gap-1.5 text-sm ${className}`}>
      <span className={`inline-block w-2 h-2 rounded-full ${color}`} />
      <span className="capitalize">{status}</span>
    </span>
  )
}
