#!/bin/bash

HOST_URL="{{.HostURL}}"
TOKEN="$1"

if [ "$(id -u)" -eq 0 ]; then SUDO=""; else SUDO="sudo"; fi

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
SSH_USER="$USER"
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
AGENT_TOKEN=$(_json agent_token 2>/dev/null || echo "")
AGENT_PORT=$(_json agent_local_port 2>/dev/null || echo "")

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
    if command -v dnf &>/dev/null; then
      echo "  unzip not found, installing via dnf..."
      $SUDO dnf install -y -q unzip || { echo "ERROR: unzip not available and could not be installed. Install it manually and re-run."; exit 1; }
    elif command -v yum &>/dev/null; then
      echo "  unzip not found, installing via yum..."
      $SUDO yum install -y -q unzip || { echo "ERROR: unzip not available and could not be installed. Install it manually and re-run."; exit 1; }
    else
      echo "  unzip not found, installing via apt..."
      $SUDO apt-get install -y unzip -qq || { echo "ERROR: unzip not available and could not be installed. Install it manually and re-run."; exit 1; }
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

if [ -f "/etc/rathole/client.toml" ]; then
  echo "WARNING: existing rathole config detected at /etc/rathole/client.toml"
  printf "Continue and overwrite? [y/N]: " >/dev/tty
  read -r OVERWRITE_CONFIRM </dev/tty
  case "$OVERWRITE_CONFIRM" in
    [yY]|[yY][eE][sS]) ;;
    *)
      echo "Aborted by user"
      exit 1
      ;;
  esac
fi

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
  # The agent uses `systemctl start` (not restart) for recovery so existing
  # tunnels on the box don't flap; reset-failed clears systemd's failure
  # counter when a unit has hit its restart-burst limit. Restart is kept for
  # operator-triggered hard restarts.
  printf '%s\n' \
    "$SSH_USER ALL=(ALL) NOPASSWD: /usr/local/bin/gopher-uninstall" \
    "$SSH_USER ALL=(ALL) NOPASSWD: /usr/bin/systemctl start rathole-client" \
    "$SSH_USER ALL=(ALL) NOPASSWD: /usr/bin/systemctl restart rathole-client" \
    "$SSH_USER ALL=(ALL) NOPASSWD: /usr/bin/systemctl reset-failed rathole-client" | $SUDO tee "$SUDOERS_FILE" >/dev/null
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

# ── Install gopher-agent ─────────────────────────────────────────────────────
# Mirrors migrate.sh exactly so new bootstraps and migrated machines end up
# in the same shape. Agent runs as a dedicated `gopher` system user with
# NOPASSWD: ALL — same model as the server-side gopher user.
#
# Optional. If AGENT_TOKEN/AGENT_PORT are missing (older server) the install
# is skipped and the migration UI on the dashboard will offer to add it later.
if [ -n "$AGENT_TOKEN" ] && [ "$AGENT_TOKEN" != "null" ] && [ -n "$AGENT_PORT" ] && [ "$AGENT_PORT" != "null" ]; then
  echo "Installing gopher-agent..."
  AGENT_ARCH_TAG="linux-amd64"
  case "$(uname -m)" in
    x86_64)         AGENT_ARCH_TAG="linux-amd64" ;;
    aarch64|arm64)  AGENT_ARCH_TAG="linux-arm64" ;;
    *)
      echo "  WARN: unsupported arch $(uname -m); skipping agent install"
      AGENT_ARCH_TAG=""
      ;;
  esac
  if [ -n "$AGENT_ARCH_TAG" ]; then
    # 1. Create gopher system user (idempotent).
    if ! id -u gopher >/dev/null 2>&1; then
      $SUDO useradd --system --shell /usr/sbin/nologin --home-dir /nonexistent --no-create-home gopher
      echo "  Created gopher system user"
    fi

    # 2. Sudoers: gopher gets NOPASSWD: ALL. Strip any prior gopher-* line
    # (e.g. older bootstraps that scoped narrower) before re-adding.
    SUDOERS_FILE="/etc/sudoers.d/gopher"
    $SUDO touch "$SUDOERS_FILE"
    $SUDO sh -c "grep -v '^gopher ' '$SUDOERS_FILE' 2>/dev/null > '$SUDOERS_FILE.tmp' || true; echo 'gopher ALL=(ALL) NOPASSWD: ALL' >> '$SUDOERS_FILE.tmp'; mv '$SUDOERS_FILE.tmp' '$SUDOERS_FILE'; chmod 0440 '$SUDOERS_FILE'"

    # 3. Download agent binary.
    AGENT_URL="$HOST_URL/static/agents/gopher-agent-${AGENT_ARCH_TAG}"
    rm -f /tmp/gopher-agent.new
    if command -v wget &>/dev/null; then
      wget -q "$AGENT_URL" -O /tmp/gopher-agent.new || { echo "  WARN: agent download failed"; rm -f /tmp/gopher-agent.new; }
    else
      curl -fsSL "$AGENT_URL" -o /tmp/gopher-agent.new || { echo "  WARN: agent download failed"; rm -f /tmp/gopher-agent.new; }
    fi
    if [ -s /tmp/gopher-agent.new ]; then
      $SUDO install -m 0755 -o root -g root /tmp/gopher-agent.new /usr/local/bin/gopher-agent
      rm -f /tmp/gopher-agent.new

      # 4. Agent config (env file consumed by EnvironmentFile=).
      $SUDO mkdir -p /etc/gopher-agent
      $SUDO tee /etc/gopher-agent/config.env >/dev/null <<EOF || true
GOPHER_AGENT_TOKEN=$AGENT_TOKEN
GOPHER_AGENT_PORT=$AGENT_PORT
GOPHER_AGENT_UNIT=rathole-client.service
EOF
      $SUDO chmod 640 /etc/gopher-agent/config.env
      $SUDO chown root:gopher /etc/gopher-agent/config.env

      # 5. Hand /etc/rathole/client.toml to gopher so the agent can write
      # config-push directly without sudo. rathole-client (running as
      # $SSH_USER) keeps reading via mode 0644.
      $SUDO chown gopher:gopher /etc/rathole/client.toml
      $SUDO chmod 0644 /etc/rathole/client.toml

      # 6. systemd unit + service start. User=gopher, not $SSH_USER.
      $SUDO tee /etc/systemd/system/gopher-agent.service >/dev/null <<EOF || true
[Unit]
Description=Gopher Agent (control-plane back-channel)
After=network.target

[Service]
Type=simple
User=gopher
EnvironmentFile=/etc/gopher-agent/config.env
ExecStart=/usr/local/bin/gopher-agent
Restart=always
RestartSec=5
# KillMode=process so the agent's children (the detached gopher-uninstall
# worker spawned from POST /uninstall) survive when this unit is stopped.
# With the default control-group, systemctl-stopping gopher-agent would
# kill gopher-uninstall mid-cleanup — exactly what we don't want.
KillMode=process
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF
      $SUDO systemctl daemon-reload || true
      $SUDO systemctl enable gopher-agent || true
      $SUDO systemctl restart gopher-agent || true
      echo "  gopher-agent installed and started on 127.0.0.1:$AGENT_PORT (user: gopher)"
    fi
  fi
else
  echo "Skipping gopher-agent install (server didn't return agent fields)"
fi

echo ""
echo "=== Bootstrap complete! ==="
echo "Machine '$MACHINE_NAME' registered as '$SSH_USER'. Tunnel port: $TUNNEL_PORT"
echo "Rathole config: /etc/rathole/client.toml"
echo "Service: sudo systemctl status rathole-client"
echo "The server will SSH back through the tunnel to verify connectivity."
