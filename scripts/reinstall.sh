#!/bin/bash
# Rebuild the binary and hot-swap it into the running systemd service.
set -e

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
INSTALL_BIN="/opt/gopher/gopher"
SERVICE="gopher"

echo "→ Building..."
bash "$ROOT/scripts/build.sh"

echo "→ Stopping $SERVICE service..."
sudo systemctl stop "$SERVICE"

echo "→ Replacing binary at $INSTALL_BIN..."
sudo cp "$ROOT/gopher" "$INSTALL_BIN"

echo "→ Starting $SERVICE service..."
sudo systemctl start "$SERVICE"

echo "✓ Reinstall complete. Status:"
sudo systemctl status "$SERVICE" --no-pager -l
