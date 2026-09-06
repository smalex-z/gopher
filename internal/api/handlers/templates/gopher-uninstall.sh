#!/bin/sh
# gopher-uninstall - Gopher machine helper script (root-privileged via sudoers)
#
# Usage:
#   gopher-uninstall                              Full uninstall (removes everything)
#   gopher-uninstall --remove-tunnel  <TUNNEL_ID> Remove a tunnel from client.toml
#   gopher-uninstall --remove-machine <MACHINE_ID> Remove machine SSH section from client.toml
#
# Full-uninstall mode notifies the dashboard via POST /api/machines/self-delete
# (best-effort) before tearing down local services, so the server's machine
# list stays in sync when an operator runs this directly on the box.

if [ "$(id -u)" -eq 0 ]; then SUDO=""; else SUDO="sudo"; fi

INSTALL_PATH="/usr/local/bin/gopher-uninstall"
HOST_URL="{{.HostURL}}"

# Consolidated /etc/gopher layout with a fall back to the legacy paths, so this
# works on both migrated and un-migrated machines.
CONFIG_FILE="/etc/gopher/rathole/client.toml"
[ -f "$CONFIG_FILE" ] || CONFIG_FILE="/etc/rathole/client.toml"
VPS_KEY_FILE="/etc/gopher/rathole/vps_key.pub"
[ -f "$VPS_KEY_FILE" ] || VPS_KEY_FILE="/etc/rathole/vps_key.pub"
AGENT_CONFIG="/etc/gopher/agent/config.env"
[ -f "$AGENT_CONFIG" ] || AGENT_CONFIG="/etc/gopher-agent/config.env"

# Remove a marker-delimited section from a file.
# Usage: remove_section <file> <start_marker> <end_marker>
remove_section() {
  _file="$1"
  _start="$2"
  _end="$3"

  [ -f "$_file" ] || return 0

  # Use a distinct variable name to avoid shadowing the caller's $_tmp.
  _rs_tmp=$(mktemp)
  awk -v s="$_start" -v e="$_end" '
    $0 == s { skip=1; next }
    $0 == e { skip=0; next }
    !skip   { print }
  ' "$_file" > "$_rs_tmp"
  cat "$_rs_tmp" > "$_file" 2>/dev/null || $SUDO mv "$_rs_tmp" "$_file" 2>/dev/null || true
  rm -f "$_rs_tmp" 2>/dev/null || true
}

CMD="${1:-}"
ARG="${2:-}"

# ── Remove a single tunnel ────────────────────────────────────────────────────
if [ "$CMD" = "--remove-tunnel" ]; then
  if [ -z "$ARG" ]; then
    echo "Usage: gopher-uninstall --remove-tunnel <TUNNEL_ID>" >&2
    exit 1
  fi

  _tmp=$(mktemp)
  $SUDO cat "$CONFIG_FILE" > "$_tmp" 2>/dev/null || { echo "Config not found, nothing to do."; rm -f "$_tmp"; exit 0; }
  remove_section "$_tmp" "# gopher-tunnel-start: $ARG" "# gopher-tunnel-end: $ARG"
  $SUDO cp "$_tmp" "$CONFIG_FILE" 2>/dev/null || true
  $SUDO chown "$(stat -c '%U' "$CONFIG_FILE" 2>/dev/null || echo root)" "$CONFIG_FILE" 2>/dev/null || true
  rm -f "$_tmp"
  $SUDO systemctl restart rathole-client 2>/dev/null || true
  echo "Removed tunnel $ARG"
  exit 0
fi

# ── Remove machine SSH tunnel section ────────────────────────────────────────
if [ "$CMD" = "--remove-machine" ]; then
  if [ -z "$ARG" ]; then
    echo "Usage: gopher-uninstall --remove-machine <MACHINE_ID>" >&2
    exit 1
  fi

  _tmp=$(mktemp)
  $SUDO cat "$CONFIG_FILE" > "$_tmp" 2>/dev/null || { echo "Config not found, nothing to do."; rm -f "$_tmp"; exit 0; }
  remove_section "$_tmp" "# gopher-machine-start: $ARG" "# gopher-machine-end: $ARG"
  $SUDO cp "$_tmp" "$CONFIG_FILE" 2>/dev/null || true
  $SUDO chown "$(stat -c '%U' "$CONFIG_FILE" 2>/dev/null || echo root)" "$CONFIG_FILE" 2>/dev/null || true
  rm -f "$_tmp"
  $SUDO systemctl restart rathole-client 2>/dev/null || true
  echo "Removed machine $ARG SSH section"
  exit 0
fi

# ── Full uninstall ────────────────────────────────────────────────────────────
echo "=== Gopher Uninstall ==="

# When invoked via sudo the real user is in $SUDO_USER and $HOME points to
# /root, so resolve the actual home directory explicitly.
if [ -n "$SUDO_USER" ] && [ "$SUDO_USER" != "root" ]; then
  REAL_HOME=$(getent passwd "$SUDO_USER" | cut -d: -f6)
else
  REAL_HOME="$HOME"
fi

# ── Notify the server BEFORE tearing down local services ─────────────────────
# Pulls GOPHER_AGENT_TOKEN from the agent's env file, posts it to
# /api/machines/self-delete on the dashboard. The dashboard resolves the
# token to the machine record and deletes it, so the dashboard's machine
# list doesn't show a stale entry after a local uninstall.
#
# We loudly warn when we can't notify (rather than skipping silently) — a
# stale machine record on the dashboard is the kind of dangling state an
# operator wants to know to clean up manually. The local teardown still
# proceeds in every failure mode.
NOTIFIED=0
if [ -z "$HOST_URL" ]; then
  echo "Skipping server notification: this script was installed before HOST_URL templating (re-bootstrap or update from the dashboard to fix)."
elif [ ! -f "$AGENT_CONFIG" ]; then
  echo "Skipping server notification: $AGENT_CONFIG missing (agent not installed, or pre-agent machine)."
else
  # Read via $SUDO: the config is chmod 640 root:gopher (the bearer token is
  # deliberately not world-readable), so a plain grep run by a non-root operator
  # silently reads nothing and we'd wrongly report the token as missing.
  AGENT_TOKEN=$($SUDO grep -E '^GOPHER_AGENT_TOKEN=' "$AGENT_CONFIG" 2>/dev/null | head -1 | cut -d= -f2-)
  if [ -z "$AGENT_TOKEN" ]; then
    echo "Skipping server notification: GOPHER_AGENT_TOKEN not found in $AGENT_CONFIG."
  else
    echo "Notifying $HOST_URL that this machine is being uninstalled..."
    if command -v curl >/dev/null 2>&1; then
      if curl -fsS -X POST "$HOST_URL/api/machines/self-delete" \
           -H "Authorization: Bearer $AGENT_TOKEN" \
           --max-time 10 >/dev/null 2>&1; then
        NOTIFIED=1
        echo "  Server notified"
      else
        echo "  WARN: curl call to /api/machines/self-delete failed"
      fi
    elif command -v wget >/dev/null 2>&1; then
      if wget -q --method=POST --header="Authorization: Bearer $AGENT_TOKEN" \
           --timeout=10 "$HOST_URL/api/machines/self-delete" -O /dev/null; then
        NOTIFIED=1
        echo "  Server notified"
      else
        echo "  WARN: wget call to /api/machines/self-delete failed"
      fi
    else
      echo "  WARN: neither curl nor wget is installed — cannot notify server"
    fi
  fi
fi
if [ "$NOTIFIED" != "1" ]; then
  echo "  → Open the dashboard and remove this machine manually so its record doesn't linger."
fi

# Remove Gopher's managed SSH key(s) from authorized_keys so the server can no
# longer SSH back in. Match on the `gopher-managed` marker comment (self-healing:
# clears every managed key, current or stale) and, for older installs whose key
# predates the marker, also match the specific blob from vps_key.pub. Operator
# keys (no marker) are never touched.
#
# Two hardenings here after a QA sweep: this used to run unconditionally, so a
# failed read of $AK (root reading across an NFS-mounted $REAL_HOME with
# root_squash, an SELinux/AppArmor denial, a transient I/O error) produced an
# EMPTY $_tmp — indistinguishable from "genuinely no other keys" — which then
# got written straight over $AK with no backup, silently deleting every key
# including the operator's own. If that was their only key, this "safe,
# best-effort" uninstall locks them out of the box with no recovery path.
AK="$REAL_HOME/.ssh/authorized_keys"
if [ -f "$AK" ]; then
  if [ ! -r "$AK" ]; then
    echo "WARN: $AK exists but isn't readable — skipping SSH key cleanup rather than risk wiping it. Remove Gopher's key from it manually if needed."
  else
    _tmp=$(mktemp)
    # Back up before rewriting: this edits the file that controls SSH access
    # to this box, so if anything below goes wrong there's something to
    # restore from instead of a silent lockout. Left in place deliberately
    # (not cleaned up) — a stray backup file is a trivial cost next to that.
    _ak_backup="$AK.gopher-uninstall.bak"
    cp "$AK" "$_ak_backup" 2>/dev/null || true
    grep -v ' gopher-managed[[:space:]]*$' "$AK" > "$_tmp" 2>/dev/null
    if [ -f "$VPS_KEY_FILE" ]; then
      KEY_BLOB=$(awk '{print $2}' "$VPS_KEY_FILE" 2>/dev/null)
      if [ -n "$KEY_BLOB" ]; then
        grep -vF "$KEY_BLOB" "$_tmp" > "$_tmp.2" 2>/dev/null && mv "$_tmp.2" "$_tmp" || true
      fi
    fi
    # cat redirect preserves the file's ownership/permissions (mv under sudo would
    # chown it to root).
    cat "$_tmp" > "$AK" 2>/dev/null || mv "$_tmp" "$AK" 2>/dev/null || true
    rm -f "$_tmp" 2>/dev/null || true
    echo "Removed Gopher-managed SSH key(s) from authorized_keys (backup: $_ak_backup)"
  fi
fi

echo "Stopping gopher-agent service..."
$SUDO systemctl stop gopher-agent 2>/dev/null || true
$SUDO systemctl disable gopher-agent 2>/dev/null || true
$SUDO rm -f /etc/systemd/system/gopher-agent.service 2>/dev/null || true

echo "Stopping rathole-client service..."
$SUDO systemctl stop rathole-client 2>/dev/null || true
$SUDO systemctl disable rathole-client 2>/dev/null || true
$SUDO rm -f /etc/systemd/system/rathole-client.service 2>/dev/null || true
$SUDO systemctl daemon-reload 2>/dev/null || true

echo "Removing rathole config..."
$SUDO rm -rf /etc/gopher/rathole /etc/rathole 2>/dev/null || true

echo "Removing rathole binary..."
$SUDO rm -f /usr/local/bin/rathole 2>/dev/null || true

echo "Removing gopher-agent binary and config..."
$SUDO rm -f /usr/local/bin/gopher-agent 2>/dev/null || true
$SUDO rm -rf /etc/gopher/agent /etc/gopher-agent 2>/dev/null || true
# Drop the /etc/gopher tree if our subdirs were all it held (origins only ever
# put rathole/ + agent/ there); ignore failure if anything else remains.
$SUDO rmdir /etc/gopher 2>/dev/null || true

# Remove the dedicated gopher system user. `userdel` fails if processes are
# still owned by the user, so it must run AFTER stopping the agent service.
# Errors ignored — user may not exist on machines that pre-date the agent.
if id -u gopher >/dev/null 2>&1; then
  $SUDO userdel gopher 2>/dev/null || true
fi

# Self-destruct: remove via $SUDO so it works regardless of how the script
# was invoked (must happen BEFORE we drop /etc/sudoers.d/gopher; after that
# point the agent's gopher user can't sudo anymore).
$SUDO rm -f "$INSTALL_PATH" 2>/dev/null || true

# Drop sudoers entries last so the operations above could still use sudo -n.
$SUDO rm -f /etc/sudoers.d/gopher 2>/dev/null || true

echo "=== Gopher uninstall complete ==="
