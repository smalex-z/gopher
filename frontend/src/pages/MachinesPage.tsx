import React, { useState, useEffect, useRef, useMemo } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { Server, Copy, Check, ChevronDown, ChevronRight, Plus, Key, Lock, Terminal, ClipboardCopy, Globe, CheckCircle, Loader2 } from 'lucide-react'
import { machinesApi } from '../api/machines'
import { tunnelsApi } from '../api/tunnels'
import { localApi } from '../api/local'
import { vpsApi } from '../api/vps'
import ServerPortInput from '../components/ServerPortInput'
import StatusBadge from '../components/StatusBadge'
import MachineHealthPanel from '../components/MachineHealthPanel'
import { relativeTime, formatDuration } from '../lib/time'
import { toast } from '../lib/toast'
import type { Machine, Tunnel, SSHKey } from '../types'

const toKeyFilename = (name: string) =>
  name.toLowerCase().replace(/\s+/g, '_').replace(/[^a-z0-9_-]/g, '')

type JumpboxOS = 'unix' | 'windows'

const UnixIcon = () => (
  <svg width="11" height="11" viewBox="0 0 12 12" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
    <rect x="1" y="1" width="10" height="10" rx="1.5"/>
    <polyline points="3,5 5.5,7 3,9"/>
    <line x1="6.5" y1="9" x2="9" y2="9"/>
  </svg>
)

const MacIcon = () => (
  <svg width="11" height="13" viewBox="0 0 11 13" fill="currentColor">
    <path d="M9.3 7C9.3 5.3 10.5 4.4 10.5 4.4 9.8 3.4 8.7 2.9 7.5 2.9 6.4 2.9 5.6 3.5 5.2 3.5 4.7 3.5 3.9 2.9 2.9 2.9 1.2 2.9 0 4.5 0 7 0 9.5 1.9 13 3.6 13 4.3 13 5 12.5 5.5 12.5 6 12.5 6.7 13 7.5 13 9.4 13 11 9.8 11 7Z"/>
    <path d="M7.1 1.7C7.4 1.2 7.6 0.6 7.5 0 6.9 0 6.1 0.4 5.7 0.9 5.4 1.3 5.2 2 5.3 2.5 5.9 2.5 6.8 2.1 7.1 1.7Z"/>
  </svg>
)

const WindowsIcon = () => (
  <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
    <rect x="0" y="0" width="5.5" height="5.5"/>
    <rect x="6.5" y="0" width="5.5" height="5.5"/>
    <rect x="0" y="6.5" width="5.5" height="5.5"/>
    <rect x="6.5" y="6.5" width="5.5" height="5.5"/>
  </svg>
)

// Bootstrap phases:
//   waiting    — token issued, no machine row yet
//   verifying  — machine row appeared (script hit /api/bootstrap) but the
//                agent hasn't reported in yet; we wait so the modal doesn't
//                show "success" while rathole + agent install is still
//                running on the box
//   success    — agent_installed flipped true, end-to-end working
//   timeout    — 10 min elapsed without success (operator can close + debug)
type BootstrapPhase = 'waiting' | 'verifying' | 'success' | 'timeout'
interface BootstrapModal { isOpen: boolean; command: string; token: string; expiresAt: string; phase: BootstrapPhase }

export default function MachinesPage() {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [bootstrapModal, setBootstrapModal] = useState<BootstrapModal>({ isOpen: false, command: '', token: '', expiresAt: '', phase: 'waiting' })
  // Fleet baseline captured when a bootstrap starts — machines present *before*
  // this bootstrap. null = not captured yet (machines query still loading); the
  // phase detector waits for it so a slow first-load query doesn't make
  // pre-existing machines look "newly registered" and false-trigger success.
  const knownMachineIds = useRef<Set<string> | null>(null)
  const bootstrapTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const bootstrapGenPrimed = useRef(false)           // false = next gen is the initial (instant) one
  // Signature of the config the current command was generated for — identical
  // config never regenerates, so wandering through bad port values and back
  // doesn't reset a command the operator may already have copied.
  const lastGenConfigRef = useRef<string | null>(null)
  const [tunnelPortInput, setTunnelPortInput] = useState<number | null>(null)
  const [bootstrapPortLoading, setBootstrapPortLoading] = useState(false)
  // Port handed out by nextPort() on open — the backend's own allocator —
  // so ServerPortInput skips re-asking about it (same pattern as TunnelsPage).
  const bootstrapVerifiedPortRef = useRef<number | null>(null)
  const [sshKeyInput, setSSHKeyInput] = useState('')
  const [publicSSHInput, setPublicSSHInput] = useState(true)   // public by default; unchecked "jumpbox" flips it
  const [sshEnabledInput, setSSHEnabledInput] = useState(true) // SSH on by default; off = agent-only
  const [sshSectionOpen, setSSHSectionOpen] = useState(false)  // collapsed by default (defaults are fine)
  const [copied, setCopied] = useState(false)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [reassigning, setReassigning] = useState<string | null>(null) // machine ID being reassigned
  const [reassignKeyID, setReassignKeyID] = useState('')
  const [jumpboxOS, setJumpboxOS] = useState<JumpboxOS>('unix')

  // Refresh cadence — three tiers based on how much is changing:
  //
  //   3s   bootstrap modal is waiting for the new machine to appear
  //   5s   any machine is < 5 min old, or has status="pending"
  //          (post-bootstrap window where status flips matter most:
  //          rathole connecting, agent installing, first health poll)
  //   15s  steady state — health service polls clients every 60s and
  //          monitor every 30s, so 15s is enough to surface changes
  //          quickly without hammering the dashboard
  //
  // refetchInterval can be a function — react-query passes the live
  // query so the cadence self-adjusts: as machines settle into
  // "connected"/"offline" and age past 5 minutes, polling drops back
  // to 15s automatically.
  const { data, isLoading } = useQuery({
    queryKey: ['machines'],
    queryFn: () => machinesApi.list(),
    refetchInterval: (query) => {
      if (bootstrapModal.isOpen && (bootstrapModal.phase === 'waiting' || bootstrapModal.phase === 'verifying')) return 3000
      const machinesNow = query.state.data?.data ?? []
      const fiveMinAgo = Date.now() - 5 * 60_000
      const hasRecent = machinesNow.some(m => {
        if (m.status === 'pending') return true
        const created = m.created_at ? Date.parse(m.created_at) : 0
        return created > fiveMinAgo
      })
      return hasRecent ? 5000 : 15000
    },
  })
  const { data: localStatus } = useQuery({ queryKey: ['local-status'], queryFn: () => localApi.status() })
  const { data: keysRes } = useQuery({ queryKey: ['ssh-keys'], queryFn: () => localApi.listSSHKeys() })
  const { data: vpsRes } = useQuery({ queryKey: ['vps'], queryFn: () => vpsApi.get() })
  const { data: firewallRes } = useQuery({ queryKey: ['firewall-overview'], queryFn: () => localApi.firewallOverview() })
  const machines: Machine[] = useMemo(() => data?.data ?? [], [data])
  // Capture the fleet baseline once machines has loaded after the modal opens.
  // Guarding on !isLoading (not just "modal open") is what fixes the first-load
  // race: without it, an empty in-flight list becomes the baseline and every
  // real machine then reads as new → instant false success + auto-close.
  useEffect(() => {
    if (bootstrapModal.isOpen && knownMachineIds.current === null && !isLoading) {
      knownMachineIds.current = new Set(machines.map(m => m.id))
    }
  }, [bootstrapModal.isOpen, isLoading, machines])
  const sshKeys: SSHKey[] = keysRes?.data ?? []
  const vps = vpsRes?.data
  const domain = localStatus?.domain ?? ''
  const vps22Open = (firewallRes?.data ?? []).some(e => e.port_range === '22' && e.action === 'ACCEPT')

  const machineSSHCmd = (m: Machine, key: SSHKey | undefined, os: JumpboxOS = 'unix'): { cmd: string; label: string; keyMissing: boolean; isJumpbox: boolean } | null => {
    if (m.tunnel_port === 0) return null
    const vpsHost = displayHost || vps?.host || '<vps-host>'
    // Jumpbox commands target the dedicated gopher-jump user when it
    // exists. Falls back to os_user (the dashboard service user) on
    // legacy installs that haven't re-run `gopher install` yet — those
    // installs are still vulnerable to the old "all keys in dashboard
    // user's authorized_keys" misconfiguration; the operator should
    // upgrade ASAP.
    const vpsUser = localStatus?.jumpbox_user || vps?.username || localStatus?.os_user || '<vps-user>'
    const keyFile = key ? `~/.ssh/${toKeyFilename(key.name) || 'id_rsa'}` : null
    if (m.public_ssh) {
      const keyFlag = keyFile ? ` -i ${keyFile}` : ''
      return { cmd: `ssh${keyFlag} -p ${m.tunnel_port} ${m.username}@${vpsHost}`, label: 'SSH:', keyMissing: !key, isJumpbox: false }
    }
    if (!vps22Open) return null
    const keyFlag = keyFile ? ` -i ${keyFile}` : ''
    if (os === 'windows') {
      const proxyKey = keyFile ? `-i ${keyFile} ` : ''
      const cmd = `ssh -o "ProxyCommand=ssh ${proxyKey}-W %h:%p ${vpsUser}@${vpsHost}"${keyFlag} -p ${m.tunnel_port} ${m.username}@localhost`
      return { cmd, label: 'Jumpbox:', keyMissing: !key, isJumpbox: true }
    }
    // Unix/Mac: -i propagates correctly to -J
    return { cmd: `ssh -J ${vpsUser}@${vpsHost}${keyFlag} -p ${m.tunnel_port} ${m.username}@localhost`, label: 'Jumpbox:', keyMissing: !key, isJumpbox: true }
  }

  // The edge's stable transport host (jumpbox SSH + raw-TCP tunnel display).
  // Source of truth is the backend's ServerHost (defaults to router.<domain>),
  // which is exactly what's baked into each client.toml's remote_addr — so the
  // displayed commands match reality instead of guessing apex-vs-router by IP.
  const displayHost = localStatus?.server_host || (domain ? `router.${domain}` : '')

  // Drive the bootstrap modal through its phases off the live machine list:
  //
  //   waiting    + new machine row appears     → verifying
  //   verifying  + machine.agent_installed     → success (auto-close 2s)
  //
  // The 10-min timeout from generateToken still fires from either non-success
  // phase, so a stuck install doesn't keep the modal open forever.
  useEffect(() => {
    if (!bootstrapModal.isOpen) return
    if (bootstrapModal.phase === 'success' || bootstrapModal.phase === 'timeout') return
    const baseline = knownMachineIds.current
    if (!baseline) return // fleet baseline not captured yet (machines still loading)
    const newMachine = machines.find(m => !baseline.has(m.id))
    if (bootstrapModal.phase === 'waiting') {
      if (!newMachine) return
      setBootstrapModal(prev => ({ ...prev, phase: 'verifying' }))
      qc.invalidateQueries({ queryKey: ['tunnels'] })
      return
    }
    // phase === 'verifying'
    if (!newMachine) return // shouldn't happen — machine row got deleted between renders
    if (!newMachine.agent_installed) return
    if (bootstrapTimeoutRef.current) clearTimeout(bootstrapTimeoutRef.current)
    setBootstrapModal(prev => ({ ...prev, phase: 'success' }))
    qc.invalidateQueries({ queryKey: ['tunnels'] })
    setTimeout(() => {
      setBootstrapModal(prev => ({ ...prev, isOpen: false, phase: 'waiting' }))
    }, 2000)
  }, [machines, bootstrapModal.isOpen, bootstrapModal.phase, qc])

  // Clean up timeout on unmount
  useEffect(() => () => { if (bootstrapTimeoutRef.current) clearTimeout(bootstrapTimeoutRef.current) }, [])

  const deleteMutation = useMutation({
    mutationFn: (id: string) => machinesApi.delete(id),
    onSuccess: (resp) => {
      qc.invalidateQueries({ queryKey: ['machines'] })
      qc.invalidateQueries({ queryKey: ['tunnels'] })
      // Server-side teardown always succeeds when we reach here; client-side
      // teardown (running gopher-uninstall on the box) is best-effort. Surface
      // a warning so the operator knows to SSH in and run gopher-uninstall
      // manually if it failed.
      if (resp.data?.client_cleanup_ok === false) {
        const reason = resp.data.client_cleanup_error || 'unknown reason'
        toast.error(`Machine deleted on server, but client cleanup failed (${resp.data.client_cleanup_path}): ${reason}. Run sudo /usr/local/bin/gopher-uninstall on the box to finish.`)
      } else {
        toast.success('Machine deleted.')
      }
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const reassignMutation = useMutation({
    mutationFn: ({ machineId, keyId }: { machineId: string; keyId: string }) =>
      machinesApi.reassignSSHKey(machineId, keyId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['machines'] })
      qc.invalidateQueries({ queryKey: ['ssh-keys'] })
      setReassigning(null)
      toast.success('SSH key updated — new key installed on machine.')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  // Agent install can't run remotely from the dashboard — installing the
  // agent needs root on the target. So clicking "Install Agent" returns
  // the curl-bash one-liner the operator pastes on the machine. Once the
  // agent comes online, HealthService detects it and the badge flips green
  // automatically (no second click required).
  const [agentInstallModal, setAgentInstallModal] = useState<{
    open: boolean
    machineName: string
    command: string
    instruction: string
  }>({ open: false, machineName: '', command: '', instruction: '' })
  const [agentInstallCopied, setAgentInstallCopied] = useState(false)

  const installAgentMutation = useMutation({
    mutationFn: (id: string) => machinesApi.installAgent(id),
    onSuccess: (resp, id) => {
      const data = resp.data
      if (data?.command) {
        setAgentInstallModal({
          open: true,
          machineName: machines.find(m => m.id === id)?.name ?? 'machine',
          command: data.command,
          instruction: data.instruction ?? '',
        })
        setAgentInstallCopied(false)
      }
      qc.invalidateQueries({ queryKey: ['machines'] })
      qc.invalidateQueries({ queryKey: ['agent-pending'] })
    },
    onError: (e: Error) => toast.error(`Agent install failed: ${e.message}`),
  })

  // Open the combined bootstrap modal with defaults (SSH on, public). The
  // command is generated by the effect below — on open once the port prefill
  // lands, then debounced as SSH config changes — so it stays in sync with no
  // manual "regenerate". The SSH port defaults to the backend's first free
  // port (same allocator as tunnel create); auto-assign at registration
  // remains the fallback if the prefill can't be fetched.
  const openBootstrapModal = () => {
    setTunnelPortInput(null)
    setSSHKeyInput('')
    setPublicSSHInput(true)
    setSSHEnabledInput(true)
    setSSHSectionOpen(false)
    knownMachineIds.current = null // re-baseline on the next machines load
    bootstrapVerifiedPortRef.current = null
    lastGenConfigRef.current = null
    setBootstrapPortLoading(true)
    tunnelsApi.nextPort()
      .then(port => {
        bootstrapVerifiedPortRef.current = port
        setTunnelPortInput(port)
      })
      .catch(() => {})
      .finally(() => setBootstrapPortLoading(false))
    setBootstrapModal({ isOpen: true, command: '', token: '', expiresAt: '', phase: 'waiting' })
  }

  // Auto-(re)generate the bootstrap token + command whenever the modal is open
  // and the SSH config changes. Debounced (400ms) on edits so typing doesn't
  // spray one-time tokens; a config identical to the one the current command
  // was generated for is left alone entirely (lastGenConfigRef), so wandering
  // through bad port values and back never resets an already-good command.
  // Each actual regenerate supersedes the prior token (which expires unused).
  //
  // A pinned port is gated before generating: out-of-range skips immediately,
  // and any port other than the prefilled one gets an availability probe
  // first. Both failure modes leave the previous command in place —
  // ServerPortInput shows the inline error for the bad value (the port is
  // only editable while its Advanced section is open), and minting a token
  // for it would just bounce off the backend's identical validation as an
  // error toast on every edit. The probe runs here, independent of
  // ServerPortInput's own display-only check, because that input is unmounted
  // whenever Advanced is collapsed — generation must never wait on it.
  useEffect(() => {
    if (!bootstrapModal.isOpen) {
      bootstrapGenPrimed.current = false
      return
    }
    const sshEnabled = sshEnabledInput
    const publicSSH = publicSSHInput
    const keyRaw = sshKeyInput
    const port = sshEnabled ? tunnelPortInput ?? undefined : undefined
    if (sshEnabled && bootstrapPortLoading) return // first free port is being prefetched
    if (port !== undefined && (port < 1024 || port > 65535)) return
    const cfg = sshEnabled ? `ssh:${publicSSH}:${keyRaw}:${port ?? 'auto'}` : 'no-ssh'
    if (cfg === lastGenConfigRef.current) return
    const delay = bootstrapGenPrimed.current ? 400 : 0
    bootstrapGenPrimed.current = true
    let cancelled = false // a newer config superseded this run mid-await
    const timer = setTimeout(async () => {
      if (port !== undefined && port !== bootstrapVerifiedPortRef.current) {
        try {
          const res = await tunnelsApi.checkPort(port)
          if (!res.available) return
        } catch { /* probe is advisory — generateToken revalidates authoritatively */ }
        if (cancelled) return
      }
      try {
        const result = await vpsApi.generateToken({
          tunnelPort: port,
          sshKeyID: sshEnabled ? (keyRaw || undefined) : undefined,
          publicSSH: sshEnabled ? publicSSH : undefined,
          sshEnabled,
        })
        if (result?.data && !cancelled) {
          const data = result.data
          lastGenConfigRef.current = cfg
          if (bootstrapTimeoutRef.current) clearTimeout(bootstrapTimeoutRef.current)
          setBootstrapModal(prev => ({
            ...prev,
            command: data.bootstrap_command,
            token: data.token,
            expiresAt: data.expires_at,
            phase: 'waiting',
          }))
          bootstrapTimeoutRef.current = setTimeout(() => {
            setBootstrapModal(prev =>
              prev.phase === 'waiting' || prev.phase === 'verifying' ? { ...prev, phase: 'timeout' } : prev,
            )
          }, 10 * 60 * 1000)
        }
      } catch (err) {
        if (!cancelled) toast.error(err instanceof Error ? err.message : 'Failed to generate token')
      }
    }, delay)
    return () => { cancelled = true; clearTimeout(timer) }
  }, [bootstrapModal.isOpen, sshEnabledInput, publicSSHInput, sshKeyInput, tunnelPortInput, bootstrapPortLoading])

  const copyCommand = () => {
    navigator.clipboard.writeText(bootstrapModal.command).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }

  const handleDelete = (id: string) => {
    if (window.confirm('Delete this machine and all of its tunnels? This also uninstalls rathole and removes client.toml on that machine.')) {
      deleteMutation.mutate(id)
    }
  }

  const toggleExpand = (id: string) => {
    setExpanded(prev => {
      const next = new Set(prev)
      next.has(id) ? next.delete(id) : next.add(id)
      return next
    })
  }

  const addTunnel = (machineId: string) => {
    navigate(`/tunnels?machine=${machineId}`)
  }

  if (isLoading) return <div className="text-gray-400 text-center py-12">Loading...</div>

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-gray-900">Machines</h1>
          <p className="text-gray-500 mt-1">Servers registered via bootstrap tunnel</p>
        </div>
        <button
          onClick={openBootstrapModal}
          className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 text-sm font-medium"
        >
          + Bootstrap New Machine
        </button>
      </div>

      {machines.length === 0 ? (
        <div className="bg-white rounded-xl shadow-sm border p-12 text-center">
          <Server className="w-12 h-12 text-gray-300 mx-auto mb-4" />
          <h2 className="text-lg font-semibold text-gray-700 mb-2">No machines registered yet</h2>
          <p className="text-gray-400 text-sm mb-6 max-w-sm mx-auto">
            Generate a bootstrap token and run the script on any machine to register it automatically via a reverse tunnel.
          </p>
          <button
            onClick={openBootstrapModal}
            className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 text-sm font-medium"
          >
            Generate Bootstrap Token
          </button>
        </div>
      ) : (
        <div className="bg-white rounded-xl shadow-sm border overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-gray-50 border-b">
              <tr>
                {['', 'Name', 'Username', 'Status', 'Agent', 'Uptime', 'Actions'].map(h => (
                  <th key={h} className="text-left px-4 py-3 text-xs font-semibold text-gray-500 uppercase tracking-wide">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y">
              {machines.map(m => {
                const isOpen = expanded.has(m.id)
                const tunnels: Tunnel[] = m.tunnels ?? []
                return (
                  <React.Fragment key={m.id}>
                    {/* Machine row */}
                    <tr className="hover:bg-gray-50">
                      <td className="px-3 py-3 w-6">
                        <button onClick={() => toggleExpand(m.id)} className="text-gray-400 hover:text-gray-600">
                          {isOpen ? <ChevronDown size={16} /> : <ChevronRight size={16} />}
                        </button>
                      </td>
                      <td className="px-4 py-3 font-medium text-gray-900">{m.name}</td>
                      <td className="px-4 py-3 text-gray-600">{m.username}</td>
                      <td className="px-4 py-3"><StatusBadge status={m.status} /></td>
                      <td className="px-4 py-3">
                        {(() => {
                          // There is no manual "upgrade" anymore: from v0.2.0 the agent
                          // self-updates with full privileges, and the server auto-rolls any
                          // reachable agent reporting outdated. So a reachable+outdated agent
                          // is shown as "Updating…" (status, not a button). The only manual
                          // action left is a first-time INSTALL on an agentless machine —
                          // there's no agent there yet to update itself. Offline machines
                          // report their last-known version (the outdated flag may be stale).
                          // Was `m.status === 'connected' || m.status === 'degraded'` — the
                          // backend never actually writes "degraded" to Machine.Status
                          // (SetMachineAgentDegraded sets "offline"), so that branch was dead
                          // code and this silently collapsed to "agent info is live only when
                          // the WHOLE machine is connected." agent_tunnel_status answers the
                          // narrower, correct question (is the agent channel itself reachable,
                          // via AgentLastSeen freshness) independently of general connectivity —
                          // exactly the degraded case (agent up, rathole/SSH down) this was
                          // originally meant to cover.
                          const reachable = m.agent_tunnel_status === 'active'
                          const showInstall = !m.agent_installed
                          const updating = m.agent_installed && m.agent_outdated && reachable
                          if (showInstall) {
                            return (
                              <button
                                onClick={() => installAgentMutation.mutate(m.id)}
                                disabled={installAgentMutation.isPending && installAgentMutation.variables === m.id}
                                title={
                                  m.agent_install_error
                                    ? `Last error: ${m.agent_install_error}`
                                    : 'Install gopher-agent on this machine'
                                }
                                className={`px-2 py-1 text-xs rounded border flex items-center gap-1 transition-colors ${
                                  m.agent_install_error
                                    ? 'bg-red-50 text-red-700 border-red-200 hover:bg-red-100'
                                    : 'bg-amber-50 text-amber-800 border-amber-200 hover:bg-amber-100'
                                }`}
                              >
                                {installAgentMutation.isPending && installAgentMutation.variables === m.id
                                  ? <><Loader2 size={11} className="animate-spin" /> Installing…</>
                                  : m.agent_install_error
                                    ? <>Retry install</>
                                    : <>Install agent</>}
                              </button>
                            )
                          }
                          if (updating) {
                            return (
                              <span
                                title={`Agent self-updating${m.agent_version ? ` from v${m.agent_version}` : ''} to the current version…`}
                                className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-xs font-medium border text-blue-700 bg-blue-50 border-blue-200"
                              >
                                <Loader2 size={11} className="animate-spin" /> Updating…
                              </span>
                            )
                          }
                          // Installed and either current, or offline — report the last-known version.
                          const healthy = reachable && !m.agent_outdated
                          return (
                            <span
                              title={reachable ? 'Agent version' : 'Last known agent version (machine offline)'}
                              className={`inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-xs font-medium border ${
                                healthy
                                  ? 'text-green-700 bg-green-50 border-green-200'
                                  : 'text-gray-600 bg-gray-50 border-gray-200'
                              }`}
                            >
                              {healthy ? <CheckCircle size={11} /> : null} v{m.agent_version || '–'}
                            </span>
                          )
                        })()}
                      </td>
                      <td
                        className="px-4 py-3 text-gray-500"
                        title={
                          m.status === 'connected' && m.connected_since
                            ? `Connected since ${new Date(m.connected_since).toLocaleString()}`
                            : m.last_seen
                              ? `Last seen ${new Date(m.last_seen).toLocaleString()}`
                              : ''
                        }
                      >
                        {m.status === 'connected' && m.connected_since
                          ? `up ${formatDuration(Math.max(0, Math.floor((Date.now() - new Date(m.connected_since).getTime()) / 1000)))}`
                          : m.last_seen
                            ? `last seen ${relativeTime(m.last_seen)}`
                            : 'Never'}
                      </td>
                      <td className="px-4 py-3">
                        <div className="flex gap-2">
                          <button
                            onClick={() => addTunnel(m.id)}
                            className="px-2 py-1 text-xs bg-green-50 text-green-700 border border-green-200 rounded hover:bg-green-100 flex items-center gap-1"
                          >
                            <Plus size={11} /> Tunnel
                          </button>
                          <button
                            onClick={() => handleDelete(m.id)}
                            className="px-2 py-1 text-xs bg-red-50 text-red-700 border border-red-200 rounded hover:bg-red-100"
                          >
                            Delete
                          </button>
                        </div>
                      </td>
                    </tr>

                    {/* Expanded tunnel list */}
                    {isOpen && (
                      <tr className="bg-gray-50">
                        <td /> {/* chevron col */}
                        <td colSpan={6} className="px-4 py-3">
                          {/* SSH key row */}
                          <div className="flex items-center gap-2 mb-2">
                            <Key size={11} className="text-gray-400 shrink-0" />
                            {m.tunnel_port === 0 ? (
                              <span className="text-xs text-gray-500">
                                SSH access: <span className="font-medium text-gray-700">Disabled (agent-only)</span>
                              </span>
                            ) : reassigning === m.id ? (
                              <>
                                <select
                                  value={reassignKeyID}
                                  onChange={e => setReassignKeyID(e.target.value)}
                                  className="border border-gray-300 rounded px-2 py-0.5 text-xs focus:outline-none focus:ring-1 focus:ring-blue-500 bg-white"
                                  autoFocus
                                >
                                  <option value="">Select a key…</option>
                                  {sshKeys.filter(k => k.id !== m.ssh_key_id).map(k => (
                                    <option key={k.id} value={k.id}>
                                      {k.name}{k.is_default ? ' (default)' : ''}
                                    </option>
                                  ))}
                                </select>
                                <button
                                  onClick={() => { if (reassignKeyID) reassignMutation.mutate({ machineId: m.id, keyId: reassignKeyID }) }}
                                  disabled={!reassignKeyID || reassignMutation.isPending}
                                  className="text-xs px-2 py-0.5 bg-blue-600 text-white rounded hover:bg-blue-700 disabled:opacity-50"
                                >
                                  {reassignMutation.isPending ? 'Saving…' : 'Save'}
                                </button>
                                <button
                                  onClick={() => setReassigning(null)}
                                  className="text-xs text-gray-400 hover:text-gray-600"
                                >
                                  Cancel
                                </button>
                              </>
                            ) : (
                              <>
                                <span className="text-xs text-gray-500">
                                  SSH key:{' '}
                                  <span className="font-medium text-gray-700">
                                    {sshKeys.find(k => k.id === m.ssh_key_id)?.name ?? (m.ssh_key_id ? 'Unknown' : 'None')}
                                  </span>
                                </span>
                                {sshKeys.length > 1 && (
                                  <button
                                    onClick={() => { setReassigning(m.id); setReassignKeyID('') }}
                                    className="text-xs text-blue-600 hover:text-blue-800"
                                  >
                                    Change
                                  </button>
                                )}
                              </>
                            )}
                          </div>
                          {/* SSH command — shown right below the key */}
                          {(() => {
                            const machineKey = sshKeys.find(k => k.id === m.ssh_key_id)
                            const ssh = machineSSHCmd(m, machineKey, jumpboxOS)
                            if (!ssh) return null
                            return (
                              <div className="mt-1 mb-3 space-y-1">
                                <div className="flex items-center gap-2 px-2 py-1.5 bg-slate-50 border border-slate-200 rounded-lg text-xs text-slate-600">
                                  <Terminal size={10} className="shrink-0 text-slate-400" />
                                  <span className="font-medium text-slate-500">{ssh.label}</span>
                                  {ssh.isJumpbox && (
                                    <div className="flex items-center gap-0.5 shrink-0">
                                      {([['unix', <><UnixIcon /><MacIcon /></>], ['windows', <WindowsIcon />]] as [JumpboxOS, React.ReactNode][]).map(([os, icon]) => (
                                        <button
                                          key={os}
                                          onClick={() => setJumpboxOS(os)}
                                          title={os === 'unix' ? 'Linux / macOS' : 'Windows'}
                                          className={`flex items-center gap-0.5 px-1 py-1 rounded transition-colors ${jumpboxOS === os ? 'text-blue-600 bg-blue-100' : 'text-slate-400 hover:text-slate-600 hover:bg-slate-200'}`}
                                        >
                                          {icon}
                                        </button>
                                      ))}
                                    </div>
                                  )}
                                  <code className="font-mono text-slate-700 bg-white border border-slate-200 px-2 py-0.5 rounded select-all flex-1 min-w-0 truncate">{ssh.cmd}</code>
                                  <button onClick={() => { navigator.clipboard.writeText(ssh.cmd); toast.success('Copied!') }} className="text-slate-400 hover:text-slate-600 shrink-0">
                                    <ClipboardCopy size={10} />
                                  </button>
                                </div>
                                {ssh.keyMissing && (
                                  <p className="text-xs text-amber-600 px-1">
                                    SSH key deleted from Gopher — private key may no longer be available. The machine's <code>authorized_keys</code> still has the old public key.
                                  </p>
                                )}
                                {machineKey && (
                                  <p className="text-xs text-gray-400 px-1">
                                    Uses <code>~/.ssh/{toKeyFilename(machineKey.name) || 'id_rsa'}</code> — download from SSH Keys if needed.
                                  </p>
                                )}
                              </div>
                            )
                          })()}
                          <div className="text-xs font-semibold text-gray-500 uppercase tracking-wide mb-2">Tunnels</div>
                          <div className="space-y-1">
                            {/* Built-in SSH tunnel */}
                            {m.tunnel_port > 0 && (() => {
                              const isPublic = m.public_ssh
                              return (
                                <div className="rounded-lg border border-gray-200 bg-white overflow-hidden">
                                  <div className="flex items-center gap-3 px-3 py-2">
                                    <span className="font-mono text-xs text-gray-400 italic">{isPublic ? 'Public' : 'VPS-local'}</span>
                                    <span className="text-gray-300">:{m.tunnel_port}</span>
                                    <span className="text-gray-400 text-xs">→</span>
                                    <span className="font-mono text-xs text-gray-700">localhost:22</span>
                                    <span className="text-xs bg-gray-100 text-gray-500 px-1.5 py-0.5 rounded flex items-center gap-0.5">SSH</span>
                                    {isPublic ? (
                                      <span className="text-xs bg-green-50 text-green-700 px-1.5 py-0.5 rounded flex items-center gap-0.5">
                                        <Globe size={9} /> Public
                                      </span>
                                    ) : (
                                      <span className="text-xs bg-slate-100 text-slate-600 px-1.5 py-0.5 rounded flex items-center gap-0.5">
                                        <Lock size={9} /> Private
                                      </span>
                                    )}
                                    <StatusBadge status={m.ssh_tunnel_status ?? m.status} />
                                  </div>
                                </div>
                              )
                            })()}
                            {/* Agent back-channel — the agent reaches the VPS through a
                                rathole tunnel too (always private, 127.0.0.1 both ends). */}
                            {m.agent_remote_port && m.agent_remote_port > 0 ? (
                              <div className="rounded-lg border border-gray-200 bg-white overflow-hidden">
                                <div className="flex items-center gap-3 px-3 py-2">
                                  <span className="font-mono text-xs text-gray-400 italic">VPS-local</span>
                                  <span className="text-gray-300">:{m.agent_remote_port}</span>
                                  <span className="text-gray-400 text-xs">→</span>
                                  <span className="font-mono text-xs text-gray-700">localhost:{m.agent_local_port ?? 4322}</span>
                                  <span className="text-xs bg-gray-100 text-gray-500 px-1.5 py-0.5 rounded flex items-center gap-0.5">Agent</span>
                                  <span className="text-xs bg-slate-100 text-slate-600 px-1.5 py-0.5 rounded flex items-center gap-0.5">
                                    <Lock size={9} /> Private
                                  </span>
                                  <StatusBadge status={m.agent_tunnel_status ?? (m.agent_installed ? m.status : 'pending')} />
                                </div>
                              </div>
                            ) : null}
                            {/* Service tunnels */}
                            {tunnels.map(t => (
                              <div key={t.id} className="flex items-center gap-3 bg-white border border-gray-200 rounded-lg px-3 py-2">
                                <span className="font-mono text-xs text-gray-700">
                                  {t.subdomain && domain
                                    ? <>{t.subdomain}.{domain}<span className="text-gray-500"> ({displayHost || domain}:{t.rathole_port})</span></>
                                    : `${displayHost || domain}:${t.rathole_port}`}
                                  <span className="text-gray-400"> → </span>
                                  localhost:{t.local_port}
                                </span>
                                <span className={`text-xs px-1.5 py-0.5 rounded uppercase ${t.transport === 'udp' ? 'bg-purple-50 text-purple-700' : 'bg-blue-50 text-blue-600'}`}>
                                  {t.transport === 'udp' ? 'UDP' : t.protocol}
                                </span>
                                <StatusBadge status={t.status} />
                              </div>
                            ))}
                            {tunnels.length === 0 && m.tunnel_port === 0 && !m.agent_remote_port && (
                              <p className="text-xs text-gray-400 italic">No tunnels yet</p>
                            )}
                          </div>
                          <button
                            onClick={() => addTunnel(m.id)}
                            className="mt-2 flex items-center gap-1 text-xs text-blue-600 hover:text-blue-800"
                          >
                            <Plus size={12} /> Add service tunnel
                          </button>

                          {/* Health panel: uptime, sparkline, live agent
                              metrics, manual "Test now". Only shown when
                              the row is expanded — no expense for collapsed
                              rows. */}
                          <MachineHealthPanel machine={m} />
                        </td>
                      </tr>
                    )}
                  </React.Fragment>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      {/* Bootstrap Config Modal */}
      {/* Bootstrap New Machine — combined config + command modal */}
      {bootstrapModal.isOpen && (
        <div className="fixed inset-0 !mt-0 bg-black/60 z-50 overflow-y-auto"><div className="flex min-h-full items-center justify-center p-4">
          <div className="bg-white rounded-xl shadow-2xl w-full max-w-2xl">
            <div className="flex items-center justify-between p-4 border-b">
              <h2 className="text-lg font-semibold">Bootstrap New Machine</h2>
              <button
                onClick={() => {
                  if (bootstrapTimeoutRef.current) clearTimeout(bootstrapTimeoutRef.current)
                  setBootstrapModal(m => ({ ...m, isOpen: false, phase: 'waiting' }))
                }}
                className="text-gray-400 hover:text-gray-600 text-xl"
              >
                ×
              </button>
            </div>

            {bootstrapModal.phase === 'success' ? (
              <div className="p-8 flex flex-col items-center gap-3 text-center">
                <CheckCircle size={48} className="text-green-500" />
                <h3 className="text-lg font-semibold text-gray-900">Machine registered!</h3>
                <p className="text-sm text-gray-500">The machine connected and configured itself successfully.</p>
              </div>
            ) : (
              <div className="p-4 space-y-4">
                <p className="text-sm text-gray-600">Run this on the machine you want to register — it self-configures and connects back to the VPS.</p>

                <div className="relative">
                  <pre className="bg-gray-900 text-green-400 text-xs rounded-lg p-4 pr-12 overflow-x-auto whitespace-pre-wrap break-all min-h-[3rem]">
                    {bootstrapModal.command || 'Generating…'}
                  </pre>
                  <button
                    onClick={copyCommand}
                    disabled={!bootstrapModal.command}
                    className="absolute top-2 right-2 p-1.5 bg-gray-700 hover:bg-gray-600 rounded text-gray-300 disabled:opacity-40"
                    title="Copy command"
                  >
                    {copied ? <Check className="w-4 h-4 text-green-400" /> : <Copy className="w-4 h-4" />}
                  </button>
                </div>

                {bootstrapModal.expiresAt && (
                  <div className="text-xs text-gray-500 bg-amber-50 border border-amber-200 rounded-lg px-3 py-2">
                    ⏱ Token expires at: {new Date(bootstrapModal.expiresAt).toLocaleString()} (1 hour)
                  </div>
                )}

                {/* SSH access — one toggle row; options live in a nested Advanced sub-panel */}
                <div className="border border-gray-200 rounded-lg">
                  <label className="flex items-center gap-2.5 px-3 py-2.5 cursor-pointer" title={sshEnabledInput ? 'SSH enabled — uncheck for agent-only' : 'Agent-only (SSH off)'}>
                    <input
                      type="checkbox"
                      checked={sshEnabledInput}
                      onChange={e => setSSHEnabledInput(e.target.checked)}
                      className="w-4 h-4 rounded border-gray-300 text-blue-600 focus:ring-blue-500 shrink-0"
                    />
                    <span className="text-sm font-medium text-gray-700">SSH access</span>
                    <span className={`ml-auto text-[11px] font-medium px-2 py-0.5 rounded-full shrink-0 ${
                      !sshEnabledInput
                        ? 'bg-gray-100 text-gray-500'
                        : publicSSHInput
                          ? 'bg-amber-50 text-amber-700 border border-amber-200'
                          : 'bg-blue-50 text-blue-600 border border-blue-100'
                    }`}>
                      {sshEnabledInput ? (publicSSHInput ? 'Public' : 'Jumpbox-gated') : 'Agent-only'}
                    </span>
                  </label>
                  {!sshEnabledInput && (
                    <p className="px-3 pb-3 text-xs text-gray-400">Agent-only machine: no SSH tunnel or <code className="bg-gray-100 px-1 rounded">authorized_keys</code> entry — control runs entirely over the agent.</p>
                  )}
                  {sshEnabledInput && (
                    <div className="mx-3 mb-3 rounded-md border border-gray-100 bg-gray-50/80">
                      <button
                        onClick={() => setSSHSectionOpen(o => !o)}
                        className="w-full flex items-center gap-1.5 px-2.5 py-2 text-xs font-medium text-gray-500 hover:text-gray-700 text-left"
                      >
                        {sshSectionOpen ? <ChevronDown size={13} className="shrink-0" /> : <ChevronRight size={13} className="shrink-0" />}
                        Advanced
                        {!sshSectionOpen && (
                          <span className="font-normal text-gray-400 truncate">· key, privacy, port</span>
                        )}
                      </button>
                      {sshSectionOpen && (
                        <div className="px-2.5 pb-3 pt-1 space-y-3">
                          <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1">
                              SSH Key <span className="text-gray-400 font-normal">(optional)</span>
                            </label>
                            <select
                              value={sshKeyInput}
                              onChange={e => setSSHKeyInput(e.target.value)}
                              className="w-full px-3 py-2 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500 bg-white"
                            >
                              <option value="">Use default key</option>
                              {sshKeys.map(k => (
                                <option key={k.id} value={k.id}>
                                  {k.name}{k.is_default ? ' (default)' : ''}
                                </option>
                              ))}
                            </select>
                            <p className="text-xs text-gray-400 mt-1">The selected key's public key is installed on the machine.</p>
                          </div>
                          <label className="flex items-center gap-3 cursor-pointer">
                            <input
                              type="checkbox"
                              checked={!publicSSHInput}
                              onChange={e => setPublicSSHInput(!e.target.checked)}
                              className="w-4 h-4 rounded border-gray-300 text-blue-600 focus:ring-blue-500"
                            />
                            <div>
                              <span className="text-sm font-medium text-gray-700">Jumpbox-gated (private)</span>
                              <p className="text-xs text-gray-400">Reach the box only via <code className="bg-gray-100 px-1 rounded">ssh -J</code> through the VPS. Default is public: sshd is reachable on the VPS public IP (rate-limited at the edge).</p>
                            </div>
                          </label>
                          <div>
                            <label className="block text-sm font-medium text-gray-700 mb-1">
                              SSH Tunnel Port
                              <span className="ml-1 font-normal text-gray-400 text-xs">(port on your VPS — 1024–65535)</span>
                            </label>
                            <ServerPortInput
                              value={tunnelPortInput}
                              onChange={setTunnelPortInput}
                              optional
                              placeholder={bootstrapPortLoading ? 'Finding a free port…' : 'Auto-assign'}
                              skipCheckFor={bootstrapVerifiedPortRef.current}
                            />
                          </div>
                        </div>
                      )}
                    </div>
                  )}
                </div>

                {sshEnabledInput && publicSSHInput && (
                  <div className="text-xs text-amber-700 bg-amber-50 border border-amber-200 rounded-lg px-3 py-2">
                    This machine's SSH will be reachable on the internet (rate-limited at the edge). Choose Jumpbox-gated above for private access, or disable SSH for an agent-only machine.
                  </div>
                )}

                {/* Phase indicator — waiting → verifying → success/timeout */}
                <div className={`flex items-center gap-2 text-xs rounded-lg px-3 py-2 ${
                  bootstrapModal.phase === 'timeout'
                    ? 'bg-amber-50 border border-amber-200 text-amber-700'
                    : 'bg-blue-50 border border-blue-100 text-blue-600'
                }`}>
                  {bootstrapModal.phase === 'timeout' ? (
                    <>⚠ Still waiting — bootstrap is taking longer than expected. Check the machine for errors.</>
                  ) : bootstrapModal.phase === 'verifying' ? (
                    <><Loader2 size={12} className="animate-spin shrink-0" /> Machine registered — waiting for agent to come online…</>
                  ) : (
                    <><Loader2 size={12} className="animate-spin shrink-0" /> Waiting for machine to connect…</>
                  )}
                </div>
              </div>
            )}

            <div className="flex justify-end p-4 border-t">
              <button
                onClick={() => {
                  if (bootstrapTimeoutRef.current) clearTimeout(bootstrapTimeoutRef.current)
                  setBootstrapModal(m => ({ ...m, isOpen: false, phase: 'waiting' }))
                }}
                className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg hover:bg-gray-50 text-sm"
              >
                {bootstrapModal.phase === 'success' ? 'Done' : 'Close'}
              </button>
            </div>
          </div>
        </div></div>
      )}

      {/* Agent Install Modal — operator pastes the curl-bash on the target.
          Closes automatically once HealthService detects the agent is up
          (Machine.agent_installed flips true via the health poll loop). */}
      {agentInstallModal.open && (
        <div className="fixed inset-0 !mt-0 bg-black/60 z-50 overflow-y-auto"><div className="flex min-h-full items-center justify-center p-4">
          <div className="bg-white rounded-xl shadow-2xl w-full max-w-2xl">
            <div className="flex items-center justify-between p-4 border-b">
              <h2 className="text-lg font-semibold">Install agent on {agentInstallModal.machineName}</h2>
              <button
                onClick={() => setAgentInstallModal(m => ({ ...m, open: false }))}
                className="text-gray-400 hover:text-gray-600 text-xl"
              >
                ×
              </button>
            </div>
            <div className="p-4 space-y-4">
              <p className="text-sm text-gray-600">{agentInstallModal.instruction}</p>
              <div className="relative">
                <pre className="bg-gray-900 text-green-400 text-xs rounded-lg p-4 pr-12 overflow-x-auto whitespace-pre-wrap break-all">
                  {agentInstallModal.command}
                </pre>
                <button
                  onClick={() => {
                    navigator.clipboard.writeText(agentInstallModal.command).then(() => {
                      setAgentInstallCopied(true)
                      setTimeout(() => setAgentInstallCopied(false), 2000)
                    })
                  }}
                  className="absolute top-2 right-2 p-1.5 bg-gray-700 hover:bg-gray-600 rounded text-gray-300"
                  title="Copy command"
                >
                  {agentInstallCopied ? <Check className="w-4 h-4 text-green-400" /> : <Copy className="w-4 h-4" />}
                </button>
              </div>
              <div className="text-xs text-gray-500 bg-blue-50 border border-blue-100 rounded-lg px-3 py-2">
                One-time per machine. Once you paste, the agent registers itself via the existing rathole tunnel — no second click needed. The badge on this page flips green within ~60s.
              </div>
            </div>
            <div className="flex justify-end p-4 border-t">
              <button
                onClick={() => setAgentInstallModal(m => ({ ...m, open: false }))}
                className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg hover:bg-gray-50 text-sm"
              >
                Close
              </button>
            </div>
          </div>
        </div></div>
      )}

    </div>
  )
}
