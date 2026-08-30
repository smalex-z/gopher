#!/bin/bash

HOST_URL="{{.HostURL}}"

# Parse args: the bootstrap token plus optional flags. --no-ssh provisions an
# agent-only machine — no SSH back-tunnel, no authorized_keys entry; control runs
# entirely over the agent. The flag is authoritative: the server honors it
# regardless of the token's SSH setting (SSH can only be turned off here).
TOKEN=""
NO_SSH=0
for arg in "$@"; do
  case "$arg" in
    --no-ssh) NO_SSH=1 ;;
    -*) echo "Warning: unknown flag $arg" >&2 ;;
    *) [ -z "$TOKEN" ] && TOKEN="$arg" ;;
  esac
done

if [ "$(id -u)" -eq 0 ]; then SUDO=""; else SUDO="sudo"; fi

# Consolidated /etc/gopher origin layout (matches the edge). The agent migrates
# legacy origins (/etc/rathole, /etc/gopher-agent) onto these on upgrade; fresh
# bootstraps write them directly.
RATHOLE_DIR="/etc/gopher/rathole"
CLIENT_CFG="$RATHOLE_DIR/client.toml"
VPS_KEY_FILE="$RATHOLE_DIR/vps_key.pub"
AGENT_DIR="/etc/gopher/agent"
AGENT_CFG="$AGENT_DIR/config.env"

if [ -z "$TOKEN" ]; then
  echo "Usage: bash bootstrap.sh <TOKEN> [--no-ssh]"
  echo "Example: curl -s {{.HostURL}}/static/bootstrap.sh | bash -s -- TOKEN"
  echo "  --no-ssh   agent-only machine: no SSH back-tunnel or authorized_keys entry"
  exit 1
fi

# ── Interactive prompts via /dev/tty (safe when piped via curl | bash) ────────
if [ ! -c /dev/tty ]; then
  echo "ERROR: No terminal available. Run the script directly."
  exit 1
fi

echo "=== Gopher Machine Bootstrap ==="
echo ""

# ── Detect prior install and clean up before re-bootstrap ────────────────────
# A leftover /usr/local/bin/gopher-uninstall (or rathole-client systemd unit,
# or /etc/rathole) from a previous bootstrap will conflict with the new one:
# stale tunnels in client.toml, stale agent token in /etc/gopher-agent, the
# old VPS public key still in authorized_keys. Run gopher-uninstall first so
# the new bootstrap starts from a known-clean state. The uninstall script
# notifies the previous server of the deletion (best-effort) and removes all
# local services/configs/binaries.
if [ -x /usr/local/bin/gopher-uninstall ] || [ -f /etc/systemd/system/rathole-client.service ] || [ -d /etc/rathole ] || [ -d /etc/gopher/rathole ]; then
  echo "Detected an existing Gopher install on this machine."
  echo "Running gopher-uninstall to clear it before re-bootstrapping..."
  if [ -x /usr/local/bin/gopher-uninstall ]; then
    $SUDO /usr/local/bin/gopher-uninstall || echo "  WARN: prior gopher-uninstall reported errors — continuing"
  else
    # Old/partial install with no uninstaller on disk. Stop services + remove
    # the directories the new bootstrap will recreate (both the consolidated
    # /etc/gopher layout and the legacy /etc/rathole + /etc/gopher-agent one).
    # Best-effort — failures are logged but don't abort.
    $SUDO systemctl stop gopher-agent rathole-client 2>/dev/null || true
    $SUDO systemctl disable gopher-agent rathole-client 2>/dev/null || true
    $SUDO rm -f /etc/systemd/system/gopher-agent.service /etc/systemd/system/rathole-client.service
    $SUDO rm -rf /etc/gopher/rathole /etc/gopher/agent /etc/rathole /etc/gopher-agent
    $SUDO rm -f /usr/local/bin/gopher-agent /usr/local/bin/rathole
    $SUDO systemctl daemon-reload 2>/dev/null || true
    echo "  Removed legacy service files and configs"
  fi
  echo ""
fi

printf "Machine name (e.g. 'web-server'): " >/dev/tty
read -r MACHINE_NAME </dev/tty
while [ -z "$MACHINE_NAME" ]; do
  printf "Machine name cannot be empty. Try again: " >/dev/tty
  read -r MACHINE_NAME </dev/tty
done
# $USER isn't guaranteed to be set under `curl | bash` (cron, minimal sudo,
# bare containers). Fall back to the real user so the rathole-client systemd
# unit never silently gets User= empty (which runs it as root).
SSH_USER="${USER:-$(id -un)}"
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
  echo "  $RATHOLE_BIN $CLIENT_CFG"
  echo ""
  echo "Option 3: Run rathole in background with nohup:"
  echo "  nohup $RATHOLE_BIN $CLIENT_CFG >/dev/null 2>&1 &"
  echo ""
  echo "Option 4: Run rathole in tmux (persistent session):"
  echo "  tmux new-session -d -s rathole \"$RATHOLE_BIN $CLIENT_CFG\""
  echo ""
  echo "Option 5: Add to crontab for auto-restart on reboot:"
  echo "  (crontab -l 2>/dev/null; echo \"@reboot $RATHOLE_BIN $CLIENT_CFG\") | crontab -"
  echo ""
  exit 1
}

# ── Register with control plane ───────────────────────────────────────────────
echo ""
echo "Registering with Gopher control plane..."
NO_SSH_JSON=""
if [ "$NO_SSH" = "1" ]; then NO_SSH_JSON=',"no_ssh":true'; fi
RESPONSE=$(curl -sS -w "\n__HTTP_STATUS__:%{http_code}" -X POST "$HOST_URL/api/bootstrap" \
  -H "Content-Type: application/json" \
  -d "{\"token\":\"$TOKEN\",\"name\":\"$MACHINE_NAME\",\"username\":\"$SSH_USER\"$NO_SSH_JSON}" 2>&1)

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
# Guard the other required fields too — a partial/garbled response would
# otherwise write an empty client.toml (and skip the server key), leaving a
# half-installed machine that silently never connects.
if [ -z "$RATHOLE_CONFIG" ] || [ "$RATHOLE_CONFIG" = "null" ]; then
  echo "ERROR: server did not return a rathole client config; aborting."
  echo "$RESPONSE"
  exit 1
fi
if [ "$NO_SSH" = "1" ] || [ "$TUNNEL_PORT" = "0" ]; then
  echo "Registered! (agent-only — no SSH tunnel)"
else
  echo "Registered! SSH tunnel port: $TUNNEL_PORT"
fi

# ── Install VPS SSH key ───────────────────────────────────────────────────────
# Agent-only machines (--no-ssh) get no server key: the server returns an empty
# key and we skip the authorized_keys install entirely. Otherwise Gopher manages
# exactly ONE key, tagged with the `gopher-managed` comment — we drop any prior
# gopher-managed key and write this one, so re-bootstraps never accumulate stale
# keys. Operator-owned keys (any without the marker) are left untouched.
if [ "$NO_SSH" = "1" ]; then
  echo "SSH access disabled for this machine (agent-only) — skipping authorized_keys install."
else
  if [ -z "$VPS_PUBLIC_KEY" ] || [ "$VPS_PUBLIC_KEY" = "null" ]; then
    echo "ERROR: server did not return its SSH public key; aborting."
    echo "$RESPONSE"
    exit 1
  fi
  echo "Installing server SSH key to authorized_keys..."
  # This whole block used to have no error checking at all — if mkdir/touch
  # failed (read-only/quota-full/permission-restricted $HOME), every step
  # after it silently no-op'd or failed too, and the script sailed on to
  # "Bootstrap complete!" with the SSH back-channel never actually installed
  # and no error pointing at why.
  if ! mkdir -p ~/.ssh; then
    echo "ERROR: could not create ~/.ssh — the SSH back-channel will not work. Aborting."
    exit 1
  fi
  chmod 700 ~/.ssh
  if ! touch ~/.ssh/authorized_keys; then
    echo "ERROR: could not create ~/.ssh/authorized_keys. Aborting."
    exit 1
  fi
  chmod 600 ~/.ssh/authorized_keys
  MANAGED_LINE=$(printf '%s\n' "$VPS_PUBLIC_KEY" | awk 'NF>=2 {print $1, $2, "gopher-managed"; exit}')
  # `grep -v` exiting 1 just means every line matched the exclusion (or the
  # file's empty, e.g. right after the touch above) — normal, not an error.
  # What we must NOT do is treat a genuine read failure the same way: that
  # would silently truncate authorized_keys to just the new managed line,
  # dropping any of the operator's own keys already in it. Check readability
  # directly instead of trusting grep's exit code to tell them apart.
  if [ ! -r ~/.ssh/authorized_keys ]; then
    echo "ERROR: ~/.ssh/authorized_keys exists but isn't readable — aborting rather than risk overwriting it."
    exit 1
  fi
  grep -v ' gopher-managed[[:space:]]*$' ~/.ssh/authorized_keys > ~/.ssh/authorized_keys.tmp 2>/dev/null
  printf '%s\n' "$MANAGED_LINE" >> ~/.ssh/authorized_keys.tmp
  mv ~/.ssh/authorized_keys.tmp ~/.ssh/authorized_keys
  chmod 600 ~/.ssh/authorized_keys
fi

# ── Install rathole binary ────────────────────────────────────────────────────
echo "Installing rathole..."
if ! command -v rathole &>/dev/null && [ ! -f "$HOME/.local/bin/rathole" ]; then
  ARCH=$(uname -m)
  case "$ARCH" in
    x86_64|aarch64|armv7l) ;;
    *) echo "ERROR: Unsupported architecture: $ARCH"; exit 1 ;;
  esac
  # Fetch the rathole binary the edge bundles for this arch, over the edge's TLS,
  # instead of downloading a zip from GitHub. The edge serves a raw binary plus a
  # ".sha256" sidecar we verify (no unzip dependency).
  RATHOLE_URL="$HOST_URL/static/rathole/${ARCH}"
  echo "  Downloading rathole from $RATHOLE_URL ..."
  rm -f /tmp/rathole-dl
  if command -v curl &>/dev/null; then
    curl -fsSL "$RATHOLE_URL" -o /tmp/rathole-dl || { echo "ERROR: rathole download failed"; exit 1; }
    EXPECTED_SUM=$(curl -fsSL "${RATHOLE_URL}.sha256" 2>/dev/null | awk '{print $1}')
  else
    wget -q "$RATHOLE_URL" -O /tmp/rathole-dl || { echo "ERROR: rathole download failed"; exit 1; }
    EXPECTED_SUM=$(wget -qO- "${RATHOLE_URL}.sha256" 2>/dev/null | awk '{print $1}')
  fi
  if [ -n "$EXPECTED_SUM" ] && command -v sha256sum &>/dev/null; then
    ACTUAL_SUM=$(sha256sum /tmp/rathole-dl | awk '{print $1}')
    if [ "$ACTUAL_SUM" != "$EXPECTED_SUM" ]; then
      echo "ERROR: rathole checksum mismatch (expected $EXPECTED_SUM, got $ACTUAL_SUM)"; rm -f /tmp/rathole-dl; exit 1
    fi
    echo "  rathole checksum verified"
  fi
  # Was unchecked — every other privileged write below this point (mkdir,
  # tee, systemctl) already bails via handle_sudo_failure, but this one
  # didn't, so a failed install here (disk full, unusual /usr/local/bin
  # permissions) still went on to enable+start a systemd unit whose
  # ExecStart points at a binary that was never actually placed —
  # `systemctl restart` reports success (it just queues the start job), the
  # script prints "Bootstrap complete!", and the machine never tunnels.
  if ! $SUDO install -m 0755 /tmp/rathole-dl /usr/local/bin/rathole; then
    echo "ERROR: could not install rathole binary to /usr/local/bin (sudo/permission/disk issue?)"
    rm -f /tmp/rathole-dl
    exit 1
  fi
  RATHOLE_BIN=/usr/local/bin/rathole
  rm -f /tmp/rathole-dl
else
  RATHOLE_BIN=$(command -v rathole 2>/dev/null || echo "$HOME/.local/bin/rathole")
fi
echo "  rathole binary: $RATHOLE_BIN"

# ── Write rathole client config ───────────────────────────────────────────────
echo "Writing rathole client config..."

if [ -f "$CLIENT_CFG" ]; then
  echo "WARNING: existing rathole config detected at $CLIENT_CFG"
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

$SUDO mkdir -p "$RATHOLE_DIR" || handle_sudo_failure
echo "$RATHOLE_CONFIG" | $SUDO tee "$CLIENT_CFG" >/dev/null || handle_sudo_failure
$SUDO chown "$SSH_USER" "$CLIENT_CFG" || handle_sudo_failure

# Save VPS public key so the uninstall script can remove it from authorized_keys
# even without a live connection to the server. Skipped for agent-only machines
# (no key was installed, so there's nothing for uninstall to remove).
if [ "$NO_SSH" != "1" ] && [ -n "$VPS_PUBLIC_KEY" ] && [ "$VPS_PUBLIC_KEY" != "null" ]; then
  echo "$VPS_PUBLIC_KEY" | $SUDO tee "$VPS_KEY_FILE" >/dev/null || true
fi

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
ExecStart=$RATHOLE_BIN $CLIENT_CFG
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
    armv7l|armv7)   AGENT_ARCH_TAG="linux-armv7" ;;
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

    # 3. Pre-write the agent config BEFORE attempting the binary download.
    # Two benefits: (a) gopher-uninstall.sh can still authenticate against
    # /api/machines/self-delete if the binary install fails, so the dashboard
    # stays in sync after a manual cleanup; (b) the migration tool can finish
    # the install later without round-tripping the token through the user.
    $SUDO mkdir -p "$AGENT_DIR"
    $SUDO tee "$AGENT_CFG" >/dev/null <<EOF || true
GOPHER_AGENT_TOKEN=$AGENT_TOKEN
GOPHER_AGENT_PORT=$AGENT_PORT
GOPHER_AGENT_UNIT=rathole-client.service
GOPHER_EDGE_URL=$HOST_URL
EOF
    $SUDO chmod 640 "$AGENT_CFG"
    $SUDO chown root:gopher "$AGENT_CFG"

    # 4. Download agent binary. We prefer curl (its -fS pair shows transport
    # errors loudly so a failed download stops being a silent skip) and fall
    # back to a cert-tolerant retry the way migrate.sh does — old distros
    # without recent CA bundles otherwise fail TLS even though the URL is
    # fine. wget is the no-curl fallback; we drop -q so the same diagnostics
    # surface there too.
    AGENT_URL="$HOST_URL/static/agents/gopher-agent-${AGENT_ARCH_TAG}"
    # Download to a UNIQUE mktemp file, never a fixed /tmp path. /tmp is sticky,
    # so a stale gopher-agent.new left by a prior run — or by the agent's own
    # self-update, which runs as the `gopher` user — can't be removed or
    # overwritten by this script (it runs as $SSH_USER), giving "Operation not
    # permitted" / "Permission denied". Worse, a non-empty leftover would pass
    # the `-s` check and get installed as a STALE binary. A fresh mktemp file is
    # owned by us and collision-free.
    AGENT_TMP=$(mktemp /tmp/gopher-agent.XXXXXX 2>/dev/null || echo "/tmp/gopher-agent.$$.new")
    echo "  Downloading agent: $AGENT_URL"
    if command -v curl >/dev/null 2>&1; then
      curl -fSL "$AGENT_URL" -o "$AGENT_TMP" \
        || { echo "  curl failed; retrying with --insecure (cert validation off)"; \
             curl -fSL --insecure "$AGENT_URL" -o "$AGENT_TMP" || true; }
    elif command -v wget >/dev/null 2>&1; then
      wget -nv "$AGENT_URL" -O "$AGENT_TMP" \
        || { echo "  wget failed; retrying with --no-check-certificate"; \
             wget -nv --no-check-certificate "$AGENT_URL" -O "$AGENT_TMP" || true; }
    else
      echo "  ERROR: neither curl nor wget is installed — cannot download agent"
    fi
    if [ ! -s "$AGENT_TMP" ]; then
      echo "  WARN: agent download failed — dashboard's migration tool can finish the install later (token stored at $AGENT_CFG)"
      rm -f "$AGENT_TMP"
    fi
    if [ -s "$AGENT_TMP" ]; then
      $SUDO install -m 0755 -o root -g root "$AGENT_TMP" /usr/local/bin/gopher-agent
      rm -f "$AGENT_TMP"

      # 5. Hand the client.toml to gopher so the agent can write config-push
      # directly without sudo. rathole-client (running as $SSH_USER) keeps
      # reading via mode 0644.
      $SUDO chown gopher:gopher "$CLIENT_CFG"
      $SUDO chmod 0644 "$CLIENT_CFG"

      # 6. systemd unit + service start. User=gopher, not $SSH_USER.
      $SUDO tee /etc/systemd/system/gopher-agent.service >/dev/null <<EOF || true
[Unit]
Description=Gopher Agent (control-plane back-channel)
After=network.target

[Service]
Type=simple
User=gopher
EnvironmentFile=$AGENT_CFG
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
if [ "$NO_SSH" = "1" ] || [ "$TUNNEL_PORT" = "0" ]; then
  echo "Machine '$MACHINE_NAME' registered as '$SSH_USER' (agent-only — no SSH tunnel)."
else
  echo "Machine '$MACHINE_NAME' registered as '$SSH_USER'. SSH tunnel port: $TUNNEL_PORT"
fi
echo "Rathole config: $CLIENT_CFG"
echo "Service: sudo systemctl status rathole-client"
# The dashboard detects the machine via the agent back-channel + a TCP probe of
# the tunnel — the server never SSHes back in.
echo "The dashboard will show this machine as online once its agent connects."
