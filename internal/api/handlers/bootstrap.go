package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/service"
)

type BootstrapHandler struct {
	svc *service.BootstrapService
}

func NewBootstrapHandler(svc *service.BootstrapService) *BootstrapHandler {
	return &BootstrapHandler{svc: svc}
}

// POST /api/bootstrap/token - generate a one-time bootstrap token
func (h *BootstrapHandler) GenerateToken(w http.ResponseWriter, r *http.Request) {
	bt, err := h.svc.GenerateToken()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	hostURL := fmt.Sprintf("%s://%s", scheme, host)
	bootstrapCmd := fmt.Sprintf("curl -s %s/static/bootstrap.sh | bash -s -- %s", hostURL, bt.Token)

	response.Success(w, map[string]string{
		"token":             bt.Token,
		"bootstrap_command": bootstrapCmd,
		"expires_at":        bt.ExpiresAt.Format("2006-01-02T15:04:05Z"),
	})
}

// POST /api/bootstrap - called by machines during self-registration
func (h *BootstrapHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req service.BootstrapRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	if req.Token == "" || req.Name == "" || req.Username == "" {
		response.BadRequest(w, "token, name, and username are required")
		return
	}

	resp, err := h.svc.Register(req, r.Host)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, resp)
}

// GET /static/bootstrap.sh - serve bootstrap script dynamically
func (h *BootstrapHandler) ServeScript(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	hostURL := fmt.Sprintf("%s://%s", scheme, r.Host)

	script := generateBootstrapScript(hostURL)
	w.Header().Set("Content-Type", "text/x-shellscript")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, script)
}

func generateBootstrapScript(hostURL string) string {
	return `#!/bin/bash

HOST_URL="` + hostURL + `"
TOKEN="$1"

if [ -z "$TOKEN" ]; then
  echo "Usage: bash bootstrap.sh <TOKEN>"
  echo "Example: curl -s ` + hostURL + `/static/bootstrap.sh | bash -s -- TOKEN"
  exit 1
fi

# ── Interactive prompts via /dev/tty (safe when piped via curl | bash) ────────
if [ ! -c /dev/tty ]; then
  echo "ERROR: No terminal available. Run the script directly."
  exit 1
fi

echo "=== Gopher Machine Bootstrap ==="
echo ""
printf "Machine name (e.g. 'web-server'): " >/dev/tty
read -r MACHINE_NAME </dev/tty
while [ -z "$MACHINE_NAME" ]; do
  printf "Machine name cannot be empty. Try again: " >/dev/tty
  read -r MACHINE_NAME </dev/tty
done
printf "SSH username on this machine (default: %s): " "$USER" >/dev/tty
read -r SSH_USER </dev/tty
SSH_USER="${SSH_USER:-$USER}"

# ── Check for passwordless sudo ───────────────────────────────────────────────
HAS_SUDO=false
if sudo -n true 2>/dev/null; then
  HAS_SUDO=true
fi

# ── Register with control plane ───────────────────────────────────────────────
echo ""
echo "Registering with Gopher control plane..."
RESPONSE=$(curl -sf -X POST "$HOST_URL/api/bootstrap" \
  -H "Content-Type: application/json" \
  -d "{\"token\":\"$TOKEN\",\"name\":\"$MACHINE_NAME\",\"username\":\"$SSH_USER\"}") || {
  echo "ERROR: Registration failed. Is the token valid and not expired?"
  exit 1
}

# Parse response (jq preferred, python3 fallback)
_json() {
  if command -v jq &>/dev/null; then
    echo "$RESPONSE" | jq -r ".data.$1"
  else
    echo "$RESPONSE" | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['$1'])"
  fi
}

TUNNEL_PORT=$(_json tunnel_port)
VPS_PUBLIC_KEY=$(_json vps_ssh_public_key)
RATHOLE_CONFIG=$(_json rathole_client_config)

if [ -z "$TUNNEL_PORT" ] || [ "$TUNNEL_PORT" = "null" ]; then
  echo "ERROR: Unexpected response from server."
  echo "$RESPONSE"
  exit 1
fi

echo "Registered! Tunnel port: $TUNNEL_PORT"

# ── Install VPS SSH key ───────────────────────────────────────────────────────
echo "Installing server SSH key to authorized_keys..."
mkdir -p ~/.ssh
chmod 700 ~/.ssh
touch ~/.ssh/authorized_keys
chmod 600 ~/.ssh/authorized_keys
if ! grep -qF "$VPS_PUBLIC_KEY" ~/.ssh/authorized_keys 2>/dev/null; then
  echo "$VPS_PUBLIC_KEY" >> ~/.ssh/authorized_keys
fi

# ── Install rathole binary ────────────────────────────────────────────────────
echo "Installing rathole..."
if ! command -v rathole &>/dev/null && [ ! -f "$HOME/.local/bin/rathole" ]; then
  ARCH=$(uname -m)
  case "$ARCH" in
    x86_64)  ARCH_TAG="x86_64-unknown-linux-gnu" ;;
    aarch64) ARCH_TAG="aarch64-unknown-linux-musl" ;;
    armv7l)  ARCH_TAG="armv7-unknown-linux-musleabihf" ;;
    *)       echo "ERROR: Unsupported architecture: $ARCH"; exit 1 ;;
  esac
  RATHOLE_URL="https://github.com/rathole-org/rathole/releases/download/v0.5.0/rathole-${ARCH_TAG}.zip"
  rm -rf /tmp/rathole-dl
  mkdir -p /tmp/rathole-dl
  echo "  Downloading from $RATHOLE_URL ..."
  if command -v wget &>/dev/null; then
    wget -q "$RATHOLE_URL" -O /tmp/rathole-dl/rathole.zip || { echo "ERROR: Download failed"; exit 1; }
  else
    curl -fsSL "$RATHOLE_URL" -o /tmp/rathole-dl/rathole.zip || { echo "ERROR: Download failed"; exit 1; }
  fi
  if ! command -v unzip &>/dev/null; then
    echo "  unzip not found, trying apt..."
    if [ "$HAS_SUDO" = true ]; then sudo apt-get install -y unzip -qq; else echo "ERROR: unzip not available and cannot install (no sudo). Install unzip manually."; exit 1; fi
  fi
  unzip -q /tmp/rathole-dl/rathole.zip -d /tmp/rathole-dl/ || { echo "ERROR: unzip failed"; exit 1; }

  if [ "$HAS_SUDO" = true ]; then
    sudo cp /tmp/rathole-dl/rathole /usr/local/bin/rathole
    sudo chmod +x /usr/local/bin/rathole
    RATHOLE_BIN=/usr/local/bin/rathole
  else
    mkdir -p "$HOME/.local/bin"
    cp /tmp/rathole-dl/rathole "$HOME/.local/bin/rathole"
    chmod +x "$HOME/.local/bin/rathole"
    RATHOLE_BIN="$HOME/.local/bin/rathole"
  fi
  rm -rf /tmp/rathole-dl
else
  RATHOLE_BIN=$(command -v rathole 2>/dev/null || echo "$HOME/.local/bin/rathole")
fi
echo "  rathole binary: $RATHOLE_BIN"

# ── Write rathole client config ───────────────────────────────────────────────
echo "Writing rathole client config..."
if [ "$HAS_SUDO" = true ]; then
  sudo mkdir -p /etc/rathole
  echo "$RATHOLE_CONFIG" | sudo tee /etc/rathole/client.toml >/dev/null
  CONFIG_PATH=/etc/rathole/client.toml
else
  mkdir -p "$HOME/.config/rathole"
  echo "$RATHOLE_CONFIG" > "$HOME/.config/rathole/client.toml"
  CONFIG_PATH="$HOME/.config/rathole/client.toml"
fi

# ── Install systemd service ───────────────────────────────────────────────────
echo "Installing systemd service..."
if [ "$HAS_SUDO" = true ]; then
  sudo tee /etc/systemd/system/rathole-client.service >/dev/null <<EOF
[Unit]
Description=Rathole Tunnel Client
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$RATHOLE_BIN $CONFIG_PATH
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF
  sudo systemctl daemon-reload
  sudo systemctl enable rathole-client
  sudo systemctl restart rathole-client
  echo "  Service installed (system). Check: sudo systemctl status rathole-client"
else
  mkdir -p "$HOME/.config/systemd/user"
  cat > "$HOME/.config/systemd/user/rathole-client.service" <<EOF
[Unit]
Description=Rathole Tunnel Client
After=network-online.target

[Service]
Type=simple
ExecStart=$RATHOLE_BIN $CONFIG_PATH
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
EOF
  systemctl --user daemon-reload
  systemctl --user enable rathole-client
  systemctl --user restart rathole-client
  loginctl enable-linger "$USER" 2>/dev/null || true
  echo "  Service installed (user). Check: systemctl --user status rathole-client"
fi

echo ""
echo "=== Bootstrap complete! ==="
echo "Machine '$MACHINE_NAME' registered. Tunnel port: $TUNNEL_PORT"
echo "The server will SSH back as '$SSH_USER' through the tunnel to verify connectivity."
`
}
