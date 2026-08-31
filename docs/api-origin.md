# Gopher Origin API (gopher-agent)

`gopher-agent` is the control daemon on every managed origin machine. It listens on **`127.0.0.1:4322`** only (`GOPHER_AGENT_PORT` to override) — there is no publicly reachable surface. The edge reaches it through the rathole back-channel: the VPS dials `127.0.0.1:<AgentRemotePort>` locally, which the tunnel forwards to the agent's loopback port on the origin. That hop is Noise-encrypted by rathole, so the agent's own transport is deliberately cleartext.

One port carries two protocols, multiplexed via cmux:

1. **gRPC over h2c** — `agent.v1.AgentControl`, the primary control surface.
2. **Plain HTTP/1** — a tiny surface (`/healthz`, `/self-update`) kept off gRPC so it survives wire-format breaks.

**Source of truth:** `proto/agent/v1/agent.proto`, `cmd/agent/` — agent `0.2.6`, protocol version `1`.

## Authentication

- **Every gRPC RPC** requires the per-machine bearer token in metadata: `authorization: Bearer <GOPHER_AGENT_TOKEN>`, enforced by unary + stream interceptors (constant-time compare). This includes gRPC reflection (enabled for `grpcurl` debugging, but still token-gated).
- **HTTP:** `/healthz` is unauthenticated (liveness/compat anchor); `/self-update` requires the same bearer token in the `Authorization` header.
- The token is loaded from `GOPHER_AGENT_TOKEN` (env) or `/etc/gopher/agent/config.env` (legacy fallback `/etc/gopher-agent/config.env`).
- The edge attaches `x-gopher-edge-url` metadata to every call; the agent persists it (post-auth only) to `config.env` as its dial-home recovery address.

## Versioning

`protocol_version` (currently `1`) is the wire-compat contract the server gates on — **not** the semver string. Additive RPCs don't bump it; older agents return `Unimplemented` for RPCs they predate and the server falls back (noted per-RPC below).

---

## Public endpoint (unauthenticated)

| Method | Path | Description |
|---|---|---|
| GET | `/healthz` | Liveness + compat anchor: `{"ok": true, "version": "0.2.6", "protocol_version": 1}`. Used by the agent's own systemd healthcheck, and lets the server detect an incompatible agent before speaking gRPC. |

"Public" here still means loopback-only — reachable from the origin itself, or from the edge through the tunnel.

## Private HTTP endpoint (bearer token)

| Method | Path | Description |
|---|---|---|
| POST | `/self-update` | Rolls the agent forward to the binary the edge serves. Body `{"base_url": "https://router.example.com", "version": "0.2.6"}`. Persists `base_url` as the dial-home address, no-ops with `200` if already on `version`, otherwise downloads `<base_url>/static/agents/gopher-agent-linux-<arch>` + its `.sha256`, verifies the checksum, and hands installation to a detached `setsid` worker (install + `systemctl restart gopher-agent`) so the restart can't kill the update mid-flight. Returns `202 {"updating": true, "from", "to"}`. Errors: `401` bad token, `400` bad body, `502` download failure, `422` checksum mismatch. Kept on HTTP (not gRPC) so upgrades still work across a future gRPC protocol break. |

## gRPC service: `agent.v1.AgentControl` (bearer token)

All RPCs require the bearer token in metadata. Unary unless noted.

| RPC | Request → Response | Description |
|---|---|---|
| `GetVersion` | `GetVersionRequest` → `VersionInfo` | Build version, `protocol_version`, managed unit name, uptime, arch. The server's compat gate. |
| `GetStatus` | `GetStatusRequest` → `StatusInfo` | One-shot snapshot: agent version/uptime/restarts served, `RatholeStatus` (systemd active/state/substate), `SystemStatus` (loadavg, mem, disk, hostname, kernel), agent clock (`now_unix`). |
| `WatchStatus` | `WatchStatusRequest{heartbeat_seconds}` → **stream** `StatusInfo` | Push-based health: one snapshot immediately, then on change and on a periodic heartbeat (requested interval is clamped; 0 = agent default). **The stream dropping is itself the death signal** — this is how the edge detects a gone origin without polling. |
| `RestartRathole` | → `RestartRatholeResponse{restarted, output}` | Runs `systemctl start rathole-client` — *start, never restart*: start resurrects a stopped/failed unit without dropping the live tunnels a restart would kill. |
| `GetRatholeConfig` | → `RatholeConfig{toml}` | Returns the current `client.toml`. |
| `PutRatholeConfig` | `RatholeConfig{toml}` → `PutRatholeConfigResponse{written, bytes}` | Writes a new `client.toml` in place (1 MiB cap). Pre-flight free-disk check prevents the O_TRUNC-then-ENOSPC zero-byte corruption mode; rathole's inotify watcher reloads without a restart. |
| `Diagnostics` | → `DiagnosticsResponse{checks[]}` | Structured pass/fail checks (`{name, pass, detail}`) on the origin. |
| `Uninstall` | → `UninstallResponse{queued, script, log}` | Kicks off a detached worker running the on-disk `gopher-uninstall` script; returns immediately. |
| `GetNetworkInfo` | → `NetworkInfo{wan_ip, lan_ip}` | On-demand WAN/LAN discovery (kept out of the status snapshot because the WAN lookup is an outbound call). *Agent ≥ 0.2.2; older agents → `Unimplemented`, server falls back to SSH.* |
| `SetManagedKey` | `{username, public_key}` → empty | Makes `public_key` the single `gopher-managed`-tagged entry in that user's `authorized_keys`, removing prior managed keys; operator keys untouched. Enables key rotation without the server holding an SSH private key. *Agent ≥ 0.2.2; older → `Unimplemented`.* |
| `CheckPorts` | `{ports: [{port, proto}]}` → `{ports: [{port, proto, listening}]}` | Reads `/proc/net` to report whether local service ports are bound — the definitive idle-vs-serving signal (impossible to probe from the edge for UDP, only heuristic for TCP). *Agent ≥ 0.2.3; older → `Unimplemented`, server falls back to the edge probe.* |

## Outbound calls the agent makes (for completeness)

The agent is not purely passive — its self-healing paths call the **edge's public API**:

- **Dial-home config recovery** (agent ≥ 0.2.6): when `client.toml` is missing or unloadable beyond local repair, the agent fetches a fresh one from `POST <edge>/api/agent/recover-config` (bearer = agent token, TLS verified) — necessary because every inbound repair channel rides the tunnel that file is the credentials for. The edge URL comes from `GOPHER_EDGE_URL` in `config.env`, refreshed from the `x-gopher-edge-url` metadata and self-update's `base_url`.
- **Self-update downloads** from `<edge>/static/agents/…` (+ `.sha256`), as described above.
- A local **rathole watchdog** loop (no API) restarts a dead or wedged `rathole-client` independently of the control surface.
