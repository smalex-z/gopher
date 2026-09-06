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

echo "→ Ensuring jumpbox system user exists..."
# Dedicated, privilege-free user whose ~/.ssh/authorized_keys holds
# Gopher-managed keys. Created here too (not just by `gopher install`)
# so legacy deployments running scripts/reinstall.sh as their upgrade
# path get the safer config without an extra step.
JUMPBOX_USER="gopher-jump"
JUMPBOX_HOME="/var/lib/gopher-jump"
if ! id -u "$JUMPBOX_USER" >/dev/null 2>&1; then
  sudo useradd --system --shell /usr/sbin/nologin --home-dir "$JUMPBOX_HOME" --create-home "$JUMPBOX_USER"
  echo "  Created system user $JUMPBOX_USER (home $JUMPBOX_HOME)"
fi
sudo install -d -m 0700 -o "$JUMPBOX_USER" -g "$JUMPBOX_USER" "$JUMPBOX_HOME/.ssh"
echo "✓ Jumpbox user ready"

# Ensure gopher.service opts into supervising the bundled caddy/rathole. This is
# the upgrade path for existing installs whose unit predates the embedded model;
# without GOPHER_MANAGED the new binary stays inert (no supervision/migration).
echo "→ Ensuring gopher.service has GOPHER_MANAGED..."
UNIT="/etc/systemd/system/$SERVICE.service"
if [ -f "$UNIT" ] && ! sudo grep -q "GOPHER_MANAGED=1" "$UNIT"; then
  sudo sed -i '/^\[Service\]/a Environment=GOPHER_MANAGED=1' "$UNIT"
  echo "  Added Environment=GOPHER_MANAGED=1 to $UNIT"
else
  echo "✓ GOPHER_MANAGED already set (or unit absent)"
fi

echo "→ Reloading systemd units..."
sudo systemctl daemon-reload

echo "→ Stopping $SERVICE service..."
sudo systemctl stop "$SERVICE"

echo "→ Replacing binary at $INSTALL_BIN..."
sudo cp "$ROOT/gopher" "$INSTALL_BIN"

# The supervisor extracts the bundled caddy/rathole here at startup; gopher runs
# as the unprivileged 'gopher' user, so the dir must be gopher-owned.
echo "→ Ensuring /opt/gopher/bin is writable by gopher..."
sudo install -d -o gopher -g gopher -m 0755 /opt/gopher/bin

echo "→ Starting $SERVICE service..."
sudo systemctl start "$SERVICE"

echo "✓ Reinstall complete. Status:"
sudo systemctl status "$SERVICE" --no-pager -l
