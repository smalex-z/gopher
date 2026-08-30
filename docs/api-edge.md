# Gopher Edge API (VPS)

The edge API is served by the `gopher` binary on the dashboard port (default `4321`, configurable via `--port`), and is normally reached through Caddy at `https://router.<domain>`. The same binary serves the embedded React dashboard as an SPA on `/`.

**Source of truth:** `internal/api/router.go` + `internal/api/handlers/`, as of `v0.1.0-beta.20.2` (`2a2cbfb`).

## Conventions

- **Response envelope:** all JSON endpoints return `{"success": true, "data": ...}` on success and `{"success": false, "error": "..."}` on failure. Exceptions: `204 No Content` on some deletes, and endpoints that stream files or plain text (noted below).
- **Errors:** `400` validation, `401` unauthenticated, `403` setup-guard, `404` not found, `409` conflict / operation already in progress, `429` rate limited (with `Retry-After`), `500` internal, `502` recovery push failed.
- **Two auth models:**
  - **Operator session** — `gopher_session` HttpOnly cookie (SameSite=Lax, Secure behind TLS, 24 h), set by login. Everything under "Authenticated" below requires it.
  - **Per-machine agent bearer token** — `Authorization: Bearer <token>`, used by origin machines calling home (bootstrap/migrate/recover/self-delete). These endpoints are public but token-authenticated and per-IP rate limited.
- **WebSockets:** cross-origin upgrades are only accepted from the dashboard's own host or the configured domain; requests without an `Origin` header (curl, server-to-server) are allowed. Ping/pong liveness: ping every 30 s, drop after 60 s without a pong.

---

## Public endpoints (no session cookie)

### Liveness & counts

| Method | Path | Description |
|---|---|---|
| GET | `/healthz` | Unauthenticated liveness probe (outside `/api`). `200 {"status":"ok","version":...}` when the process is up and the DB answers `SELECT 1`; `503` otherwise. Leaks nothing operator-relevant. |
| GET | `/api/status` | Counts only: `{"machines": n, "tunnels": n, "vps": bool}`. |

### Authentication

| Method | Path | Description |
|---|---|---|
| GET | `/api/auth/status` | `{"setup": bool, "authenticated": bool, "totp_enabled": bool}`. |
| POST | `/api/auth/setup` | First-run password creation. Body `{"password"}`. `409` if already configured. Logs the session in immediately (sets cookie). |
| POST | `/api/auth/login` | Body `{"password"}`. Sets the session cookie on success. If 2FA is enabled, returns `200` with `{"needs_2fa": true, "pending_token": "..."}` and **no** cookie. Per-IP rate limited (`429` + `Retry-After`). |
| POST | `/api/auth/login/2fa` | Body `{"pending_token", "code"}` (TOTP or backup code). Sets the session cookie. |

### Setup wizard (public by necessity, then locked)

These run before a session can exist. The mutating ones self-disable once their wizard step completes (`403 "setup already complete"` / `"firewall already configured"`).

| Method | Path | Description |
|---|---|---|
| GET | `/api/local/setup-state` | Boolean-only wizard gating: `local_setup_done`, `firewall_configured`, `fail2ban_setup_done`, `ssh_key_configured`. (The rich `/api/local/status` is auth-only — it was a recon payload.) |
| POST | `/api/local/install` | Body `{"domain"}` (required, validated FQDN). Kicks off the async Caddy + rathole install; logs stream to the log WS. `403` once setup is done; `409` if an operation is already running. |
| GET | `/api/local/logs/ws` | WebSocket log stream, **setup-time variant**: rejects with `401` once both setup and firewall are configured. Stream ends with sentinel `\x00DONE` or `\x00ERROR`. |
| GET | `/api/local/check-dns?domain=X&expected_ip=Y` | Structured DNS preflight (wildcard, router, propagation, ip_match, CAA) with per-check results. `expected_ip` optional. 6 s hard ceiling. |
| GET | `/api/local/resolve-ip?host=X` | Resolves a hostname to its first A record (IPs pass through). `{"ip": "..."}`, empty string when unresolvable. |
| GET | `/api/local/detect-ip` | Detects the VPS public IP via external echo services. `{"ip": "..."}`. |
| GET | `/api/local/firewall/detect` | Read-only detection of UFW/firewalld/nftables/iptables state. |
| POST | `/api/local/firewall/configure` | Body `{"mode": "gopher"\|"manual"\|"none"}`. `gopher` mode runs the async takeover (logs to WS). `403` once a mode is set. |

### Machine-facing (agent bearer token, per-IP rate limited)

| Method | Path | Description |
|---|---|---|
| POST | `/api/bootstrap` | Called by `bootstrap.sh` during self-registration. Body `{"token", "name", "username", "no_ssh"}` (token is one-time, claimed atomically). Returns `{"tunnel_port", "rathole_token", "vps_ssh_public_key", "rathole_client_config", "vps_host", "agent_token", "agent_local_port", "agent_remote_port"}` — the agent fields are all-or-nothing install hints. |
| POST | `/api/migrate` | Called by `migrate.sh` during agent install. Body `{"token"}` (single-use migration token; a failed install needs a fresh one). Returns `{"machine_id", "agent_token", "agent_port", "rathole_token", "noise_pubkey"}`. |
| POST | `/api/agent/recover-config` | Agent dial-home recovery. `Authorization: Bearer <agent token>`. Returns the machine's regenerated managed `client.toml` as `text/plain`. Every use is audit-logged at warn severity with the caller IP. |
| POST | `/api/machines/self-delete` | Called by `gopher-uninstall` on the origin before tearing itself down. `Authorization: Bearer <agent token>`. Deletes the machine record; `404` for an unknown token (deliberately indistinguishable from wrong). |

### Static / distribution

| Method | Path | Description |
|---|---|---|
| GET | `/static/bootstrap.sh` | Bootstrap script, templated with the canonical host URL and pinned component versions. |
| GET | `/static/gopher-uninstall.sh` | Client uninstall script (templated with host URL for the self-delete callback). |
| GET | `/static/migrate.sh` | Agent install/migration script (token as `$1`, calls back to `/api/migrate`). |
| GET | `/static/agents/gopher-agent-linux-<arch>` | Embedded agent binaries (`amd64`, `arm64`, `armv7`). Append `.sha256` for an on-the-fly checksum sidecar. Used by agent self-update. |
| GET | `/static/rathole/<uname>` | Bundled rathole binary for bootstrapping origins (embedded builds only). `.sha256` variant likewise. |

### Tunnel gate endpoints (on tunnel hostnames, not the dashboard)

Requests to a gated tunnel's own hostname (`<subdomain>.<domain>`) are intercepted by the edge's bot-protection/password middleware before reaching the origin:

| Path | Description |
|---|---|
| `/bot-verify` | Proof-of-work challenge post-back when bot protection is enabled. Success sets a purpose-scoped HMAC cookie (invalidated on process restart). |
| `/auth-verify` | Password-gate post-back when tunnel password auth is enabled. Per-IP brute-force throttle (8 fails / 5 min). |

API clients (JSON `Accept`) get a `403`/`401` JSON error instead of the HTML challenge; allowlisted source IPs bypass the gates.

---

## Authenticated endpoints (session cookie required)

### Session & 2FA

| Method | Path | Description |
|---|---|---|
| POST | `/api/auth/logout` | Invalidates the session, clears the cookie. |
| GET | `/api/auth/2fa/status` | `{"enabled", "devices", "backup_codes_remaining"}`. |
| POST | `/api/auth/2fa/enroll` | Generates a pending secret: `{"secret", "qr_data_url"}`. Nothing committed until confirm. |
| POST | `/api/auth/2fa/confirm` | Body `{"code", "name"}`. Enrolls the device; returns `backup_codes` **only on first enrollment**. |
| POST | `/api/auth/2fa/disable` | Body `{"code"}` (any device or a backup code). Removes all devices. |
| DELETE | `/api/auth/2fa/devices/{id}` | Body `{"code"}` — must verify via a *different* device or backup code. |
| POST | `/api/auth/2fa/backup-codes/regenerate` | Body `{"code"}`. Returns fresh codes (shown once). |

### Bootstrap

| Method | Path | Description |
|---|---|---|
| POST | `/api/bootstrap/token` | Generates a one-time bootstrap token. Optional body `{"tunnel_port", "ssh_key_id", "public_ssh", "ssh_enabled"}` (defaults: SSH enabled + public). Returns `{"token", "bootstrap_command", "expires_at"}` — the copy-paste curl-bash one-liner. |

### Local host / edge management (`/api/local`)

| Method | Path | Description |
|---|---|---|
| GET | `/api/local/status` | Full host status: caddy/rathole installed+active, domain, server host, setup flags, host IPs, OS user, install permission, etc. |
| GET | `/api/local/activity` | Latest 50 machine + tunnel lifecycle/health events (auth events live under `/api/security/logs`). |
| POST | `/api/local/reconcile` | Full rebuild of `rathole server.toml` **and** every tunnel's Caddy block from DB. Recovery handle for out-of-band drift. |
| POST | `/api/local/setup-fail2ban` | Async fail2ban install (logs to WS). `409` if an op is running. |
| POST | `/api/local/skip-fail2ban` | Records the skip so the wizard advances; installable later from Security. |
| PUT | `/api/local/server-ports` | Body `{"dashboard_private": bool}` — toggles dashboard port exposure through the managed firewall chain. |
| PUT | `/api/local/bind-ip` | Body `{"bind_ip"}` (empty to clear). Reconciles rathole + Caddy immediately; the dashboard HTTP listener needs a restart. |
| POST | `/api/local/dismiss-custom-services-warning` | Clears the "custom rathole services need manual noise pubkey" banner. |

### Firewall (`/api/local/firewall`)

| Method | Path | Description |
|---|---|---|
| GET | `/api/local/firewall/overview` | Managed rule entries (tunnel ports, dashboard port, custom rules). |
| POST | `/api/local/firewall/rules` | Body `{"description", "raw": bool, "raw_spec"}` **or** `{"description", "protocol"(tcp), "port_range", "source"(0.0.0.0/0), "action"(ACCEPT)}`. Applied to the `GOPHER_CUSTOM` chain. |
| DELETE | `/api/local/firewall/rules/{id}` | Removes a custom rule. |
| GET | `/api/local/firewall/live` | Live iptables chain dump. |
| POST | `/api/local/firewall/reload` | Re-applies the full managed ruleset. |

### SSH keys (`/api/local/ssh-keys`)

| Method | Path | Description |
|---|---|---|
| GET | `/api/local/ssh-keys/` | All key records + per-key machine count. Private keys never included. |
| POST | `/api/local/ssh-keys/generate` | Body `{"name", "set_default"}`. Generates a pair. |
| POST | `/api/local/ssh-keys/upload` | Body `{"name", "private_key", "public_key", "set_default"}` (`public_key` required). |
| DELETE | `/api/local/ssh-keys/{id}` | Delete a key record. |
| PUT | `/api/local/ssh-keys/{id}/default` | Make this the default key. |
| GET | `/api/local/ssh-keys/challenge-info` | Which credential the step-up modal should ask for: `{"requires": "totp"\|"password"}`. |
| POST | `/api/local/ssh-keys/{id}/download` | **Step-up gated.** Body `{"totp_code"}` or `{"password"}`. Streams the private key as `application/octet-stream`. Audit-logged. |
| POST | `/api/local/ssh-keys/{id}/delete-private` | **Step-up gated** (irreversible). Clears the stored private half, keeps the public key. Audit-logged. |
| POST | `/api/local/ssh-keys/{id}/private` | Body `{"private_key"}`. Restores the private half; verified against the stored public key server-side (so no step-up needed). |

The step-up challenge exists so a stolen session cookie alone can't exfiltrate or destroy the keys the edge uses to SSH into origins. It shares the login rate limiter.

### VPS

| Method | Path | Description |
|---|---|---|
| GET | `/api/vps/` | The VPS config record. `404` if not configured. |

### Machines (`/api/machines`)

| Method | Path | Description |
|---|---|---|
| GET | `/api/machines/` | List machines. |
| POST | `/api/machines/` | Body `{"name", "host", "port", "username"}`. `201`. |
| GET | `/api/machines/{id}` | Fetch one. |
| PUT | `/api/machines/{id}` | Same fields as create. |
| DELETE | `/api/machines/{id}` | `204` when fully clean; `200` with a DeleteResult body only when server-side delete succeeded but client-side cleanup failed. |
| POST | `/api/machines/{id}/deploy` | Async deploy (logs to WS). `409` if an op is running. |
| GET | `/api/machines/{id}/status` | Machine status snapshot. |
| GET | `/api/machines/{id}/network-info` | Refreshes WAN/LAN IPs (agent RPC first, SSH fallback). |
| GET | `/api/machines/{id}/rathole-config` | Canonical `client.toml` as a `text/plain` download. `?format=script` wraps it in a one-shot recovery shell script (pipe through bash on the origin). |
| POST | `/api/machines/{id}/recover` | Server-side repair: agent push first, SSH-via-tunnel fallback. `502` when both fail (tunnel fully down → use the manual script above). |
| PUT | `/api/machines/{id}/ssh-key` | Body `{"ssh_key_id"}`. Pushes the new key to the machine and updates the record. |
| GET | `/api/machines/agent/pending` | Machines without the agent installed (dashboard banner). |
| POST | `/api/machines/{id}/install-agent` | Returns the operator-paste curl-bash install command (the server can't install remotely — no root over SSH). |
| GET | `/api/machines/{id}/health` | 24 h HealthSummary: uptime %, latest, recent rows (sparkline). |
| POST | `/api/machines/{id}/health/check` | "Test now" — one-off check, returns `{"check", "now"}`. |
| GET | `/api/machines/{id}/agent-status` | Live system metrics proxied from the agent over the back-channel (no DB writes). `400` if the agent isn't installed. |

### Tunnels (`/api/tunnels`)

| Method | Path | Description |
|---|---|---|
| GET | `/api/tunnels/` | All tunnels, **including virtual machine-SSH tunnels** synthesized from machine records (IDs `machine-{id}-ssh`). |
| GET | `/api/tunnels/next-port` | `{"port": n}` — next free rathole port. |
| GET | `/api/tunnels/port-check?port=N` | `{"available": bool, "reason": "..."}` — DB + live OS probe. |
| POST | `/api/tunnels/` | Create. Body: `machine_id`, `name`, `subdomain`, `local_port`, `rathole_port` (0 = auto), `transport` (`tcp`/`udp`), `no_tls`, `private`, bot protection (`bot_protection_enabled`, `bot_protection_ttl`, `bot_protection_allow_ip`), password gate (`auth_enabled`, `auth_password` — plaintext on the wire, bcrypt at rest, `auth_ttl`, `auth_allow_ip`), `tls_skip_verify`. `201` / `400` / `409`. |
| GET | `/api/tunnels/{id}` | Fetch one. |
| PUT | `/api/tunnels/{id}` | Update (same fields minus port/transport; empty `auth_password` keeps the existing one). Virtual `machine-*-ssh` IDs route to the SSH privacy toggle. |
| DELETE | `/api/tunnels/{id}` | `204`. |
| POST | `/api/tunnels/{id}/test` | Live probe. `200 {"status": "active"\|"connected"}`; `400` with a reason for `idle` (tunnel up, nothing listening on the origin port) or offline. |
| GET | `/api/tunnels/{id}/health` | 24 h health summary. Managed IDs (`machine-{id}-ssh` / `-agent`) read the machine's health series. |

### Events & live streams

| Method | Path | Description |
|---|---|---|
| GET | `/api/events` | Unified event log. Query params (all optional): `source` (csv of `auth,machine,tunnel,health,firewall,system`), `severity`, `min_severity`, `resource_id`, `q` (substring), `since`/`until`/`before` (RFC3339; `before` is the pagination cursor), `limit` (default 100, max 500). Returns `{"events": [...], "total": n}`. |
| GET | `/api/status/ws` | WebSocket push of machine/tunnel status transitions (badge updates without polling). |
| GET | `/api/logs/ws` | WebSocket log stream for long-running ops (install, takeover, deploy). Sentinels `\x00DONE` / `\x00ERROR` close a stream. |

### Security (`/api/security`)

| Method | Path | Description |
|---|---|---|
| GET | `/api/security/stale-tokens` | Recorded attempts using stale/unknown machine tokens. |
| GET | `/api/security/logs` | Auth audit log. |
| GET | `/api/security/fail2ban` | Jail status. |
| POST | `/api/security/fail2ban/unban` | Body `{"jail", "ip"}`. |
| GET | `/api/security/fail2ban/config` | Current config. |
| PUT | `/api/security/fail2ban/config` | Body `{"max_retry" ≥1, "find_time" ≥10, "ban_time" ≥10, "ignore_ips"}`. |
| POST | `/api/security/fail2ban/whitelist` | Body `{"ip"}`. |
| DELETE | `/api/security/fail2ban/whitelist/{ip}` | Remove from whitelist. |
| GET | `/api/security/backup/download` | SQLite snapshot (`VACUUM INTO`) streamed as a timestamped file download. |
| POST | `/api/security/backup/restore` | Multipart upload field `file`. Validates, swaps onto the live DB, schedules a service restart. |

### Debug & updates

| Method | Path | Description |
|---|---|---|
| GET | `/api/debug/caddyfile` | The Caddyfile that would be deployed, rendered from DB (`text/plain`). |
| GET | `/api/debug/rathole-server` | The rathole `server.toml` that would be deployed (`text/plain`). |
| GET | `/api/update/check` | Update availability for the configured channel. |
| POST | `/api/update/apply` | Downloads + swaps the binary, schedules restart. Returns `202`. |
| POST | `/api/update/channel` | Body `{"channel": "stable"\|"beta"\|"alpha"}`. |
