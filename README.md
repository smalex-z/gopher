# 🐹 Gopher

Public router for private services. Self-hosted reverse tunnel system with automatic HTTPS and subdomain routing. Expose homelab services, research servers, or any private machine via clean URLs — no port forwarding needed.

**Example:**
```
photos.yourdomain.com → Immich on home NAS (192.168.1.50:2283)
lab.yourdomain.com    → Jupyter on university server (no public IP)
vault.yourdomain.com  → Bitwarden on Raspberry Pi (behind NAT)
```

---

## Built on Caddy and rathole

Gopher would not exist without two excellent open-source projects:

**[Caddy](https://caddyserver.com/)** handles all HTTPS termination and subdomain routing on the VPS. It automatically provisions and renews TLS certificates via Let's Encrypt, with zero configuration required from the user. Gopher generates and manages the Caddyfile for you, but Caddy is doing the actual reverse proxying.

**[rathole](https://github.com/rathole-org/rathole)** is the tunnel engine. It's a Rust-based, extremely lightweight TCP/UDP tunnel that machines use to punch through NAT and firewalls by maintaining a persistent outbound connection to the VPS. Gopher manages the rathole server config and deploys rathole client configs to each machine — but rathole is what makes the tunnels actually work.

If you find Gopher useful, please consider starring those projects too.

---

## How It Works

Run Gopher on a VPS. It manages Caddy (reverse proxy) and rathole (tunnel server). Private machines establish outbound tunnels. Gopher routes incoming traffic by subdomain.

```
Internet
   │
   ▼
┌─────────────────────────────────────────────────┐
│  Public VPS (Gopher runs here)                  │
│                                                 │
│  ┌─────────────────┐   ┌─────────────────────┐  │
│  │  Caddy (80/443) │   │  rathole server     │  │
│  │  auto-HTTPS     │──▶│  (port 2333)        │  │
│  │  subdomain→port │   │  tunnels listening  │  │
│  └─────────────────┘   └────────┬────────────┘  │
└────────────────────────────────-│───────────────┘
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

**Key insight:** Machines connect *outbound* to the VPS, bypassing NAT and firewalls. The VPS routes incoming traffic back through those established tunnels.

---

## Installation

Gopher ships as a single self-contained binary for Linux. No runtime dependencies — Caddy and rathole are downloaded automatically during setup.

### Requirements

- Linux VPS (Ubuntu 22.04+ or RHEL 8+ recommended), x86_64 or arm64
- A domain with a wildcard DNS A record pointing at your VPS: `*.yourdomain.com → <VPS IP>`
- Ports 22, 80, 443, and 2333 reachable on the VPS

### Download

**Latest stable release:**
```bash
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
TAG=$(curl -fsSL https://api.github.com/repos/smalex-z/gopher/releases/latest | grep '"tag_name"' | cut -d'"' -f4)
curl -fsSL "https://github.com/smalex-z/gopher/releases/download/${TAG}/gopher-linux-${ARCH}" -o gopher && chmod +x gopher
```

**Latest pre-release** (recommended for now — no stable release yet):
```bash
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
TAG=$(curl -fsSL https://api.github.com/repos/smalex-z/gopher/releases | grep -m1 '"tag_name"' | cut -d'"' -f4)
curl -fsSL "https://github.com/smalex-z/gopher/releases/download/${TAG}/gopher-linux-${ARCH}" -o gopher && chmod +x gopher
```

You can verify your download against the checksums file on the [releases page](https://github.com/smalex-z/gopher/releases).

### Run

```bash
# Start the web UI (listens on :4321)
./gopher

# Install as a systemd service (runs on boot, survives reboots)
sudo ./gopher install

# Service management
sudo systemctl status gopher
sudo systemctl restart gopher

# Uninstall
sudo ./gopher uninstall
```

On first start, visit `http://<your-vps-ip>:4321` and complete the setup wizard:

1. **Password** — set your admin password
2. **Local services** — install Caddy and rathole on this machine (or skip for rathole-only)
3. **Firewall** — choose how network rules are managed (Gopher-managed is recommended)
4. **SSH key** — generate or upload an SSH key pair used to access bootstrapped machines

---

## Setup Workflow

After the wizard, you're at the main dashboard:

**1. Add Machines (Machines tab)**
- Click **Bootstrap New Machine**
- Copy the one-liner and run it on any machine you want to expose
- It installs rathole client and establishes a reverse tunnel back to your VPS

**2. Create Tunnels (Tunnels tab)**
- Select a machine, enter a subdomain (e.g. `photos`) and the local port (e.g. `2283`)
- Click **Create Tunnel**
- `https://photos.yourdomain.com` is live with automatic TLS in seconds

Gopher supports **HTTP, raw TCP, and UDP** tunnels.

---

## Use Cases

**Homelab:**
```
photos.home.com   → Jellyfin media server
vault.home.com    → Bitwarden password manager
files.home.com    → Nextcloud file sync
monitor.home.com  → Grafana dashboards
```

**Research Lab:**
```
jupyter.lab.edu   → Jupyter notebook server
vnc1.lab.edu      → VNC to lab workstation
data.lab.edu      → Dataset browser
```

**Multi-site:**
```
nas.example.com      → Home NAS (Maryland)
jupyter.example.com  → Lab server (UCLA)
media.example.com    → Friend's shared Plex
```

---

## Comparison

|  | Gopher | ngrok | Cloudflare Tunnel | Port Forwarding |
|---|--------|-------|-------------------|-----------------|
| **Cost** | VPS (~$3–5/mo) | $8–20/mo | Free* | None |
| **Custom domain** | ✅ | 💰 Paid tier | ⚠️ CF DNS required | ✅ |
| **Permanent URLs** | ✅ | ❌ Ephemeral | ✅ | ✅ |
| **Self-hosted** | ✅ | ❌ | ❌ | N/A |
| **Vendor lock-in** | ❌ | ✅ | ✅ | ❌ |
| **Protocol support** | HTTP / TCP / UDP | HTTP / TCP | HTTP only** | All |
| **Works behind NAT** | ✅ | ✅ | ✅ | ❌ |
| **Automatic HTTPS** | ✅ | ✅ | ✅ | ❌ |
| **Traffic privacy** | ✅ You control | ❌ ngrok sees all | ❌ CF sees all | ✅ |

\* Free with limitations  
\*\* Non-HTTP protocols require Cloudflare Access (paid)

---

## Firewall Setup

### OS-level firewall

Gopher can manage iptables rules automatically. During the setup wizard, choose **Gopher-managed** firewall mode and Gopher will:

- Create a dedicated `GOPHER_TUNNELS` iptables chain
- Keep ports 22, 80, 443, and 2333 permanently open
- Open the dashboard port (default 4321) — or restrict it to localhost if Caddy is configured
- Automatically open/close tunnel ports as you add or remove tunnels

If you prefer to manage firewall rules yourself, choose **Manual** mode and open the required ports yourself. Each tunnel you create gets its own port (starting around 20000) — Gopher shows the assigned port when you create one.

### Cloud-level firewall / security groups

Most cloud providers have a firewall that sits in front of the VM and blocks traffic before it ever reaches the OS. You need to open ports there as well — Gopher cannot manage these.

**Oracle Cloud (OCI)** — the most common gotcha, since the default security list blocks almost everything:

1. Go to **Networking → Virtual Cloud Networks → your VCN → Security Lists**
2. Edit the **Default Security List** (or whichever is attached to your subnet)
3. Add ingress rules for TCP ports 80, 443, and 2333 from source `0.0.0.0/0`
4. Port 22 is usually already open

Alternatively, use a **Network Security Group** attached to your instance instead of the security list.

**AWS EC2:**

1. Go to **EC2 → Instances → your instance → Security**
2. Click the security group link
3. Edit **Inbound rules** → add TCP 80, 443, 2333 from `0.0.0.0/0`

**Hetzner:**

1. Go to **Firewall** in the Cloud Console
2. Add inbound rules for TCP 80, 443, 2333
3. Apply the firewall to your server

**DigitalOcean:**

1. Go to **Networking → Firewalls**
2. Create or edit a firewall, add TCP 80, 443, 2333 inbound
3. Apply it to your droplet

---

## VPS Recommendations

**Tested providers:**
- **Oracle Cloud Free Tier** — 4 vCPU ARM, 24 GB RAM, 4 Gbps — best free option for Gopher
- **Hetzner** — ~€4/month — reliable and cheap
- **DigitalOcean / Vultr** — $6/month droplet works well
- **AWS EC2** — t2.micro is free tier eligible but networking is slow (~0.05 Gbps)

**Minimum:** 1 vCPU, 512 MB RAM handles 10+ tunnels comfortably.

---

## Project Structure

```
gopher/
├── cmd/server/
│   ├── main.go                     # Entry point; embeds frontend
│   └── frontend/dist/              # Compiled React app (embedded at build time)
├── frontend/                       # React + TypeScript + Tailwind
│   └── src/
│       ├── pages/                  # Dashboard, Machines, Tunnels, Server, Setup
│       └── components/             # UI components
├── internal/
│   ├── api/                        # Chi router + HTTP handlers
│   ├── config/                     # Caddyfile + rathole TOML generation
│   ├── db/                         # SQLite (GORM) models + migrations
│   ├── service/                    # Business logic (install, tunnels, firewall, …)
│   └── ssh/                        # SSH client + VPS/machine deploy scripts
└── scripts/
    ├── build.sh                    # Build frontend then Go binary
    └── dev.sh                      # Dev mode with hot reload
```

## Tech Stack

**Backend:** Go 1.21+, Chi router, GORM + glebarez/sqlite (pure-Go, no CGO), golang.org/x/crypto/ssh

**Frontend:** React 18 + TypeScript, Vite, Tailwind CSS — embedded in the binary via `//go:embed`

**Infrastructure (managed by Gopher):** [Caddy 2](https://caddyserver.com/) for HTTPS + routing, [rathole](https://github.com/rathole-org/rathole) for tunneling

---

## Development

```bash
# Dev mode (hot reload on both frontend and backend)
./scripts/dev.sh
# Frontend: http://localhost:5173
# Backend:  http://localhost:4321

# Production build
./scripts/build.sh
./gopher
```

---

## Contributing

Issues and PRs welcome. See [open issues](https://github.com/smalex-z/gopher/issues).

Areas that would help most:
- Testing on different distros and VPS providers
- Bug reports with reproduction steps

## License

MIT — see [LICENSE](LICENSE)

## Acknowledgments

Gopher is a thin management layer on top of two great projects: [Caddy](https://caddyserver.com/) by Matt Holt and the team, and [rathole](https://github.com/rathole-org/rathole) (originally by rapiz1). Without them there is no Gopher.
