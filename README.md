# 🐹 Gopher

Self-hosted alternative to ngrok. One cloud VM, unlimited private services, all accessible via subdomains with **automatic HTTPS**.

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

### Build & Run

```bash
git clone https://github.com/smalex-z/gopher.git
cd gopher
go build -o gopher .
./gopher
# Open http://localhost:8080
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
├── main.go                     # Entry point, embeds frontend
├── go.mod
├── internal/
│   ├── api/
│   │   └── handlers.go         # HTTP API (gorilla/mux)
│   ├── config/
│   │   ├── caddy.go            # Caddyfile generation
│   │   └── rathole.go          # rathole TOML generation
│   ├── db/
│   │   ├── db.go               # SQLite open + migrate
│   │   ├── vps.go              # vps_config table
│   │   ├── machines.go         # machines table
│   │   └── tunnels.go          # tunnels table
│   └── ssh/
│       ├── client.go           # SSH client wrapper
│       ├── vps.go              # VPS setup/deploy via SSH
│       └── client_deploy.go    # rathole client deploy via SSH
├── frontend/
│   └── index.html              # React SPA (CDN, no build step)
└── vps/
    ├── docker-compose.yml      # Caddy + rathole stack
    ├── Caddyfile.template
    └── rathole-server.toml.template
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

- **Backend:** Go, gorilla/mux, mattn/go-sqlite3, golang.org/x/crypto/ssh
- **Frontend:** React 18 (CDN), vanilla CSS, no build step required
- **VPS:** Docker Compose — Caddy 2 + rapiz1/rathole
- **Clients:** rathole binary + systemd on each private machine
