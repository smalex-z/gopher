# 🐹 Gopher

Self-hosted reverse tunnel gateway. Run Gopher on your public VPS — it manages Caddy (HTTPS reverse proxy) and rathole (tunnel server) locally, then bootstraps private machines to punch outbound tunnels back. Any service on any machine becomes reachable at `service.yourdomain.com` with automatic TLS.

## Architecture

```
Internet
   │
   ▼
┌─────────────────────────────────────────────────┐
│  Public VPS  — Gopher runs here                  │
│                                                   │
│  ┌─────────────────┐   ┌─────────────────────┐   │
│  │  Caddy (80/443) │   │  rathole server     │   │
│  │  auto-HTTPS     │──▶│  (port 2333)        │   │
│  │  subdomain→port │   │  tunnels listening  │   │
│  └─────────────────┘   └────────┬────────────┘   │
└────────────────────────────────-│────────────────┘
                                  │  outbound TCP tunnel
                   ───────────────┘
                  │
         ┌────────┴──────────────────────────────────┐
         │  Private network                          │
         │                                           │
         │  ┌──────────────┐  ┌──────────────────┐  │
         │  │  Immich VM   │  │  Bitwarden VM    │  │
         │  │  rathole cli │  │  rathole cli     │  │
         │  │  :2283       │  │  :80             │  │
         │  └──────────────┘  └──────────────────┘  │
         └───────────────────────────────────────────┘
```

**Flow:** `photos.example.com` → Caddy → rathole server → tunnel → rathole client (Immich VM) → `localhost:2283`

## Quick Start

### Prerequisites
- A publicly accessible VPS (Ubuntu/Debian recommended) — Gopher runs here
- Go 1.21+ and Node.js 18+ to build from source

### Build & Run

```bash
git clone https://github.com/smalex-z/gopher.git
cd gopher
./scripts/build.sh        # builds frontend then compiles Go binary
sudo ./gopher             # needs root (or passwordless sudo) to manage Caddy/rathole/systemd
# Open http://localhost:8080
```

The build script:
1. Runs `npm ci && npm run build` inside `frontend/`, producing a compiled React app in `cmd/server/frontend/dist/`
2. Compiles the Go binary with the frontend embedded via `//go:embed` — a single self-contained binary

To rebuild only the Go binary after a backend-only change:
```bash
go build -o gopher ./cmd/server/...
```

### Workflow

1. **Server tab** — run the setup wizard to install Caddy and rathole-server locally and configure your domain.
2. **Machines tab** — click **Bootstrap New Machine**, run the generated one-liner on any remote machine. It installs rathole and establishes a persistent reverse tunnel back to this VPS.
3. **Tunnels tab** — map subdomains to services: `subdomain → machine:local_port`.
4. Done — `https://subdomain.yourdomain.com` is live with automatic TLS.

## Project Structure

```
gopher/
├── cmd/server/
│   ├── main.go                     # Entry point; embeds frontend/dist
│   └── frontend/dist/              # Compiled React app (git-ignored)
├── frontend/                       # React + TypeScript + Tailwind source
│   └── src/
│       ├── pages/                  # Dashboard, Machines, Tunnels, Server, Status
│       ├── components/             # Shared UI components
│       ├── api/                    # Typed API client wrappers
│       └── types/                  # Shared TypeScript types
├── internal/
│   ├── api/                        # chi router, handlers, middleware
│   ├── config/                     # Caddyfile + rathole TOML generation
│   ├── db/                         # SQLite (GORM) models + migrations
│   ├── service/                    # Business logic
│   └── ssh/                        # SSH/SFTP client + deploy scripts
├── scripts/
│   ├── build.sh                    # Full build (frontend + Go binary)
│   └── dev.sh                      # Dev mode (Vite dev server + Go)
└── vps/                            # Reference config templates
```

## API Reference

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/local/status` | Local service status (Caddy, rathole, domain) |
| POST | `/api/local/install` | Install Caddy + rathole-server locally |
| POST | `/api/bootstrap/token` | Generate one-time machine bootstrap token |
| POST | `/api/bootstrap` | Machine self-registration (called by bootstrap script) |
| GET | `/api/machines` | List machines |
| DELETE | `/api/machines/{id}` | Remove machine |
| POST | `/api/machines/{id}/deploy` | Re-deploy rathole client to machine |
| GET | `/api/tunnels` | List tunnels |
| POST | `/api/tunnels` | Create tunnel |
| PUT | `/api/tunnels/{id}` | Update tunnel |
| DELETE | `/api/tunnels/{id}` | Delete tunnel |

## Tech Stack

- **Backend:** Go, chi router, GORM + glebarez/sqlite, golang.org/x/crypto/ssh, pkg/sftp, gorilla/websocket
- **Frontend:** React 18, TypeScript, Tailwind CSS, Vite — embedded in Go binary via `//go:embed`
- **Tunnel:** Caddy 2 (reverse proxy + HTTPS) + rathole-org/rathole (TCP tunnel)


## Architecture

```
Internet
   │
   ▼
┌─────────────────────────────────────────────────┐
│  Public VPS  (AWS / OCI / Hetzner / etc.)        │
│                                                   │
│  ┌─────────────────┐   ┌─────────────────────┐   │
│  │  Caddy (80/443) │   │  rathole server     │   │
│  │  auto-HTTPS     │──▶│  (port 2333)        │   │
│  │  subdomain→port │   │  tunnels listening  │   │
│  └─────────────────┘   └────────┬────────────┘   │
└────────────────────────────────-│────────────────┘
                                  │  outbound TCP tunnel
                   ───────────────┘
                  │
         ┌────────┴──────────────────────────────────┐
         │  Home / LAN                               │
         │                                           │
         │  ┌──────────────────────────────────┐     │
         │  │  Host Machine                    │     │
         │  │  Gopher backend (Go, port 8080)  │     │
         │  │  React web GUI                   │     │
         │  └──────────────────────────────────┘     │
         │                                           │
         │  ┌──────────────┐  ┌──────────────────┐  │
         │  │  Immich VM   │  │  Bitwarden VM    │  │
         │  │  rathole cli │  │  rathole cli     │  │
         │  │  :2283       │  │  :80             │  │
         │  └──────────────┘  └──────────────────┘  │
         └───────────────────────────────────────────┘
```

**Flow:** `photos.example.com` → Caddy (VPS) → rathole server → tunnel → rathole client (Immich VM) → `localhost:2283`

## Quick Start

### Prerequisites
- A publicly accessible VPS (Ubuntu/Debian recommended)
- Go 1.21+ on your host machine
- Node.js 18+ and npm (for building the frontend)

### Build & Run

```bash
git clone https://github.com/smalex-z/gopher.git
cd gopher
./scripts/build.sh        # builds frontend then compiles Go binary
./gopher
# Open http://localhost:8080
```

The build script:
1. Runs `npm ci && npm run build` inside `frontend/`, producing a compiled React app in `cmd/server/frontend/dist/`
2. Compiles the Go binary with the frontend embedded via `//go:embed` — a single self-contained binary with no external file dependencies

To rebuild only the Go binary after a backend-only change:
```bash
go build -o gopher ./cmd/server/...
```

### Workflow

1. **VPS Setup tab** — enter your VPS host, SSH credentials, and domain. Click **Full VPS Setup** to install Docker, Caddy, and rathole automatically.
2. **Machines tab** — add each private machine (Immich VM, Bitwarden VM, etc.) with its SSH credentials.
3. **Tunnels tab** — create tunnel mappings: subdomain → machine:local_port. A unique remote port and auth token are auto-generated.
4. **Deploy Config** — push updated Caddyfile + rathole config to the VPS without reinstalling.
5. **Machines → Deploy Client** — SSH into each private machine, install rathole, and start the systemd service.
6. Done! `https://photos.example.com` is live with automatic TLS.

## Project Structure

```
gopher/
├── cmd/server/
│   ├── main.go                     # Entry point; embeds frontend/dist
│   └── frontend/dist/              # Compiled React app (git-ignored, produced by build)
├── frontend/                       # React + TypeScript + Tailwind source
│   ├── src/
│   │   ├── pages/                  # Dashboard, Machines, Tunnels, VPS, Status
│   │   ├── components/             # Shared UI components
│   │   ├── api/                    # Typed API client wrappers
│   │   └── types/                  # Shared TypeScript types
│   ├── vite.config.ts              # Builds to cmd/server/frontend/dist
│   └── package.json
├── internal/
│   ├── api/
│   │   ├── router.go               # gorilla/mux route definitions
│   │   ├── handlers/               # HTTP handlers (vps, machines, tunnels, ...)
│   │   └── dto/                    # Request/response DTOs
│   ├── config/
│   │   ├── caddy.go                # Caddyfile generation
│   │   ├── rathole.go              # rathole TOML generation
│   │   └── templates/              # Embedded config templates
│   ├── db/
│   │   ├── db.go                   # SQLite open + migrate
│   │   ├── models.go               # GORM models
│   │   ├── repository.go           # DB queries
│   │   └── migrations/             # SQL migration files
│   ├── service/                    # Business logic (deploy, machine, tunnel, ...)
│   └── ssh/
│       ├── client.go               # SSH/SFTP client wrapper
│       ├── vps_bootstrap.go        # Full VPS setup via SSH
│       ├── vps_deploy.go           # Config deploy + service restart
│       └── client_deploy.go        # rathole client install on machines
├── scripts/
│   ├── build.sh                    # Full build (frontend + Go binary)
│   └── dev.sh                      # Dev mode (Vite dev server + Go with hot reload)
└── vps/
    ├── docker-compose.yml          # Caddy + rathole stack
    └── *.template                  # Reference config templates
```

## API Reference

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/vps` | Get VPS config |
| PUT | `/api/vps` | Save VPS config |
| POST | `/api/vps/setup` | Full VPS setup (install Docker + deploy stack) |
| POST | `/api/vps/deploy` | Push config update + reload services |
| GET | `/api/machines` | List machines |
| POST | `/api/machines` | Add machine |
| PUT | `/api/machines/{id}` | Update machine |
| DELETE | `/api/machines/{id}` | Delete machine |
| POST | `/api/machines/{id}/deploy` | Deploy rathole client to machine |
| GET | `/api/tunnels` | List tunnels |
| POST | `/api/tunnels` | Create tunnel |
| PUT | `/api/tunnels/{id}` | Update tunnel |
| DELETE | `/api/tunnels/{id}` | Delete tunnel |

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_PATH` | `gopher.db` | Path to SQLite database |

## Tech Stack

- **Backend:** Go, gorilla/mux, GORM + mattn/go-sqlite3, golang.org/x/crypto/ssh, pkg/sftp
- **Frontend:** React 18, TypeScript, Tailwind CSS, Vite — compiled and embedded in the Go binary via `//go:embed`
- **VPS:** Docker Compose — Caddy 2 + rapiz1/rathole
- **Clients:** rathole binary + systemd on each private machine
