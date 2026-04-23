import { useState } from 'react'
import { Copy, Check, ChevronDown, ChevronRight } from 'lucide-react'

// ─── Utilities ────────────────────────────────────────────────────────────────

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  const copy = () => {
    navigator.clipboard.writeText(text).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }
  return (
    <button
      onClick={copy}
      className="absolute top-2 right-2 p-1.5 rounded text-gray-400 hover:text-gray-200 hover:bg-white/10 transition-colors"
      title="Copy"
    >
      {copied ? <Check size={14} className="text-green-400" /> : <Copy size={14} />}
    </button>
  )
}

function CodeBlock({ code, lang = 'bash' }: { code: string; lang?: string }) {
  return (
    <div className="relative">
      <pre className={`language-${lang} bg-gray-900 text-gray-100 rounded-xl p-4 text-xs overflow-x-auto font-mono leading-relaxed`}>
        <code>{code.trim()}</code>
      </pre>
      <CopyButton text={code.trim()} />
    </div>
  )
}

function Badge({ method }: { method: string }) {
  const colors: Record<string, string> = {
    GET: 'bg-blue-100 text-blue-700',
    POST: 'bg-green-100 text-green-700',
    DELETE: 'bg-red-100 text-red-700',
    PUT: 'bg-amber-100 text-amber-700',
  }
  return (
    <span className={`inline-block px-2 py-0.5 rounded text-xs font-bold font-mono ${colors[method] ?? 'bg-gray-100 text-gray-600'}`}>
      {method}
    </span>
  )
}

interface Field {
  name: string
  type: string
  required?: boolean
  description: string
}

function FieldTable({ fields }: { fields: Field[] }) {
  return (
    <table className="w-full text-sm border-collapse">
      <thead>
        <tr className="border-b border-gray-200">
          <th className="text-left py-2 pr-4 font-medium text-gray-500 text-xs w-1/4">Field</th>
          <th className="text-left py-2 pr-4 font-medium text-gray-500 text-xs w-1/6">Type</th>
          <th className="text-left py-2 font-medium text-gray-500 text-xs">Description</th>
        </tr>
      </thead>
      <tbody className="divide-y divide-gray-100">
        {fields.map(f => (
          <tr key={f.name}>
            <td className="py-2 pr-4">
              <code className="text-xs font-mono text-gray-800">{f.name}</code>
              {f.required && <span className="ml-1 text-red-500 text-xs">*</span>}
            </td>
            <td className="py-2 pr-4 text-xs text-gray-500 font-mono">{f.type}</td>
            <td className="py-2 text-xs text-gray-600">{f.description}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

interface EndpointProps {
  method: string
  path: string
  summary: string
  description?: string
  requestFields?: Field[]
  queryFields?: Field[]
  responseFields?: Field[]
  curlExample: string
  responseExample: string
}

function Endpoint(props: EndpointProps) {
  const [open, setOpen] = useState(false)

  return (
    <div className="border border-gray-200 rounded-xl overflow-hidden">
      <button
        onClick={() => setOpen(o => !o)}
        className="w-full flex items-center gap-3 px-5 py-4 text-left hover:bg-gray-50 transition-colors"
      >
        <Badge method={props.method} />
        <code className="text-sm font-mono text-gray-800 flex-1">{props.path}</code>
        <span className="text-sm text-gray-500 hidden sm:block">{props.summary}</span>
        {open ? <ChevronDown size={16} className="text-gray-400 shrink-0" /> : <ChevronRight size={16} className="text-gray-400 shrink-0" />}
      </button>

      {open && (
        <div className="border-t border-gray-200 px-5 py-5 space-y-5 bg-white">
          {props.description && <p className="text-sm text-gray-600">{props.description}</p>}

          {props.requestFields && props.requestFields.length > 0 && (
            <div>
              <h4 className="text-xs font-semibold text-gray-500 uppercase tracking-wide mb-2">Request body</h4>
              <FieldTable fields={props.requestFields} />
            </div>
          )}

          {props.queryFields && props.queryFields.length > 0 && (
            <div>
              <h4 className="text-xs font-semibold text-gray-500 uppercase tracking-wide mb-2">Query parameters</h4>
              <FieldTable fields={props.queryFields} />
            </div>
          )}

          {props.responseFields && props.responseFields.length > 0 && (
            <div>
              <h4 className="text-xs font-semibold text-gray-500 uppercase tracking-wide mb-2">Response fields</h4>
              <FieldTable fields={props.responseFields} />
            </div>
          )}

          <div>
            <h4 className="text-xs font-semibold text-gray-500 uppercase tracking-wide mb-2">Example request</h4>
            <CodeBlock code={props.curlExample} lang="bash" />
          </div>

          <div>
            <h4 className="text-xs font-semibold text-gray-500 uppercase tracking-wide mb-2">Example response</h4>
            <CodeBlock code={props.responseExample} lang="json" />
          </div>
        </div>
      )}
    </div>
  )
}

// ─── Page ─────────────────────────────────────────────────────────────────────

const BASE = 'https://your-domain.com'
const KEY = '$GOPHER_API_KEY'

export default function DocsPage() {
  return (
    <div className="max-w-4xl space-y-10">
      <div>
        <h1 className="text-2xl font-bold text-gray-900">External API</h1>
        <p className="text-sm text-gray-500 mt-1">REST API for programmatic tunnel management.</p>
      </div>

      {/* Authentication */}
      <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-6 space-y-4">
        <h2 className="font-semibold text-gray-900">Authentication</h2>
        <p className="text-sm text-gray-600">
          All <code className="font-mono text-xs bg-gray-100 px-1 rounded">/api/v1/</code> endpoints require a Bearer token
          in the <code className="font-mono text-xs bg-gray-100 px-1 rounded">Authorization</code> header.
          Generate or rotate your key in <a href="/security" className="text-blue-600 hover:underline">Access Control</a>,
          or set the <code className="font-mono text-xs bg-gray-100 px-1 rounded">GOPHER_API_KEY</code> environment variable.
        </p>
        <CodeBlock code={`curl -H "Authorization: Bearer ${KEY}" ${BASE}/api/v1/machines`} />

        <div className="rounded-xl border border-gray-200 overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-gray-50 border-b border-gray-200">
              <tr>
                <th className="text-left py-2 px-4 font-medium text-gray-500 text-xs">Status</th>
                <th className="text-left py-2 px-4 font-medium text-gray-500 text-xs">Meaning</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              <tr><td className="py-2 px-4 font-mono text-xs text-gray-600">401</td><td className="py-2 px-4 text-xs text-gray-600">Missing or invalid API key</td></tr>
              <tr><td className="py-2 px-4 font-mono text-xs text-gray-600">503</td><td className="py-2 px-4 text-xs text-gray-600">No API key configured on the server</td></tr>
            </tbody>
          </table>
        </div>
      </div>

      {/* Workflow */}
      <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-6 space-y-4">
        <h2 className="font-semibold text-gray-900">Workflow</h2>
        <p className="text-sm text-gray-600">
          Machines and tunnels are separate resources. Bootstrap the machine first, wait for it to connect, then create tunnels on it.
          One machine can have multiple tunnels.
        </p>
        <ol className="space-y-2 text-sm text-gray-600 list-decimal list-inside">
          <li><strong>POST /api/v1/machines</strong> → returns <code className="font-mono text-xs bg-gray-100 px-1 rounded">bootstrap_url</code></li>
          <li>Run <code className="font-mono text-xs bg-gray-100 px-1 rounded">curl &lt;bootstrap_url&gt; | bash</code> on the target VM</li>
          <li>Poll <strong>GET /api/v1/machines/:id</strong> until <code className="font-mono text-xs bg-gray-100 px-1 rounded">status</code> is <span className="text-green-700 font-medium">connected</span></li>
          <li><strong>POST /api/v1/tunnels</strong> with <code className="font-mono text-xs bg-gray-100 px-1 rounded">machine_id</code> — synchronous, returns the live tunnel URL immediately</li>
          <li>Repeat step 4 for each additional service on that machine</li>
          <li><strong>DELETE /api/v1/machines/:id</strong> to teardown everything when done</li>
        </ol>

        <div className="flex items-center gap-2 text-sm flex-wrap">
          <span className="text-xs font-medium text-gray-500">Machine status:</span>
          {['pending', '→', 'connected', '/', 'failed'].map((s, i) => (
            s === '→' || s === '/' ? (
              <span key={i} className="text-gray-400 text-xs">{s}</span>
            ) : (
              <span key={i} className={`px-2 py-0.5 rounded-full text-xs font-medium border ${
                s === 'connected' ? 'bg-green-50 text-green-700 border-green-200' :
                s === 'failed'    ? 'bg-red-50 text-red-700 border-red-200' :
                                    'bg-amber-50 text-amber-700 border-amber-200'
              }`}>{s}</span>
            )
          ))}
        </div>
      </div>

      {/* Machines */}
      <div className="space-y-3">
        <h2 className="font-semibold text-gray-900 text-lg">Machines</h2>

        <Endpoint
          method="POST"
          path="/api/v1/machines"
          summary="Bootstrap a machine"
          description="Generates a one-time bootstrap token and returns a bootstrap_url. Run that URL on the target VM to install the rathole client and register the machine with Gopher. All fields are optional."
          requestFields={[
            { name: 'public_ssh', type: 'boolean', description: 'Expose the SSH back-tunnel publicly (default false — VPS-local only).' },
            { name: 'ssh_key_id', type: 'string', description: "ID of the SSH key to install on the machine. Defaults to the server's default key." },
          ]}
          responseFields={[
            { name: 'id', type: 'string', description: 'Machine record ID — use for GET and DELETE.' },
            { name: 'status', type: 'string', description: '"pending" until the VM connects.' },
            { name: 'bootstrap_url', type: 'string', description: 'One-time URL. Run on the VM: curl <bootstrap_url> | bash' },
            { name: 'public_ssh', type: 'boolean', description: 'Whether the SSH back-tunnel is publicly reachable.' },
            { name: 'created_at', type: 'string', description: 'ISO 8601 timestamp.' },
          ]}
          curlExample={`curl -X POST ${BASE}/api/v1/machines \\
  -H "Authorization: Bearer ${KEY}" \\
  -H "Content-Type: application/json" \\
  -d '{"public_ssh": false}'`}
          responseExample={`{
  "success": true,
  "data": {
    "id": "a1b2c3d4e5f6a7b8",
    "status": "pending",
    "public_ssh": false,
    "bootstrap_url": "${BASE}/bootstrap/tok_abc123...",
    "created_at": "2026-04-23T12:00:00Z"
  }
}`}
        />

        <Endpoint
          method="GET"
          path="/api/v1/machines"
          summary="List machines"
          description="Returns a paginated list of all externally-managed machines."
          queryFields={[
            { name: 'limit', type: 'integer', description: 'Max results (default 50, max 200).' },
            { name: 'offset', type: 'integer', description: 'Pagination offset (default 0).' },
          ]}
          responseFields={[
            { name: 'items', type: 'array', description: 'Array of machine objects.' },
            { name: 'total', type: 'integer', description: 'Total number of records.' },
            { name: 'limit', type: 'integer', description: 'Applied limit.' },
            { name: 'offset', type: 'integer', description: 'Applied offset.' },
          ]}
          curlExample={`curl "${BASE}/api/v1/machines?limit=10&offset=0" \\
  -H "Authorization: Bearer ${KEY}"`}
          responseExample={`{
  "success": true,
  "data": {
    "items": [
      {
        "id": "a1b2c3d4e5f6a7b8",
        "status": "connected",
        "public_ssh": false,
        "created_at": "2026-04-23T12:00:00Z"
      }
    ],
    "total": 1,
    "limit": 10,
    "offset": 0
  }
}`}
        />

        <Endpoint
          method="GET"
          path="/api/v1/machines/{id}"
          summary="Get a machine"
          description="Poll this endpoint after bootstrap to check when the machine connects. Status is derived live from the underlying machine record — no separate state to manage."
          responseFields={[
            { name: 'id', type: 'string', description: 'Machine record ID.' },
            { name: 'status', type: 'string', description: '"pending" | "connected" | "failed".' },
            { name: 'public_ssh', type: 'boolean', description: 'Whether the SSH back-tunnel is public.' },
            { name: 'error', type: 'string', description: 'Failure reason (present when failed).' },
            { name: 'created_at', type: 'string', description: 'ISO 8601 timestamp.' },
          ]}
          curlExample={`curl ${BASE}/api/v1/machines/a1b2c3d4e5f6a7b8 \\
  -H "Authorization: Bearer ${KEY}"`}
          responseExample={`{
  "success": true,
  "data": {
    "id": "a1b2c3d4e5f6a7b8",
    "status": "connected",
    "public_ssh": false,
    "created_at": "2026-04-23T12:00:00Z"
  }
}`}
        />

        <Endpoint
          method="DELETE"
          path="/api/v1/machines/{id}"
          summary="Delete a machine"
          description="Full teardown: SSHes into the VM to remove the rathole client service, removes all associated tunnels and Caddy routes, reconciles the rathole server config, and deletes all database records. SSH failures are best-effort and non-fatal — safe to call after the VM is destroyed."
          curlExample={`curl -X DELETE ${BASE}/api/v1/machines/a1b2c3d4e5f6a7b8 \\
  -H "Authorization: Bearer ${KEY}"`}
          responseExample={`HTTP 204 No Content`}
        />
      </div>

      {/* Tunnels */}
      <div className="space-y-3">
        <h2 className="font-semibold text-gray-900 text-lg">Tunnels</h2>

        <Endpoint
          method="POST"
          path="/api/v1/tunnels"
          summary="Create a tunnel"
          description="Creates a service tunnel on an already-connected machine. Synchronous — the tunnel is live (or failed) by the time this call returns. One machine can have multiple tunnels."
          requestFields={[
            { name: 'machine_id', type: 'string', required: true, description: 'ID of a connected machine (from POST /api/v1/machines).' },
            { name: 'target_port', type: 'integer', required: true, description: 'Port the service listens on inside the remote machine.' },
            { name: 'subdomain', type: 'string', description: 'DNS subdomain (e.g. "myapp" → myapp.your-domain.com). Auto-generated from machine name if omitted.' },
            { name: 'target_ip', type: 'string', description: 'IP of the service on the remote machine. Defaults to 127.0.0.1.' },
            { name: 'private', type: 'boolean', description: 'Bind tunnel to 127.0.0.1 on the VPS (not publicly reachable). Default false.' },
            { name: 'no_tls', type: 'boolean', description: 'Skip Caddy TLS — serve plain http:// instead of https://. Default false.' },
          ]}
          responseFields={[
            { name: 'id', type: 'string', description: 'Tunnel record ID.' },
            { name: 'machine_id', type: 'string', description: 'The machine this tunnel belongs to.' },
            { name: 'status', type: 'string', description: '"active" or "failed".' },
            { name: 'subdomain', type: 'string', description: 'The subdomain (generated or specified).' },
            { name: 'target_ip', type: 'string', description: 'Target IP.' },
            { name: 'target_port', type: 'integer', description: 'Target port.' },
            { name: 'tunnel_url', type: 'string', description: 'Public URL (present when active).' },
            { name: 'error', type: 'string', description: 'Failure reason (present when failed).' },
            { name: 'created_at', type: 'string', description: 'ISO 8601 timestamp.' },
          ]}
          curlExample={`curl -X POST ${BASE}/api/v1/tunnels \\
  -H "Authorization: Bearer ${KEY}" \\
  -H "Content-Type: application/json" \\
  -d '{"machine_id":"a1b2c3d4e5f6a7b8","target_port":3000}'`}
          responseExample={`{
  "success": true,
  "data": {
    "id": "f8e7d6c5b4a39281",
    "machine_id": "a1b2c3d4e5f6a7b8",
    "status": "active",
    "subdomain": "my-server-a1b2c3",
    "target_ip": "127.0.0.1",
    "target_port": 3000,
    "tunnel_url": "https://my-server-a1b2c3.your-domain.com",
    "created_at": "2026-04-23T12:01:00Z"
  }
}`}
        />

        <Endpoint
          method="GET"
          path="/api/v1/tunnels"
          summary="List tunnels"
          description="Returns a paginated list of all externally-managed tunnels."
          queryFields={[
            { name: 'limit', type: 'integer', description: 'Max results (default 50, max 200).' },
            { name: 'offset', type: 'integer', description: 'Pagination offset (default 0).' },
          ]}
          responseFields={[
            { name: 'items', type: 'array', description: 'Array of tunnel objects.' },
            { name: 'total', type: 'integer', description: 'Total number of records.' },
            { name: 'limit', type: 'integer', description: 'Applied limit.' },
            { name: 'offset', type: 'integer', description: 'Applied offset.' },
          ]}
          curlExample={`curl "${BASE}/api/v1/tunnels?limit=10&offset=0" \\
  -H "Authorization: Bearer ${KEY}"`}
          responseExample={`{
  "success": true,
  "data": {
    "items": [
      {
        "id": "f8e7d6c5b4a39281",
        "machine_id": "a1b2c3d4e5f6a7b8",
        "status": "active",
        "subdomain": "my-server-a1b2c3",
        "target_ip": "127.0.0.1",
        "target_port": 3000,
        "tunnel_url": "https://my-server-a1b2c3.your-domain.com",
        "created_at": "2026-04-23T12:01:00Z"
      }
    ],
    "total": 1,
    "limit": 10,
    "offset": 0
  }
}`}
        />

        <Endpoint
          method="GET"
          path="/api/v1/tunnels/{id}"
          summary="Get a tunnel"
          responseFields={[
            { name: 'id', type: 'string', description: 'Tunnel record ID.' },
            { name: 'machine_id', type: 'string', description: 'The machine this tunnel belongs to.' },
            { name: 'status', type: 'string', description: '"active" or "failed".' },
            { name: 'subdomain', type: 'string', description: 'The subdomain.' },
            { name: 'target_ip', type: 'string', description: 'Target IP.' },
            { name: 'target_port', type: 'integer', description: 'Target port.' },
            { name: 'tunnel_url', type: 'string', description: 'Public URL (when active).' },
            { name: 'error', type: 'string', description: 'Failure reason (when failed).' },
            { name: 'created_at', type: 'string', description: 'ISO 8601 timestamp.' },
          ]}
          curlExample={`curl ${BASE}/api/v1/tunnels/f8e7d6c5b4a39281 \\
  -H "Authorization: Bearer ${KEY}"`}
          responseExample={`{
  "success": true,
  "data": {
    "id": "f8e7d6c5b4a39281",
    "machine_id": "a1b2c3d4e5f6a7b8",
    "status": "active",
    "subdomain": "my-server-a1b2c3",
    "target_ip": "127.0.0.1",
    "target_port": 3000,
    "tunnel_url": "https://my-server-a1b2c3.your-domain.com",
    "created_at": "2026-04-23T12:01:00Z"
  }
}`}
        />

        <Endpoint
          method="DELETE"
          path="/api/v1/tunnels/{id}"
          summary="Delete a tunnel"
          description="Removes this service tunnel only — the machine remains and can have new tunnels created on it. To tear down everything, use DELETE /api/v1/machines/:id instead."
          curlExample={`curl -X DELETE ${BASE}/api/v1/tunnels/f8e7d6c5b4a39281 \\
  -H "Authorization: Bearer ${KEY}"`}
          responseExample={`HTTP 204 No Content`}
        />
      </div>

      {/* Error format */}
      <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-6 space-y-4">
        <h2 className="font-semibold text-gray-900">Error format</h2>
        <p className="text-sm text-gray-600">All errors return JSON with a consistent shape.</p>
        <CodeBlock code={`{
  "success": false,
  "error": "machine is not connected (status: pending)"
}`} lang="json" />
        <div className="rounded-xl border border-gray-200 overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-gray-50 border-b border-gray-200">
              <tr>
                <th className="text-left py-2 px-4 font-medium text-gray-500 text-xs">Code</th>
                <th className="text-left py-2 px-4 font-medium text-gray-500 text-xs">Meaning</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              <tr><td className="py-2 px-4 font-mono text-xs text-gray-600">400</td><td className="py-2 px-4 text-xs text-gray-600">Bad request — e.g. machine not yet connected</td></tr>
              <tr><td className="py-2 px-4 font-mono text-xs text-gray-600">401</td><td className="py-2 px-4 text-xs text-gray-600">Invalid API key</td></tr>
              <tr><td className="py-2 px-4 font-mono text-xs text-gray-600">404</td><td className="py-2 px-4 text-xs text-gray-600">Machine or tunnel not found</td></tr>
              <tr><td className="py-2 px-4 font-mono text-xs text-gray-600">409</td><td className="py-2 px-4 text-xs text-gray-600">Subdomain already in use</td></tr>
              <tr><td className="py-2 px-4 font-mono text-xs text-gray-600">500</td><td className="py-2 px-4 text-xs text-gray-600">Internal server error</td></tr>
              <tr><td className="py-2 px-4 font-mono text-xs text-gray-600">503</td><td className="py-2 px-4 text-xs text-gray-600">External API not configured — generate a key in Access Control</td></tr>
            </tbody>
          </table>
        </div>
      </div>

      {/* OpenAPI */}
      <div className="bg-white rounded-2xl shadow-sm border border-gray-200 p-6 space-y-3">
        <h2 className="font-semibold text-gray-900">OpenAPI spec</h2>
        <p className="text-sm text-gray-600">
          A machine-readable OpenAPI 3.0 spec is available at{' '}
          <a href="/api/v1/openapi.json" className="text-blue-600 hover:underline font-mono text-xs">/api/v1/openapi.json</a>.
          Import it into Postman, Insomnia, or any OpenAPI-compatible tool.
        </p>
        <CodeBlock code={`curl ${BASE}/api/v1/openapi.json`} />
      </div>
    </div>
  )
}
