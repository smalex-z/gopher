# 🐹 Gopher

Public router for private services. Self-hosted reverse tunnel system with automatic HTTPS and subdomain routing. Expose homelab services, research servers, or any private machine via clean URLs - no port forwarding needed.

**Example:**
```
photos.yourdomain.com → Immich on home NAS (192.168.1.50:2283)
lab.yourdomain.com → Jupyter on university server (no public IP)
vault.yourdomain.com → Bitwarden on Raspberry Pi (behind NAT)
```

## How It Works

Run Gopher on a VPS. It manages Caddy (reverse proxy) and rathole (tunnel server). Private machines establish outbound tunnels. Gopher routes incoming traffic by subdomain.
```
Internet
   │
   ▼
┌─────────────────────────────────────────────────┐
│  Public VPS (Gopher runs here)                   │
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
         │  Private network (home/lab/office)        │
         │                                           │
         │  ┌──────────────┐  ┌──────────────────┐  │
         │  │  Immich VM   │  │  Bitwarden VM    │  │
         │  │  rathole cli │  │  rathole cli     │  │
         │  │  :2283       │  │  :80             │  │
         │  └──────────────┘  └──────────────────┘  │
         └───────────────────────────────────────────┘
```

**Traffic flow:** `photos.yourdomain.com` → Caddy (VPS) → rathole tunnel → rathole client → `localhost:2283` (Immich)

**Key insight:** Machines connect *outbound* to VPS (bypasses firewalls). VPS routes incoming requests through established tunnels.

## Quick Start

### Prerequisites

- **VPS:** Any cloud provider (Oracle, AWS, Hetzner) with Ubuntu/Debian
- **Domain:** Point DNS A record to your VPS IP
- **Build tools:** Go 1.21+ and Node.js 18+

### Installation
```bash
# Build
git clone https://github.com/smalex-z/gopher.git
cd gopher
./scripts/build.sh        # Builds frontend + Go binary

# Run
./gopher
# Open http://localhost:8080
```

### Setup Workflow

**1. Configure Server (Server tab)**
- Enter VPS IP/hostname
- Provide SSH credentials (user + private key)
- Enter your domain (e.g., `example.com`)
- Click **Install Server** → Gopher installs Caddy + rathole on VPS

**2. Add Machines (Machines tab)**
- Click **Bootstrap New Machine**
- Copy the one-liner command
- SSH to your private machine and run it
- Machine establishes reverse tunnel to VPS

**3. Create Tunnels (Tunnels tab)**
- Select machine from dropdown
- Enter subdomain (e.g., `photos`)
- Enter local port (e.g., `2283` for Immich)
- Click **Create Tunnel**

**Done!** `https://photos.example.com` is live with automatic TLS.

## Use Cases

**Homelab:**
```
photos.home.com    → Jellyfin media server
vault.home.com     → Bitwarden password manager
files.home.com     → Nextcloud file sync
monitor.home.com   → Grafana dashboards
```

**Research Lab:**
```
jupyter.lab.edu    → Jupyter notebook server
vnc1.lab.edu       → VNC to lab computer 1
vnc2.lab.edu       → VNC to lab computer 2
data.lab.edu       → Dataset browser
```

**Multi-Network:**
```
nas.alexzheng.com       → Home NAS (Maryland)
jupyter.alexzheng.com   → Lab server (UCLA)
media.alexzheng.com     → Friend's Plex (shared server)
```

All managed from one dashboard, all accessible via clean URLs.

## Project Structure
```
gopher/
├── cmd/server/
│   ├── main.go                     # Entry point; embeds frontend
│   └── frontend/dist/              # Compiled React app
├── frontend/                       # React + TypeScript + Tailwind
│   └── src/
│       ├── pages/                  # Dashboard, Machines, Tunnels, Server
│       └── components/             # UI components
├── internal/
│   ├── api/                        # Chi router + handlers
│   ├── config/                     # Caddyfile + rathole TOML generation
│   ├── db/                         # SQLite (GORM) models + migrations
│   ├── service/                    # Business logic
│   └── ssh/                        # SSH client + deploy scripts
└── scripts/
    ├── build.sh                    # Build script
    └── dev.sh                      # Dev mode (hot reload)
```

## Tech Stack

**Backend:**
- Go 1.21+ - Single binary, great for SSH/networking
- Chi router - Lightweight HTTP router
- GORM + glebarez/sqlite - Pure Go SQLite (no CGO)
- golang.org/x/crypto/ssh - SSH for VPS management

**Frontend:**
- React 18 + TypeScript - Component-based UI with type safety
- Vite - Lightning-fast dev server
- Tailwind CSS - Utility-first styling
- Embedded via `//go:embed` - Single binary distribution

**Infrastructure (deployed by Gopher):**
- Caddy 2 - Reverse proxy with automatic HTTPS
- rathole - Lightweight TCP tunnel (Rust-based)

## Development
```bash
# Development mode (hot reload)
./scripts/dev.sh
# Frontend: http://localhost:5173
# Backend: http://localhost:8080

# Production build
./scripts/build.sh
./gopher
```

## VPS Recommendations

**Tested providers:**
- **Oracle Cloud:** Free tier (4 vCPU ARM, 24GB RAM, 4 Gbps) - perfect for Gopher
- **Hetzner:** €3.79/month - reliable and cheap
- **DigitalOcean:** $6/month droplet works well
- **AWS EC2:** t2.micro (free tier eligible)
  - Not recommended because of miserable networking (~0.05 Gbps)

**Minimum specs:** 1 vCPU, 1GB RAM handles ~10 tunnels

**Required ports open:** 22 (SSH), 80 (HTTP), 443 (HTTPS), 2333 (rathole)

## Comparison

|  | Gopher | ngrok | Cloudflare Tunnel | Port Forwarding |
|---|--------|-------|-------------------|-----------------|
| **Cost** | VPS (~$3-5/mo) | $8-20/mo | Free* | None |
| **Custom domain** | ✅ | 💰 Paid tier | ⚠️ CF DNS required | ✅ |
| **Permanent URLs** | ✅ | ❌ Ephemeral | ✅ | ✅ |
| **Self-hosted** | ✅ | ❌ | ❌ | N/A |
| **Vendor lock-in** | ❌ | ✅ | ✅ | ❌ |
| **Protocol support** | HTTP/TCP/UDP | HTTP/TCP | HTTP only** | All |
| **Works behind NAT** | ✅ | ✅ | ✅ | ❌ |
| **Automatic HTTPS** | ✅ | ✅ | ✅ | ❌ |
| **Traffic privacy** | ✅ You control | ❌ ngrok sees all | ❌ CF sees all | ✅ |
| **Custom middleware** | ✅ Full Caddy | ❌ | ⚠️ CF Workers | N/A |

*Cloudflare Tunnel: Free for HTTP/HTTPS; TCP requires Cloudflare Access (paid)  
**Non-HTTP protocols require Cloudflare Access subscription

## Contributing

Issues and PRs welcome! See [issues](https://github.com/smalex-z/gopher/issues).

**Areas needing help:**
- Testing on different VPS providers
- Documentation improvements
- Bug reports and fixes

## License

MIT - see [LICENSE](LICENSE)

## Acknowledgments

Built with [Caddy](https://caddyserver.com/), [rathole](https://github.com/rapiz1/rathole), and inspired by ngrok and Cloudflare Tunnel.
