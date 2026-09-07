import { useQuery } from '@tanstack/react-query'
import { ShieldAlert } from 'lucide-react'
import { localApi } from '../api/local'

// NoiseMigrationBlockedBanner surfaces a DEFERRED encrypted-transport
// migration.
//
// The upgrade to rathole's noise transport can only move the fleet as a unit:
// a plaintext client cannot talk to a noise server, or the reverse. So when
// any machine is unreachable at migration time, the migration aborts before
// changing anything rather than flipping the server and stranding that
// machine. This banner is how the operator finds out — otherwise a deferred
// migration is completely invisible and they'd never know their install is
// still on plaintext.
//
// Deliberately NOT dismissible: unlike the custom-services warning (a one-time
// "go edit these configs by hand" note), this is a live state that resolves
// itself the moment the listed machines come back and the install restarts.
// Hiding it would just hide an unfinished upgrade.
export default function NoiseMigrationBlockedBanner() {
  const { data } = useQuery({
    queryKey: ['local-status'],
    queryFn: localApi.status,
    refetchInterval: 60_000,
    retry: false,
  })

  const blocked = data?.rathole_noise_blocked_machines ?? []
  if (blocked.length === 0) return null

  return (
    <div className="mb-6 bg-amber-50 border border-amber-200 rounded-xl px-4 py-3">
      <div className="flex items-start gap-3">
        <ShieldAlert size={18} className="text-amber-600 mt-0.5 shrink-0" />
        <div className="min-w-0 flex-1">
          <div className="text-sm font-semibold text-amber-900">
            Encrypted transport upgrade is waiting on {blocked.length} machine{blocked.length === 1 ? '' : 's'}
          </div>
          <p className="mt-1 text-sm text-amber-800">
            Nothing has been changed. This install is still using the plaintext rathole
            transport and every tunnel is working normally. The upgrade moves all machines
            at once, so it deferred itself rather than leaving these ones unable to reconnect.
          </p>
          <ul className="mt-2 flex flex-wrap gap-1.5">
            {blocked.map(name => (
              <li
                key={name}
                className="rounded-md bg-amber-100 px-2 py-0.5 font-mono text-xs text-amber-900"
              >
                {name}
              </li>
            ))}
          </ul>
          <p className="mt-2 text-sm text-amber-800">
            Bring {blocked.length === 1 ? 'it' : 'them'} back online, or reinstall the agent
            from the machine&apos;s page, then restart Gopher. The upgrade runs again on its own
            and completes with a single brief reconnect.
          </p>
        </div>
      </div>
    </div>
  )
}
