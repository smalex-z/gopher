import React, { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { Server, Copy, Check, ChevronDown, ChevronRight, Plus } from 'lucide-react'
import { machinesApi } from '../api/machines'
import { localApi } from '../api/local'
import { vpsApi } from '../api/vps'
import StatusBadge from '../components/StatusBadge'
import DeployLogModal from '../components/DeployLogModal'
import { toast } from '../lib/toast'
import type { Machine, Tunnel } from '../types'

interface BootstrapModal { isOpen: boolean; command: string; token: string; expiresAt: string }
interface DeployModalState { isOpen: boolean; machineId: string; machineName: string }

export default function MachinesPage() {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [bootstrapModal, setBootstrapModal] = useState<BootstrapModal>({ isOpen: false, command: '', token: '', expiresAt: '' })
  const [deployModal, setDeployModal] = useState<DeployModalState>({ isOpen: false, machineId: '', machineName: '' })
  const [copied, setCopied] = useState(false)
  const [tokenLoading, setTokenLoading] = useState(false)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  const { data, isLoading } = useQuery({ queryKey: ['machines'], queryFn: () => machinesApi.list() })
  const { data: localStatus } = useQuery({ queryKey: ['local-status'], queryFn: () => localApi.status() })
  const machines: Machine[] = data?.data ?? []
  const domain = localStatus?.domain ?? ''

  const deleteMutation = useMutation({
    mutationFn: (id: string) => machinesApi.delete(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['machines'] })
      toast.success('Machine deleted.')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const generateToken = async () => {
    setTokenLoading(true)
    try {
      const result = await vpsApi.generateToken()
      if (result?.data) {
        setBootstrapModal({
          isOpen: true,
          command: result.data.bootstrap_command,
          token: result.data.token,
          expiresAt: result.data.expires_at,
        })
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
    if (window.confirm('Are you sure you want to delete this machine?')) {
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
          onClick={generateToken}
          disabled={tokenLoading}
          className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 text-sm font-medium disabled:opacity-50"
        >
          {tokenLoading ? 'Generating...' : '+ Bootstrap New Machine'}
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
            onClick={generateToken}
            disabled={tokenLoading}
            className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 text-sm font-medium disabled:opacity-50"
          >
            {tokenLoading ? 'Generating...' : 'Generate Bootstrap Token'}
          </button>
        </div>
      ) : (
        <div className="bg-white rounded-xl shadow-sm border overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-gray-50 border-b">
              <tr>
                {['', 'Name', 'Username', 'Status', 'Last Seen', 'Actions'].map(h => (
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
                      <td className="px-4 py-3 text-gray-500">{m.last_seen ? new Date(m.last_seen).toLocaleString() : 'Never'}</td>
                      <td className="px-4 py-3">
                        <div className="flex gap-2">
                          <button
                            onClick={() => addTunnel(m.id)}
                            className="px-2 py-1 text-xs bg-green-50 text-green-700 border border-green-200 rounded hover:bg-green-100 flex items-center gap-1"
                          >
                            <Plus size={11} /> Tunnel
                          </button>
                          <button
                            onClick={() => setDeployModal({ isOpen: true, machineId: m.id, machineName: m.name })}
                            className="px-2 py-1 text-xs bg-blue-50 text-blue-700 border border-blue-200 rounded hover:bg-blue-100"
                          >
                            Deploy
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
                        <td colSpan={5} className="px-4 py-3">
                          <div className="text-xs font-semibold text-gray-500 uppercase tracking-wide mb-2">Tunnels</div>
                          <div className="space-y-1">
                            {/* Built-in SSH tunnel */}
                            {m.tunnel_port > 0 && (
                              <div className="flex items-center gap-3 bg-white border border-gray-200 rounded-lg px-3 py-2">
                                <span className="font-mono text-xs text-gray-700">
                                  {domain ? `${domain}:${m.tunnel_port}` : `:${m.tunnel_port}`}
                                  <span className="text-gray-400"> → </span>
                                  localhost:22
                                </span>
                                <span className="text-xs bg-gray-100 text-gray-500 px-1.5 py-0.5 rounded">SSH</span>
                                <StatusBadge status={m.status === 'connected' ? 'active' : m.status} />
                              </div>
                            )}
                            {/* Service tunnels */}
                            {tunnels.map(t => (
                              <div key={t.id} className="flex items-center gap-3 bg-white border border-gray-200 rounded-lg px-3 py-2">
                                <span className="font-mono text-xs text-gray-700">
                                  {domain ? `${t.subdomain}.${domain}` : t.subdomain}
                                  <span className="text-gray-400"> → </span>
                                  localhost:{t.local_port}
                                </span>
                                <span className="text-xs bg-blue-50 text-blue-600 px-1.5 py-0.5 rounded uppercase">{t.protocol}</span>
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

      {/* Bootstrap Token Modal */}
      {bootstrapModal.isOpen && (
        <div className="fixed inset-0 bg-black/60 z-50 flex items-center justify-center p-4">
          <div className="bg-white rounded-xl shadow-2xl w-full max-w-2xl">
            <div className="flex items-center justify-between p-4 border-b">
              <h2 className="text-lg font-semibold">Bootstrap New Machine</h2>
              <button
                onClick={() => setBootstrapModal(m => ({ ...m, isOpen: false }))}
                className="text-gray-400 hover:text-gray-600 text-xl"
              >
                ×
              </button>
            </div>
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
            </div>
            <div className="flex justify-end p-4 border-t">
              <button
                onClick={() => setBootstrapModal(m => ({ ...m, isOpen: false }))}
                className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg hover:bg-gray-50 text-sm"
              >
                Close
              </button>
            </div>
          </div>
        </div>
      )}

      <DeployLogModal
        isOpen={deployModal.isOpen}
        onClose={() => setDeployModal(d => ({ ...d, isOpen: false }))}
        title={`Deploy Client to ${deployModal.machineName}`}
        onStart={() => machinesApi.deploy(deployModal.machineId)}
      />
    </div>
  )
}
