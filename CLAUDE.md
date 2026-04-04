# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

### After any code change — always run:
```bash
./scripts/reinstall.sh
```
This builds the full binary (frontend + Go), stops the running systemd service, swaps the binary, and restarts it. Do not skip this step.

### Build only (no deploy):
```bash
./scripts/build.sh          # full build: npm ci + tsc + vite + go build
```

### Development mode (local, no systemd):
```bash
./scripts/dev.sh            # Go backend on :4321, Vite dev server on :5173
```

### Frontend only:
```bash
cd frontend && npm run dev       # Vite dev server
cd frontend && npm run build     # production build (output: frontend/dist/)
cd frontend && npm run lint      # ESLint (0 warnings policy)
cd frontend && npx tsc --noEmit  # type check only
```

### Go tests:
```bash
go test ./...                                      # all tests
go test ./internal/service/... -run TestXxx        # single test
go test ./internal/api/handlers/...                # handler tests
```

### Shell integration tests (require running service):
```bash
./tests/critical-path.sh
./tests/config-validation.sh
./tests/idempotency.sh
./tests/state-reconciliation.sh
```

## Architecture

### Overview
Gopher is a self-hosted reverse tunnel gateway. It runs on a public VPS and manages:
- **rathole** (tunnel server) — private machines connect outbound to the VPS; rathole binds ports locally
- **Caddy** (reverse proxy) — routes `subdomain.domain.com` → `localhost:<rathole_port>`

The Go binary embeds the built React frontend (`frontend/dist`) and serves it as a SPA.

### Data flow
```
Internet → Caddy (80/443) → localhost:<rathole_port> → rathole tunnel → client machine
                                    ↑
         Gopher manages rathole server config + Caddy conf.d files
```

### Backend structure

**`cmd/server/`** — entrypoint. Parses `install`/`uninstall` subcommands; otherwise starts the HTTP server. Embeds `frontend/dist` at compile time.

**`internal/db/`**  
- `models.go` — all GORM models: `Machine`, `Tunnel`, `AppSettings`, `SSHKey`, `BootstrapToken`, `FirewallRule`
- `repository.go` — all DB queries (no ORM magic outside this file)
- `db.go` — `Initialize()`: opens SQLite, runs `AutoMigrate`, then numbered SQL migrations in `migrations/`
- Schema changes: add a field to the model struct (AutoMigrate handles the column), and if data migration is needed add a numbered `.sql` file in `migrations/`

**`internal/service/`**  
Services own all business logic. The `LocalSetupService` is the largest and most critical:
- `local_install.go` — Caddy + rathole install flow (streamed to WebSocket log hub)
- `local_rathole.go` — `ReconcileServerConfig()` (full rebuild of `/etc/rathole/server.toml` from DB), `AddServiceTunnel()` (server.toml + Caddy + SSH into client), `RemoveServiceTunnelClient/Caddy()`
- `local_caddyfile.go` — Caddy config generation; managed entries go in `conf.d/*.caddy`, user entries are preserved between `BEGIN/END CUSTOM CONFIGURATION` markers
- `firewall_manage.go` — iptables management via `GOPHER_TUNNELS` chain; `ApplyTunnelPort()`, `ApplyDashboardPort()`, `RevokeTunnelPort()`, full takeover sequence
- `firewall_detect.go` — read-only detection of UFW/firewalld/nftables/iptables
- `firewall_custom.go` — user-defined rules applied to `GOPHER_CUSTOM` chain
- `deploy.go` — `LogHub` (pub/sub broadcast for WebSocket streaming of long-running operations)
- `monitor.go` — polls rathole TCP port every 30s to update `Machine.Status`

The `localOps` interface (`local_ops.go`) is what `TunnelService` and `MachineService` call into `LocalSetupService` for — it keeps service coupling explicit and testable.

**`internal/config/`**  
- `rathole.go` — `GenerateRatholeServerConfig()`: builds complete `server.toml` from DB state. Machine SSH tunnels bind `127.0.0.1` (private) or `0.0.0.0` (public) based on `Machine.PublicSSH`; service tunnels bind based on `Tunnel.Private`.

**`internal/api/`**  
- `router.go` — chi router; public routes (setup wizard, bootstrap WebSocket) are outside the auth middleware group
- `handlers/` — thin handlers that decode JSON, call service methods, return `response.Success/BadRequest/InternalError`
- `dto/` — request structs (separate from DB models)

**`internal/ssh/`** — SSH client wrapper used to push config files to client machines via SFTP and execute commands over the tunnel.

### Frontend structure

**`src/api/`** — axios client wrappers per resource (`tunnels.ts`, `machines.ts`, `vps.ts`, `local.ts`). All API calls go through `src/api/client.ts`.

**`src/pages/`** — one file per route. Data fetching via `@tanstack/react-query`. Mutations invalidate query cache on success.

**`src/types/index.ts`** — shared TypeScript interfaces mirroring Go DB models.

**`src/lib/auth.tsx`** — `AuthProvider` fetches `/api/auth/status` + `/api/local/status` on mount; gates the entire app through `AppShell` in `App.tsx`. Gates: `!isSetup` → step 1, `!localSetupDone` → step 2, `!firewallConfigured` → step 3.

### Key design patterns

**Rathole config is always fully regenerated** — `ReconcileServerConfig()` never diffs or patches; it rebuilds `/etc/rathole/server.toml` entirely from DB, validates it, then sends `SIGHUP` (falls back to `systemctl restart`). User's custom services are preserved in a `BEGIN/END CUSTOM CONFIGURATION` marker block at the bottom.

**Machine SSH tunnels are virtual** — they don't exist in the `tunnels` table. `TunnelService.List()` synthesizes them from `Machine` records (`TunnelPort > 0`). Their IDs are `machine-{id}-ssh`. `TunnelService.Update()` detects this ID pattern and routes to `updateMachineSSHPrivacy()` instead of the normal update path.

**Firewall rules use a dedicated chain** — `GOPHER_TUNNELS` chain is jumped from `INPUT`. Tunnel ports are added/removed from this chain dynamically as tunnels are created/deleted. Only active when `AppSettings.FirewallMode == "gopher"`. The dashboard port (default 4321, configurable via `--port`) is also managed through this chain (controlled by `AppSettings.DashboardPrivate`). Port 22/80/443/2333 are hardcoded in `INPUT` and never touched dynamically.

**Long-running ops stream logs** — install, firewall takeover, and deploy operations run in goroutines and broadcast to `LogHub`. The frontend connects via WebSocket (`/api/local/logs/ws`) and displays a live log modal. Sentinel `\x00DONE` or `\x00ERROR` closes the stream.

**Bootstrap flow** — `POST /bootstrap/token` generates a one-time token. The token encodes tunnel port, SSH key ID, and `PublicSSH` preference. The bootstrap script (shell) curls the Gopher server, installs rathole-client as a systemd service, and registers the machine. On registration, Gopher SSHes into the machine through the new tunnel to push the server's public key to `authorized_keys`.
