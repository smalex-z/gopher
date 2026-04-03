import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Shield, Plus, Trash2, RefreshCw, Terminal, ChevronDown, ChevronRight, AlertTriangle, CheckCircle2, Lock, Globe } from 'lucide-react'
import { localApi } from '../api/local'
import { toast } from '../lib/toast'
import type { FirewallEntry } from '../types'

const PROTOCOL_OPTIONS = ['tcp', 'udp', 'all', 'icmp']
const ACTION_OPTIONS = ['ACCEPT', 'DROP', 'REJECT']

function actionBadge(action: string) {
  if (action === 'ACCEPT') return <span className="text-xs px-1.5 py-0.5 rounded bg-green-50 text-green-700 border border-green-200 font-medium">ACCEPT</span>
  if (action === 'DROP')   return <span className="text-xs px-1.5 py-0.5 rounded bg-red-50 text-red-700 border border-red-200 font-medium">DROP</span>
  return <span className="text-xs px-1.5 py-0.5 rounded bg-amber-50 text-amber-700 border border-amber-200 font-medium">REJECT</span>
}

function typeBadge(type: FirewallEntry['type']) {
  if (type === 'system')      return <span className="text-xs px-1.5 py-0.5 rounded bg-gray-100 text-gray-500">System</span>
  if (type === 'tunnel')      return <span className="text-xs px-1.5 py-0.5 rounded bg-blue-50 text-blue-600">Tunnel</span>
  if (type === 'machine-ssh') return <span className="text-xs px-1.5 py-0.5 rounded bg-purple-50 text-purple-600">Machine SSH</span>
  return <span className="text-xs px-1.5 py-0.5 rounded bg-amber-50 text-amber-700">Custom</span>
}

function sourceBadge(source: string) {
  if (source === '0.0.0.0/0') return <span className="flex items-center gap-0.5 text-xs text-gray-500"><Globe size={10} /> Any</span>
  if (source === '127.0.0.1') return <span className="flex items-center gap-0.5 text-xs text-slate-600"><Lock size={10} /> localhost</span>
  return <span className="font-mono text-xs text-gray-600">{source}</span>
}

const emptyForm = { description: '', raw: false, raw_spec: '', protocol: 'tcp', port_range: '', source: '', action: 'ACCEPT' }

export default function FirewallPage() {
  const qc = useQueryClient()
  const [showAdd, setShowAdd] = useState(false)
  const [showLive, setShowLive] = useState(false)
  const [form, setForm] = useState({ ...emptyForm })

  const { data: overviewData, isLoading } = useQuery({
    queryKey: ['firewall-overview'],
    queryFn: () => localApi.firewallOverview(),
  })
  const entries: FirewallEntry[] = overviewData?.data ?? []

  const { data: statusData } = useQuery({
    queryKey: ['local-status'],
    queryFn: () => localApi.status(),
  })
  const mode = statusData?.firewall_mode ?? ''
  const isGopherManaged = mode === 'gopher'

  const { data: liveData, refetch: refetchLive, isFetching: liveLoading } = useQuery({
    queryKey: ['firewall-live'],
    queryFn: () => localApi.getLiveRules(),
    enabled: showLive,
    staleTime: 0,
  })
  const liveChains = liveData?.data ?? {}

  const createMutation = useMutation({
    mutationFn: () => localApi.createFirewallRule(
      form.raw
        ? { description: form.description, raw: true, raw_spec: form.raw_spec }
        : { description: form.description, protocol: form.protocol, port_range: form.port_range, source: form.source || '0.0.0.0/0', action: form.action }
    ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['firewall-overview'] })
      setShowAdd(false)
      setForm({ ...emptyForm })
      toast.success('Rule added')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) => localApi.deleteFirewallRule(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['firewall-overview'] })
      toast.success('Rule deleted')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const reloadMutation = useMutation({
    mutationFn: () => localApi.reloadFirewall(),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['firewall-overview'] })
      qc.invalidateQueries({ queryKey: ['firewall-live'] })
      toast.success('Firewall reloaded')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const modeBadge = () => {
    if (mode === 'gopher') return <span className="text-xs bg-blue-100 text-blue-700 px-2 py-0.5 rounded-full font-medium flex items-center gap-1"><CheckCircle2 size={11} /> Gopher-managed</span>
    if (mode === 'manual') return <span className="text-xs bg-amber-100 text-amber-700 px-2 py-0.5 rounded-full font-medium flex items-center gap-1"><AlertTriangle size={11} /> Manual</span>
    if (mode === 'none')   return <span className="text-xs bg-gray-100 text-gray-500 px-2 py-0.5 rounded-full font-medium">None</span>
    return <span className="text-xs bg-gray-100 text-gray-400 px-2 py-0.5 rounded-full font-medium">Not configured</span>
  }

  if (isLoading) return <div className="text-gray-400 text-center py-12">Loading…</div>

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-gray-900 flex items-center gap-2"><Shield size={22} /> Firewall</h1>
          <div className="flex items-center gap-2 mt-1">
            <span className="text-gray-400 text-sm">Mode:</span>
            {modeBadge()}
          </div>
        </div>
        <div className="flex gap-2">
          {isGopherManaged && (
            <>
              <button
                onClick={() => reloadMutation.mutate()}
                disabled={reloadMutation.isPending}
                className="flex items-center gap-1.5 px-3 py-2 text-sm border border-gray-300 rounded-lg hover:bg-gray-50 text-gray-600 disabled:opacity-50"
              >
                <RefreshCw size={14} className={reloadMutation.isPending ? 'animate-spin' : ''} /> Reload
              </button>
              <button
                onClick={() => setShowAdd(v => !v)}
                className="flex items-center gap-1.5 px-3 py-2 text-sm bg-blue-600 text-white rounded-lg hover:bg-blue-700 font-medium"
              >
                <Plus size={14} /> Add Rule
              </button>
            </>
          )}
        </div>
      </div>

      {!isGopherManaged && (
        <div className="bg-amber-50 border border-amber-200 rounded-xl p-4 flex items-start gap-3 text-sm text-amber-800">
          <AlertTriangle size={16} className="mt-0.5 shrink-0" />
          <div>Firewall management is <strong>{mode || 'not configured'}</strong>. Rules require Gopher-managed mode.</div>
        </div>
      )}

      {/* Add Rule */}
      {showAdd && (
        <div className="bg-white rounded-xl shadow-sm border p-5 space-y-4">
          <div className="flex items-center justify-between">
            <h2 className="font-semibold text-gray-900">New Rule</h2>
            {/* Raw / Structured toggle */}
            <div className="flex rounded-lg border border-gray-300 overflow-hidden text-sm">
              <button
                onClick={() => setForm(f => ({ ...f, raw: false }))}
                className={`px-3 py-1.5 ${!form.raw ? 'bg-blue-600 text-white' : 'text-gray-600 hover:bg-gray-50'}`}
              >
                Structured
              </button>
              <button
                onClick={() => setForm(f => ({ ...f, raw: true }))}
                className={`px-3 py-1.5 border-l border-gray-300 ${form.raw ? 'bg-blue-600 text-white' : 'text-gray-600 hover:bg-gray-50'}`}
              >
                Raw
              </button>
            </div>
          </div>

          {form.raw ? (
            <div className="space-y-3">
              <div>
                <label className="block text-xs font-medium text-gray-600 mb-1">Description <span className="text-gray-400 font-normal">(optional)</span></label>
                <input
                  value={form.description}
                  onChange={e => setForm(f => ({ ...f, description: e.target.value }))}
                  placeholder="e.g. Allow monitoring agent"
                  className="w-full px-3 py-1.5 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
              </div>
              <div>
                <label className="block text-xs font-medium text-gray-600 mb-1">iptables rule spec</label>
                <input
                  value={form.raw_spec}
                  onChange={e => setForm(f => ({ ...f, raw_spec: e.target.value }))}
                  placeholder="-s 203.0.113.5 -p tcp --dport 3000 -j ACCEPT"
                  className="w-full px-3 py-1.5 border border-gray-300 rounded-lg text-sm font-mono focus:outline-none focus:ring-2 focus:ring-blue-500 bg-gray-50"
                />
                <p className="text-xs text-gray-400 mt-1">Everything after <code className="bg-gray-100 px-1 rounded">iptables -A GOPHER_CUSTOM</code>. Lines starting with <code className="bg-gray-100 px-1 rounded">-A</code> or <code className="bg-gray-100 px-1 rounded">-I</code> are passed through as-is.</p>
              </div>
            </div>
          ) : (
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <div className="col-span-2 sm:col-span-4">
                <label className="block text-xs font-medium text-gray-600 mb-1">Description <span className="text-gray-400 font-normal">(optional)</span></label>
                <input
                  value={form.description}
                  onChange={e => setForm(f => ({ ...f, description: e.target.value }))}
                  placeholder="e.g. Allow monitoring agent"
                  className="w-full px-3 py-1.5 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
              </div>
              <div>
                <label className="block text-xs font-medium text-gray-600 mb-1">Protocol</label>
                <select value={form.protocol} onChange={e => setForm(f => ({ ...f, protocol: e.target.value }))}
                  className="w-full px-3 py-1.5 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500 bg-white">
                  {PROTOCOL_OPTIONS.map(p => <option key={p}>{p}</option>)}
                </select>
              </div>
              <div>
                <label className="block text-xs font-medium text-gray-600 mb-1">Port / Range</label>
                <input
                  value={form.port_range}
                  onChange={e => setForm(f => ({ ...f, port_range: e.target.value }))}
                  placeholder="80  or  8000:9000"
                  className="w-full px-3 py-1.5 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
              </div>
              <div>
                <label className="block text-xs font-medium text-gray-600 mb-1">Source CIDR</label>
                <input
                  value={form.source}
                  onChange={e => setForm(f => ({ ...f, source: e.target.value }))}
                  placeholder="0.0.0.0/0"
                  className="w-full px-3 py-1.5 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
              </div>
              <div>
                <label className="block text-xs font-medium text-gray-600 mb-1">Action</label>
                <select value={form.action} onChange={e => setForm(f => ({ ...f, action: e.target.value }))}
                  className="w-full px-3 py-1.5 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500 bg-white">
                  {ACTION_OPTIONS.map(a => <option key={a}>{a}</option>)}
                </select>
              </div>
            </div>
          )}

          <div className="flex justify-end gap-2 pt-1">
            <button onClick={() => { setShowAdd(false); setForm({ ...emptyForm }) }}
              className="px-3 py-1.5 text-sm border border-gray-300 rounded-lg hover:bg-gray-50 text-gray-600">
              Cancel
            </button>
            <button
              onClick={() => createMutation.mutate()}
              disabled={createMutation.isPending || (form.raw ? !form.raw_spec.trim() : false)}
              className="px-3 py-1.5 text-sm bg-blue-600 text-white rounded-lg hover:bg-blue-700 disabled:opacity-50"
            >
              {createMutation.isPending ? 'Adding…' : 'Add Rule'}
            </button>
          </div>
        </div>
      )}

      {/* Main table */}
      <div className="bg-white rounded-xl shadow-sm border overflow-hidden">
        <div className="px-5 py-3 border-b bg-gray-50 flex items-center justify-between">
          <span className="text-sm font-semibold text-gray-700">
            Open Ports &amp; Rules
            <span className="ml-2 text-xs font-normal text-gray-400">({entries.length} entries)</span>
          </span>
        </div>
        {entries.length === 0 ? (
          <div className="py-10 text-center text-sm text-gray-400">No rules — firewall may not be active.</div>
        ) : (
          <table className="w-full text-sm">
            <thead className="border-b">
              <tr>
                {['Port / Service', 'Protocol', 'Source', 'Action', 'Managed by', ''].map(h => (
                  <th key={h} className="text-left px-4 py-2.5 text-xs font-semibold text-gray-500 uppercase tracking-wide">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y">
              {entries.map((entry, i) => (
                <tr key={entry.id ?? `${entry.type}-${i}`} className="hover:bg-gray-50">
                  <td className="px-4 py-2.5">
                    <div className="font-medium text-gray-800">
                      {entry.raw ? (
                        <code className="text-xs bg-gray-100 px-1.5 py-0.5 rounded text-gray-700">{entry.raw_spec}</code>
                      ) : (
                        entry.port_range || <span className="text-gray-400 italic">any</span>
                      )}
                    </div>
                    {entry.description && <div className="text-xs text-gray-400 mt-0.5">{entry.description}</div>}
                  </td>
                  <td className="px-4 py-2.5 font-mono text-xs text-gray-600">
                    {entry.raw ? '—' : (entry.protocol || 'all')}
                  </td>
                  <td className="px-4 py-2.5">{entry.raw ? '—' : sourceBadge(entry.source)}</td>
                  <td className="px-4 py-2.5">{actionBadge(entry.action)}</td>
                  <td className="px-4 py-2.5">{typeBadge(entry.type)}</td>
                  <td className="px-4 py-2.5 w-8">
                    {entry.type === 'custom' && entry.id && (
                      <button
                        onClick={() => { if (window.confirm('Delete this rule?')) deleteMutation.mutate(entry.id!) }}
                        className="text-gray-300 hover:text-red-500 transition-colors"
                      >
                        <Trash2 size={14} />
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {/* Live iptables (collapsible) */}
      <div className="bg-white rounded-xl shadow-sm border overflow-hidden">
        <button
          className="w-full flex items-center justify-between px-5 py-4 hover:bg-gray-50 transition-colors"
          onClick={() => { setShowLive(v => !v); if (!showLive) refetchLive() }}
        >
          <div className="flex items-center gap-2">
            <Terminal size={15} className="text-gray-500" />
            <span className="font-semibold text-gray-900 text-sm">Live iptables Output</span>
            <span className="text-xs text-gray-400">GOPHER_CUSTOM + GOPHER_TUNNELS</span>
          </div>
          <div className="flex items-center gap-2">
            {showLive && (
              <button onClick={e => { e.stopPropagation(); refetchLive() }} className="text-gray-400 hover:text-gray-600 p-1">
                <RefreshCw size={13} className={liveLoading ? 'animate-spin' : ''} />
              </button>
            )}
            {showLive ? <ChevronDown size={16} className="text-gray-400" /> : <ChevronRight size={16} className="text-gray-400" />}
          </div>
        </button>
        {showLive && (
          <div className="border-t divide-y">
            {Object.entries(liveChains).map(([chain, output]) => (
              <div key={chain}>
                <div className="px-5 py-2 bg-gray-900 border-b border-gray-700">
                  <span className="text-xs font-mono font-semibold text-gray-300">{chain}</span>
                </div>
                <pre className="px-5 py-3 text-xs font-mono bg-black text-green-400 overflow-x-auto whitespace-pre">{output}</pre>
              </div>
            ))}
            {Object.keys(liveChains).length === 0 && (
              <div className="px-5 py-6 text-sm text-gray-400 text-center">
                {liveLoading ? 'Loading…' : 'No data — firewall may not be active.'}
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
