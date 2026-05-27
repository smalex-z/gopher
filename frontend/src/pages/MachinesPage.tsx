import React, { useState, useEffect, useRef, useMemo } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { Server, Copy, Check, ChevronDown, ChevronRight, Plus, Key, Lock, Terminal, ClipboardCopy, Globe, CheckCircle, Loader2 } from 'lucide-react'
import { machinesApi } from '../api/machines'
import { localApi } from '../api/local'
import { vpsApi } from '../api/vps'
import StatusBadge from '../components/StatusBadge'
import MachineHealthPanel from '../components/MachineHealthPanel'
import { relativeTime } from '../lib/time'
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
  const knownMachineIds = useRef<Set<string>>(new Set())
  const bootstrapTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const [configModal, setConfigModal] = useState(false)
  const [tunnelPortInput, setTunnelPortInput] = useState('')
  const [sshKeyInput, setSSHKeyInput] = useState('')
  const [publicSSHInput, setPublicSSHInput] = useState(false)
  const [copied, setCopied] = useState(false)
  const [tokenLoading, setTokenLoading] = useState(false)
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

  const { data: domainIPData } = useQuery({
    queryKey: ['resolve-ip', domain],
    queryFn: () => localApi.resolveIP(domain),
    enabled: !!domain,
    staleTime: 10 * 60 * 1000,
  })
  const { data: routerIPData } = useQuery({
    queryKey: ['resolve-ip', `router.${domain}`],
    queryFn: () => localApi.resolveIP(`router.${domain}`),
    enabled: !!domain,
    staleTime: 10 * 60 * 1000,
  })
  const domainIP = domainIPData?.ip ?? ''
  const routerIP = routerIPData?.ip ?? ''
  const displayHost = domain
    ? (domainIP && routerIP && domainIP === routerIP ? domain : `router.${domain}`)
    : ''

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
    const newMachine = machines.find(m => !knownMachineIds.current.has(m.id))
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

  const openConfigModal = () => {
    setTunnelPortInput('')
    setSSHKeyInput('')
    setPublicSSHInput(false)
    setConfigModal(true)
  }

  const generateToken = async () => {
    const port = tunnelPortInput ? parseInt(tunnelPortInput, 10) : undefined
    const keyID = sshKeyInput || undefined
    setTokenLoading(true)
    try {
      const result = await vpsApi.generateToken(port, keyID, publicSSHInput)
      if (result?.data) {
        // Snapshot current machine IDs so we can detect the new registration
        knownMachineIds.current = new Set(machines.map(m => m.id))
        setConfigModal(false)
        setBootstrapModal({
          isOpen: true,
          command: result.data.bootstrap_command,
          token: result.data.token,
          expiresAt: result.data.expires_at,
          phase: 'waiting',
        })
        // 10-minute timeout — fires from either pre-success phase.
        bootstrapTimeoutRef.current = setTimeout(() => {
          setBootstrapModal(prev =>
            prev.phase === 'waiting' || prev.phase === 'verifying'
              ? { ...prev, phase: 'timeout' }
              : prev,
          )
        }, 10 * 60 * 1000)
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to generate token')
    } finally {
      setTokenLoading(false)
    }
  }

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
          onClick={openConfigModal}
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
            Generate a bootstrap token and run the script on any machine to register it automatically via reverse SSH tunnel.
          </p>
          <button
            onClick={openConfigModal}
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
                {['', 'Name', 'Username', 'Status', 'Agent', 'Last Seen', 'Actions'].map(h => (
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
                        {m.agent_installed ? (
                          <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-xs font-medium text-green-700 bg-green-50 border border-green-200">
                            <CheckCircle size={11} /> v{m.agent_version || '–'}
                          </span>
                        ) : (
                          <button
                            onClick={() => installAgentMutation.mutate(m.id)}
                            disabled={installAgentMutation.isPending && installAgentMutation.variables === m.id}
                            title={m.agent_install_error ? `Last error: ${m.agent_install_error}` : 'Install gopher-agent on this machine'}
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
                        )}
                      </td>
                      <td className="px-4 py-3 text-gray-500" title={m.last_seen ? new Date(m.last_seen).toLocaleString() : ''}>
                        {m.last_seen ? relativeTime(m.last_seen) : 'Never'}
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
                            {reassigning === m.id ? (
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
                                    <StatusBadge status={m.status} />
                                  </div>
                                </div>
                              )
                            })()}
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
                            {tunnels.length === 0 && m.tunnel_port === 0 && (
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
      {configModal && (
        <div className="fixed inset-0 bg-black/60 z-50 overflow-y-auto"><div className="flex min-h-full items-center justify-center p-4">
          <div className="bg-white rounded-xl shadow-2xl w-full max-w-sm">
            <div className="flex items-center justify-between p-4 border-b">
              <h2 className="text-lg font-semibold">Bootstrap New Machine</h2>
              <button
                onClick={() => setConfigModal(false)}
                className="text-gray-400 hover:text-gray-600 text-xl"
              >
                ×
              </button>
            </div>
            <div className="p-4 space-y-4">
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
                <p className="text-xs text-gray-400 mt-1">The selected key's public key will be installed on the machine.</p>
              </div>
              <div>
                <label className="flex items-center gap-3 cursor-pointer">
                  <input
                    type="checkbox"
                    checked={publicSSHInput}
                    onChange={e => setPublicSSHInput(e.target.checked)}
                    className="w-4 h-4 rounded border-gray-300 text-blue-600 focus:ring-blue-500"
                  />
                  <div>
                    <span className="text-sm font-medium text-gray-700">Public SSH access</span>
                    <p className="text-xs text-gray-400">Expose the SSH port publicly on the VPS (0.0.0.0). Default is private (127.0.0.1, jumpbox only).</p>
                  </div>
                </label>
              </div>
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">
                  SSH Tunnel Port <span className="text-gray-400 font-normal">(optional)</span>
                </label>
                <input
                  type="number"
                  value={tunnelPortInput}
                  onChange={e => setTunnelPortInput(e.target.value)}
                  placeholder="Auto-assign"
                  min={1}
                  max={65535}
                  className="w-full px-3 py-2 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
                <p className="text-xs text-gray-400 mt-1">Leave blank to auto-assign the next available port.</p>
              </div>
            </div>
            <div className="flex justify-end gap-2 p-4 border-t">
              <button
                onClick={() => setConfigModal(false)}
                className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg hover:bg-gray-50 text-sm"
              >
                Cancel
              </button>
              <button
                onClick={generateToken}
                disabled={tokenLoading}
                className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 text-sm font-medium disabled:opacity-50"
              >
                {tokenLoading ? 'Generating...' : 'Generate Token'}
              </button>
            </div>
          </div>
        </div></div>
      )}

      {/* Bootstrap Token Modal */}
      {bootstrapModal.isOpen && (
        <div className="fixed inset-0 bg-black/60 z-50 overflow-y-auto"><div className="flex min-h-full items-center justify-center p-4">
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
                <p className="text-sm text-gray-600">
                  Run this command on the machine you want to register. The machine will self-configure and establish a reverse SSH tunnel to the VPS.
                </p>
                <div className="relative">
                  <pre className="bg-gray-900 text-green-400 text-xs rounded-lg p-4 pr-12 overflow-x-auto whitespace-pre-wrap break-all">
                    {bootstrapModal.command}
                  </pre>
                  <button
                    onClick={copyCommand}
                    className="absolute top-2 right-2 p-1.5 bg-gray-700 hover:bg-gray-600 rounded text-gray-300"
                    title="Copy command"
                  >
                    {copied ? <Check className="w-4 h-4 text-green-400" /> : <Copy className="w-4 h-4" />}
                  </button>
                </div>
                <div className="text-xs text-gray-500 bg-amber-50 border border-amber-200 rounded-lg px-3 py-2">
                  ⏱ Token expires at: {new Date(bootstrapModal.expiresAt).toLocaleString()} (1 hour)
                </div>
                <div className="text-xs text-gray-400 space-y-1">
                  <p>The script will:</p>
                  <ol className="list-decimal ml-4 space-y-0.5">
                    <li>Register the machine with this Gopher instance</li>
                    <li>Install the VPS SSH public key in <code className="bg-gray-100 px-1 rounded">~/.ssh/authorized_keys</code></li>
                    <li>Install and configure rathole as a reverse tunnel client</li>
                    <li>Enable a systemd service to keep the tunnel running</li>
                  </ol>
                </div>
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
        <div className="fixed inset-0 bg-black/60 z-50 overflow-y-auto"><div className="flex min-h-full items-center justify-center p-4">
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
