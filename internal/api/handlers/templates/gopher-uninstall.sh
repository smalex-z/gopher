#!/bin/sh
# gopher-uninstall - Gopher machine helper script (root-privileged via sudoers)
#
# Usage:
#   gopher-uninstall                                       Full uninstall (removes everything)
#   gopher-uninstall --remove-tunnel  <TUNNEL_ID>          Remove a tunnel from client.toml
#   gopher-uninstall --remove-machine <MACHINE_ID>         Remove machine SSH section from client.toml
#   gopher-uninstall --install-agent  <URL> <TOKEN> <PORT> <USER>
#                                                          Install gopher-agent on this machine
#
# Invoked via `sudo -n /usr/local/bin/gopher-uninstall ...` and runs as root.
# The narrow sudoers rule the bootstrap installs lets the SSH user execute
# this binary without a password, so the dashboard can drive privileged
# operations through it without granting NOPASSWD on broader commands like
# `install`, `tee`, `mkdir`, or `systemctl`.

if [ "$(id -u)" -eq 0 ]; then SUDO=""; else SUDO="sudo"; fi

CONFIG_FILE="/etc/rathole/client.toml"
VPS_KEY_FILE="/etc/rathole/vps_key.pub"
INSTALL_PATH="/usr/local/bin/gopher-uninstall"

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

# Remove the VPS SSH public key from authorized_keys so the server can no
# longer SSH back into this machine.
if [ -f "$VPS_KEY_FILE" ]; then
  VPS_KEY=$(cat "$VPS_KEY_FILE" 2>/dev/null)
  if [ -n "$VPS_KEY" ]; then
    KEY_BLOB=$(echo "$VPS_KEY" | awk '{print $2}')
    AK="$REAL_HOME/.ssh/authorized_keys"
    if [ -n "$KEY_BLOB" ] && [ -f "$AK" ]; then
      _tmp=$(mktemp)
      grep -v "$KEY_BLOB" "$AK" > "$_tmp" 2>/dev/null || true
      # Use cat redirect to preserve the original file's ownership/permissions.
      # mv would change ownership to root when run via sudo.
      cat "$_tmp" > "$AK" 2>/dev/null || mv "$_tmp" "$AK" 2>/dev/null || true
      rm -f "$_tmp" 2>/dev/null || true
      echo "Removed VPS SSH key from authorized_keys"
    fi
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
$SUDO rm -rf /etc/rathole 2>/dev/null || true

echo "Removing rathole binary..."
$SUDO rm -f /usr/local/bin/rathole 2>/dev/null || true

echo "Stopping user-mode gopher-agent (if any)..."
# User-mode installs live entirely in $REAL_HOME — kill the running process,
# strip cron rules, remove files. Cron-strip runs as the user (we're root,
# so we need sudo -u to drop down) since `crontab -` modifies the calling
# user's crontab.
pkill -f "$REAL_HOME/bin/gopher-agent" 2>/dev/null || true
if [ -n "$SUDO_USER" ] && [ "$SUDO_USER" != "root" ]; then
  sudo -u "$SUDO_USER" sh -c '(crontab -l 2>/dev/null | grep -v "gopher-agent" || true) | crontab -' 2>/dev/null || true
fi

echo "Removing gopher-agent binary and config..."
$SUDO rm -rf /etc/gopher-agent 2>/dev/null || true
$SUDO rm -f /usr/local/bin/gopher-agent 2>/dev/null || true
rm -f "$REAL_HOME/bin/gopher-agent" 2>/dev/null || true
rm -rf "$REAL_HOME/.config/gopher-agent" 2>/dev/null || true
rm -f "$REAL_HOME/.cache/gopher-agent.log" 2>/dev/null || true

# Drop sudoers entries last so the operations above could still use sudo -n.
$SUDO rm -f /etc/sudoers.d/gopher 2>/dev/null || true

# Self-destruct last so the script can finish cleanly.
rm -f "$INSTALL_PATH" 2>/dev/null || true

echo "=== Gopher uninstall complete ==="
