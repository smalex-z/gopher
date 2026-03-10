#!/bin/bash

HOST_URL="{{.HostURL}}"
TOKEN="$1"

if [ -z "$TOKEN" ]; then
  echo "Usage: bash bootstrap.sh <TOKEN>"
  echo "Example: curl -s {{.HostURL}}/static/bootstrap.sh | bash -s -- TOKEN"
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
RESPONSE=$(curl -sS -w "\n__HTTP_STATUS__:%{http_code}" -X POST "$HOST_URL/api/bootstrap" \
  -H "Content-Type: application/json" \
  -d "{\"token\":\"$TOKEN\",\"name\":\"$MACHINE_NAME\",\"username\":\"$SSH_USER\"}" 2>&1)

HTTP_STATUS=$(printf '%s' "$RESPONSE" | tail -1 | sed 's/.*__HTTP_STATUS__://')
RESPONSE=$(printf '%s' "$RESPONSE" | sed '$d' | sed 's/__HTTP_STATUS__:[0-9]*//')

if [ "$HTTP_STATUS" != "200" ]; then
  echo "ERROR: Registration failed (HTTP $HTTP_STATUS)."
  echo "Server response: $RESPONSE"
  exit 1
fi

# Parse response (jq preferred, python3 fallback)
_json() {
  if command -v jq &>/dev/null; then
    printf '%s\n' "$RESPONSE" | jq -r ".data.$1"
  else
    printf '%s\n' "$RESPONSE" | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['$1'])"
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
  sudo chown "$SSH_USER" /etc/rathole
  echo "$RATHOLE_CONFIG" > /etc/rathole/client.toml
  sudo chown "$SSH_USER" /etc/rathole/client.toml
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
User=$SSH_USER
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
  echo "  Service installed (system, running as $SSH_USER). Check: systemctl status rathole-client"
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
