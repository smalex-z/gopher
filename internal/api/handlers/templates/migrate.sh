#!/bin/bash
# gopher-migrate - Install the gopher-agent on an already-bootstrapped machine.
#
# Usage:
#   curl -fsSL <HOST_URL>/static/migrate.sh | sudo bash -s -- <TOKEN>
#
# Mirrors the bootstrap.sh flow: receives a one-time TOKEN, calls back to the
# server's /api/migrate endpoint to fetch the per-machine secrets, then does
# the install. No secrets ever appear in the operator's shell history or the
# server's access log.
#
# Idempotent: re-running is safe (gopher user is reused, sudoers entry is
# de-duped, rathole config block is stripped+re-added, systemd unit is
# overwritten).
set -e

HOST_URL="{{.HostURL}}"
TOKEN="$1"

if [ "$(id -u)" -ne 0 ]; then
  echo "ERROR: must run as root. Re-run via curl ... | sudo bash -s -- <TOKEN>" >&2
  exit 1
fi

if [ -z "$TOKEN" ]; then
  echo "Usage: curl -fsSL $HOST_URL/static/migrate.sh | sudo bash -s -- <TOKEN>" >&2
  echo "  (use the dashboard's Install Agent button to mint a token)" >&2
  exit 1
fi

echo "=== Gopher Agent Migration ==="

# ── 0. Resolve token → per-machine secrets via the API callback ─────────────
RESPONSE=$(curl -sS -w "\n__HTTP_STATUS__:%{http_code}" -X POST "$HOST_URL/api/migrate" \
  -H "Content-Type: application/json" \
  -d "{\"token\":\"$TOKEN\"}" 2>&1)

HTTP_STATUS=$(printf '%s' "$RESPONSE" | tail -1 | sed 's/.*__HTTP_STATUS__://')
RESPONSE=$(printf '%s' "$RESPONSE" | sed '$d' | sed 's/__HTTP_STATUS__:[0-9]*//')

if [ "$HTTP_STATUS" != "200" ]; then
  echo "ERROR: token resolution failed (HTTP $HTTP_STATUS)." >&2
  echo "Server response: $RESPONSE" >&2
  exit 1
fi

# Parse response (jq preferred, python3 fallback). Same pattern as bootstrap.sh.
_json() {
  if command -v jq >/dev/null 2>&1; then
    printf '%s\n' "$RESPONSE" | jq -r ".data.$1"
  else
    printf '%s\n' "$RESPONSE" | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['$1'])"
  fi
}

MACHINE_ID=$(_json machine_id)
AGENT_TOKEN=$(_json agent_token)
AGENT_PORT=$(_json agent_port)
RATHOLE_TOKEN=$(_json rathole_token)
NOISE_PUBKEY=$(_json noise_pubkey 2>/dev/null || echo "")
[ "$NOISE_PUBKEY" = "null" ] && NOISE_PUBKEY=""

if [ -z "$MACHINE_ID" ] || [ "$MACHINE_ID" = "null" ]; then
  echo "ERROR: invalid response from /api/migrate" >&2
  echo "$RESPONSE" >&2
  exit 1
fi

echo "Machine ID:  $MACHINE_ID"
echo "Agent port:  $AGENT_PORT"

# ── 1. Create gopher system user (idempotent) ────────────────────────────────
if ! id -u gopher >/dev/null 2>&1; then
  useradd --system --shell /usr/sbin/nologin --home-dir /nonexistent --no-create-home gopher
  echo "Created gopher system user"
fi

# ── 2. Sudoers: gopher gets NOPASSWD: ALL ───────────────────────────────────
# Industry-standard for control-plane daemons (dockerd, kubelet, etc. all
# effectively root). Running as a dedicated user gives operational symmetry
# with the server-side gopher user, file-ownership consistency, and process
# visibility — without the maintenance burden of granular sudoers updates on
# every release that adds a new privileged operation.
SUDOERS_FILE="/etc/sudoers.d/gopher"
touch "$SUDOERS_FILE"
# Strip any prior gopher-* lines, then re-add the canonical NOPASSWD: ALL.
grep -v '^gopher ' "$SUDOERS_FILE" 2>/dev/null > "$SUDOERS_FILE.tmp" || true
echo 'gopher ALL=(ALL) NOPASSWD: ALL' >> "$SUDOERS_FILE.tmp"
mv "$SUDOERS_FILE.tmp" "$SUDOERS_FILE"
chmod 0440 "$SUDOERS_FILE"

# ── 3. Download agent binary ────────────────────────────────────────────────
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)         ARCH_TAG=linux-amd64 ;;
  aarch64|arm64)  ARCH_TAG=linux-arm64 ;;
  *) echo "ERROR: unsupported arch $ARCH" >&2; exit 1 ;;
esac

if command -v curl >/dev/null 2>&1; then
  curl -fsSL --insecure "$HOST_URL/static/agents/gopher-agent-$ARCH_TAG" -o /tmp/gopher-agent.new
elif command -v wget >/dev/null 2>&1; then
  wget -q --no-check-certificate "$HOST_URL/static/agents/gopher-agent-$ARCH_TAG" -O /tmp/gopher-agent.new
else
  echo "ERROR: neither curl nor wget is available" >&2
  exit 1
fi
install -m 0755 -o root -g root /tmp/gopher-agent.new /usr/local/bin/gopher-agent
rm -f /tmp/gopher-agent.new

# ── 4. Agent config (env file consumed by EnvironmentFile=) ─────────────────
mkdir -p /etc/gopher-agent
cat > /etc/gopher-agent/config.env <<EOF
GOPHER_AGENT_TOKEN=$AGENT_TOKEN
GOPHER_AGENT_PORT=$AGENT_PORT
GOPHER_AGENT_UNIT=rathole-client.service
EOF
chmod 640 /etc/gopher-agent/config.env
chown root:gopher /etc/gopher-agent/config.env

# ── 5. Add agent back-channel to rathole client config ──────────────────────
# rathole-server already has the matching server-side block (added by the VPS
# at machine bootstrap, even on pre-agent-era machines — the schema migration
# allocated AgentRemotePort/AgentRatholeToken on every existing row). All we
# need on the client is the matching client.services entry pointing local_addr
# at our agent.
if [ -f /etc/rathole/client.toml ]; then
  awk -v start="# gopher-machine-agent-start: $MACHINE_ID" \
      -v end="# gopher-machine-agent-end: $MACHINE_ID" '
    $0 == start { skip=1; next }
    $0 == end   { skip=0; next }
    !skip       { print }
  ' /etc/rathole/client.toml > /etc/rathole/client.toml.tmp

  # Ensure the [client.transport] noise block is present. Required for any
  # machine whose original bootstrap predated the noise upgrade — without
  # this, rathole-client tries to connect plaintext to a noise-only server
  # and the tunnel never establishes. Strip any stale transport sections
  # first so re-running migrate.sh with a rotated key is idempotent.
  if [ -n "$NOISE_PUBKEY" ]; then
    awk '
      /^\[client\.transport\]/         { skip=1; next }
      /^\[client\.transport\.noise\]/  { skip=1; next }
      skip && /^\[/                    { skip=0 }
      !skip                            { print }
    ' /etc/rathole/client.toml.tmp > /etc/rathole/client.toml.tmp2
    mv /etc/rathole/client.toml.tmp2 /etc/rathole/client.toml.tmp
    cat >> /etc/rathole/client.toml.tmp <<EOF

[client.transport]
type = "noise"

[client.transport.noise]
remote_public_key = "$NOISE_PUBKEY"
EOF
  fi

  cat >> /etc/rathole/client.toml.tmp <<EOF

# gopher-machine-agent-start: $MACHINE_ID
[client.services.machine-$MACHINE_ID-agent]
type = "tcp"
token = "$RATHOLE_TOKEN"
local_addr = "127.0.0.1:$AGENT_PORT"
# gopher-machine-agent-end: $MACHINE_ID
EOF
  mv /etc/rathole/client.toml.tmp /etc/rathole/client.toml
  # Hand the file over to gopher so the agent can write it directly going
  # forward (config-push uses os.WriteFile, not sudo tee).
  chown gopher:gopher /etc/rathole/client.toml
  chmod 0644 /etc/rathole/client.toml
fi

# ── 6. systemd unit + service start ─────────────────────────────────────────
cat > /etc/systemd/system/gopher-agent.service <<EOF
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
# kill gopher-uninstall mid-cleanup.
KillMode=process
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable gopher-agent >/dev/null 2>&1 || true
systemctl restart gopher-agent

# rathole-client picks up the new client.toml via inotify (no restart needed).
# But if the unit is somehow stopped, kick it back up — start is a no-op on
# active units, no flap.
systemctl start rathole-client 2>/dev/null || true

echo ""
echo "=== Migration complete ==="
echo "Status:  systemctl status gopher-agent"
echo "Logs:    journalctl -u gopher-agent -f"
