#!/bin/bash

HOST_URL="{{.HostURL}}"
TOKEN="$1"

if [ "$(id -u)" -eq 0 ]; then SUDO=""; else SUDO="sudo"; fi

if [ -z "$TOKEN" ]; then
  echo "Usage: bash bootstrap.sh <TOKEN>"
  echo "Example: curl -s {{.HostURL}}/static/bootstrap.sh | bash -s -- TOKEN"
  exit 1
fi

echo "=== Gopher Machine Bootstrap ==="
echo ""
# Prefer the explicit env var. Otherwise prompt only when /dev/tty is actually
# usable — the device file exists in many non-interactive contexts (piped SSH,
# containers) but reads/writes fail with "no such device or address". Probe
# with a real open before relying on it.
if [ -n "$GOPHER_MACHINE_NAME" ]; then
  MACHINE_NAME="$GOPHER_MACHINE_NAME"
  echo "Machine name: $MACHINE_NAME"
elif (exec </dev/tty) 2>/dev/null; then
  printf "Machine name (e.g. 'web-server'): " >/dev/tty
  read -r MACHINE_NAME </dev/tty
  while [ -z "$MACHINE_NAME" ]; do
    printf "Machine name cannot be empty. Try again: " >/dev/tty
    read -r MACHINE_NAME </dev/tty
  done
else
  # Non-interactive caller didn't pass GOPHER_MACHINE_NAME — fall back to
  # the box's own hostname so the bootstrap still completes. Better than
  # exiting; the operator can rename it from the dashboard later.
  MACHINE_NAME=$(hostname -s 2>/dev/null || hostname 2>/dev/null || echo "unnamed-machine")
  echo "Machine name: $MACHINE_NAME (auto-derived; pass GOPHER_MACHINE_NAME to override)"
fi
SSH_USER="${GOPHER_SSH_USER:-$USER}"
echo "SSH user: $SSH_USER"

handle_sudo_failure() {
  echo ""
  echo "ERROR: Failed to install system-wide service (requires root or sudo)."
  echo ""
  echo "Option 1: Re-run bootstrap as root:"
  echo "  sudo bash"
  echo "  # then run: curl -s {{.HostURL}}/static/bootstrap.sh | bash -s -- $TOKEN"
  echo ""
  echo "Option 2: Run rathole manually in the foreground (for testing):"
  echo "  $RATHOLE_BIN /etc/rathole/client.toml"
  echo ""
  echo "Option 3: Run rathole in background with nohup:"
  echo "  nohup $RATHOLE_BIN /etc/rathole/client.toml >/dev/null 2>&1 &"
  echo ""
  echo "Option 4: Run rathole in tmux (persistent session):"
  echo "  tmux new-session -d -s rathole \"$RATHOLE_BIN /etc/rathole/client.toml\""
  echo ""
  echo "Option 5: Add to crontab for auto-restart on reboot:"
  echo "  (crontab -l 2>/dev/null; echo \"@reboot $RATHOLE_BIN /etc/rathole/client.toml\") | crontab -"
  echo ""
  exit 1
}

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
    # cloud-init / unattended-upgrades commonly hold dpkg during early boot.
    # Wait up to 2 min for the lock to free before giving up. apt's own
    # DPkg::Lock::Timeout makes apt itself wait; the outer loop also retries
    # the install in case of transient errors.
    apt_install_with_retry() {
      local pkg="$1"
      local attempt
      for attempt in 1 2 3 4 5; do
        if $SUDO apt-get install -y -qq -o DPkg::Lock::Timeout=120 "$pkg"; then
          return 0
        fi
        echo "  apt-get install $pkg failed (attempt $attempt/5); retrying in 10s..."
        sleep 10
      done
      return 1
    }
    if command -v dnf &>/dev/null; then
      echo "  unzip not found, installing via dnf..."
      $SUDO dnf install -y -q unzip || { echo "ERROR: unzip not available and could not be installed. Install it manually and re-run."; exit 1; }
    elif command -v yum &>/dev/null; then
      echo "  unzip not found, installing via yum..."
      $SUDO yum install -y -q unzip || { echo "ERROR: unzip not available and could not be installed. Install it manually and re-run."; exit 1; }
    else
      echo "  unzip not found, installing via apt (with lock-wait + retry)..."
      apt_install_with_retry unzip || { echo "ERROR: unzip not available and could not be installed. Install it manually and re-run."; exit 1; }
    fi
  fi
  unzip -q /tmp/rathole-dl/rathole.zip -d /tmp/rathole-dl/ || { echo "ERROR: unzip failed"; exit 1; }

  $SUDO cp /tmp/rathole-dl/rathole /usr/local/bin/rathole
  $SUDO chmod +x /usr/local/bin/rathole
  RATHOLE_BIN=/usr/local/bin/rathole
  rm -rf /tmp/rathole-dl
else
  RATHOLE_BIN=$(command -v rathole 2>/dev/null || echo "$HOME/.local/bin/rathole")
fi
echo "  rathole binary: $RATHOLE_BIN"

# ── Write rathole client config ───────────────────────────────────────────────
echo "Writing rathole client config..."


$SUDO mkdir -p /etc/rathole || handle_sudo_failure
echo "$RATHOLE_CONFIG" | $SUDO tee /etc/rathole/client.toml >/dev/null || handle_sudo_failure
$SUDO chown "$SSH_USER" /etc/rathole/client.toml || handle_sudo_failure

# Save VPS public key so the uninstall script can remove it from authorized_keys
# even without a live connection to the server.
echo "$VPS_PUBLIC_KEY" | $SUDO tee /etc/rathole/vps_key.pub >/dev/null || true

# ── Install gopher-uninstall script ──────────────────────────────────────────
echo "Installing gopher-uninstall script..."
if command -v wget &>/dev/null; then
  wget -q "{{.HostURL}}/static/gopher-uninstall.sh" -O /tmp/gopher-uninstall.sh || true
else
  curl -fsSL "{{.HostURL}}/static/gopher-uninstall.sh" -o /tmp/gopher-uninstall.sh || true
fi
if [ -s /tmp/gopher-uninstall.sh ]; then
  $SUDO mv /tmp/gopher-uninstall.sh /usr/local/bin/gopher-uninstall
  $SUDO chmod +x /usr/local/bin/gopher-uninstall
  echo "  Installed to /usr/local/bin/gopher-uninstall"

  # Grant the current user passwordless sudo for commands the gopher server
  # needs to trigger non-interactively over SSH.
  SUDOERS_FILE="/etc/sudoers.d/gopher"
  printf '%s\n' \
    "$SSH_USER ALL=(ALL) NOPASSWD: /usr/local/bin/gopher-uninstall" \
    "$SSH_USER ALL=(ALL) NOPASSWD: /usr/bin/systemctl restart rathole-client" | $SUDO tee "$SUDOERS_FILE" >/dev/null
  $SUDO chmod 0440 "$SUDOERS_FILE"
  echo "  Sudoers rule written to $SUDOERS_FILE"
else
  echo "  WARNING: could not download gopher-uninstall script (server may be unreachable)"
  rm -f /tmp/gopher-uninstall.sh
fi

# ── Install system-wide service ──────────────────────────────────────────────
echo "Installing system rathole-client service..."
$SUDO tee /etc/systemd/system/rathole-client.service >/dev/null <<EOF || handle_sudo_failure
[Unit]
Description=Rathole Tunnel Client
After=network.target

[Service]
Type=simple
User=$SSH_USER
ExecStart=$RATHOLE_BIN /etc/rathole/client.toml
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF
$SUDO systemctl daemon-reload || handle_sudo_failure
$SUDO systemctl enable rathole-client || handle_sudo_failure
$SUDO systemctl restart rathole-client || handle_sudo_failure
echo "  Service installed (system). Check: systemctl status rathole-client"

echo ""
echo "=== Bootstrap complete! ==="
echo "Machine '$MACHINE_NAME' registered as '$SSH_USER'. Tunnel port: $TUNNEL_PORT"
echo "Rathole config: /etc/rathole/client.toml"
echo "Service: sudo systemctl status rathole-client"
echo "The server will SSH back through the tunnel to verify connectivity."
