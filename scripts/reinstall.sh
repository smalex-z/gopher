#!/bin/bash
# Rebuild the binary and hot-swap it into the running systemd service.
set -e

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
INSTALL_BIN="/opt/gopher/gopher"
SERVICE="gopher"

echo "→ Building..."
bash "$ROOT/scripts/build.sh"

echo "→ Patching sudoers for gopher service user..."
SUDOERS_FILE="/etc/sudoers.d/gopher"
for cmd in /usr/sbin/iptables /sbin/iptables /usr/sbin/iptables-save /sbin/iptables-save /usr/sbin/iptables-restore /sbin/iptables-restore /usr/sbin/ufw /usr/bin/ufw /usr/bin/fail2ban-client /usr/local/bin/fail2ban-client; do
  if [ -f "$cmd" ] && ! sudo grep -q "$cmd" "$SUDOERS_FILE" 2>/dev/null; then
    echo "gopher ALL=(ALL:ALL) NOPASSWD: $cmd" | sudo tee -a "$SUDOERS_FILE" > /dev/null
  fi
done
echo "✓ Sudoers up to date"

echo "→ Reloading systemd units..."
sudo systemctl daemon-reload

echo "→ Stopping $SERVICE service..."
sudo systemctl stop "$SERVICE"

echo "→ Replacing binary at $INSTALL_BIN..."
sudo cp "$ROOT/gopher" "$INSTALL_BIN"

echo "→ Starting $SERVICE service..."
sudo systemctl start "$SERVICE"

echo "✓ Reinstall complete. Status:"
sudo systemctl status "$SERVICE" --no-pager -l
