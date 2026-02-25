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
	bootstrapCmd := fmt.Sprintf("curl -s %s/static/bootstrap.sh | bash -s -- %s %s", hostURL, hostURL, bt.Token)

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

	resp, err := h.svc.Register(req)
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
set -e

HOST_URL="` + hostURL + `"
TOKEN="$1"

if [ -z "$TOKEN" ]; then
  echo "Usage: bash bootstrap.sh <TOKEN>"
  echo "Example: curl -s ` + hostURL + `/static/bootstrap.sh | bash -s -- TOKEN"
  exit 1
fi

echo "=== Gopher Machine Bootstrap ==="
echo ""
read -p "Machine name (e.g. 'immich-server'): " MACHINE_NAME
read -p "SSH username for VPS to connect back (default: $USER): " SSH_USER
SSH_USER="${SSH_USER:-$USER}"

echo ""
echo "Registering with Gopher control plane..."
RESPONSE=$(curl -sf -X POST "$HOST_URL/api/bootstrap" \
  -H "Content-Type: application/json" \
  -d "{\"token\":\"$TOKEN\",\"name\":\"$MACHINE_NAME\",\"username\":\"$SSH_USER\"}")

if [ $? -ne 0 ]; then
  echo "ERROR: Registration failed. Is the token valid and not expired?"
  exit 1
fi

TUNNEL_PORT=$(echo "$RESPONSE" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['data']['tunnel_port'])")
VPS_PUBLIC_KEY=$(echo "$RESPONSE" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['data']['vps_ssh_public_key'])")
RATHOLE_CONFIG=$(echo "$RESPONSE" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['data']['rathole_client_config'])")

echo "Registered! Tunnel port: $TUNNEL_PORT"
echo ""
echo "Installing VPS SSH key..."
mkdir -p ~/.ssh
chmod 700 ~/.ssh
if ! grep -qF "$VPS_PUBLIC_KEY" ~/.ssh/authorized_keys 2>/dev/null; then
  echo "$VPS_PUBLIC_KEY" >> ~/.ssh/authorized_keys
  chmod 600 ~/.ssh/authorized_keys
fi

echo "Installing rathole..."
ARCH=$(uname -m)
if [ "$ARCH" = "x86_64" ]; then ARCH="x86_64"; else ARCH="aarch64"; fi
RATHOLE_URL="https://github.com/rapiz1/rathole/releases/latest/download/rathole-$ARCH-unknown-linux-musl.zip"
if ! command -v rathole &>/dev/null; then
  if command -v wget &>/dev/null; then
    wget -q "$RATHOLE_URL" -O /tmp/rathole.zip
  else
    curl -sL "$RATHOLE_URL" -o /tmp/rathole.zip
  fi
  if command -v unzip &>/dev/null; then
    unzip -q /tmp/rathole.zip -d /tmp/rathole-bin/
    sudo cp /tmp/rathole-bin/rathole /usr/local/bin/rathole
    sudo chmod +x /usr/local/bin/rathole
  else
    echo "WARNING: unzip not found, please install rathole manually to /usr/local/bin/rathole"
  fi
fi

echo "Writing rathole client config..."
sudo mkdir -p /etc/rathole
echo "$RATHOLE_CONFIG" | sudo tee /etc/rathole/client.toml > /dev/null

echo "Installing systemd service..."
sudo tee /etc/systemd/system/rathole-client.service > /dev/null << EOF
[Unit]
Description=Rathole Tunnel Client
After=network.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/rathole /etc/rathole/client.toml
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable rathole-client
sudo systemctl start rathole-client

echo ""
echo "=== Bootstrap complete! ==="
echo "Machine '$MACHINE_NAME' is now registered and tunneling to VPS."
echo "VPS SSH username: $SSH_USER, tunnel port: $TUNNEL_PORT"
echo ""
echo "Check status: systemctl status rathole-client"
`
}
