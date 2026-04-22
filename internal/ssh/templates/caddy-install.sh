#!/bin/bash
# Install Caddy server

set -euo pipefail

if [ "$(id -u)" -eq 0 ]; then SUDO=""; else SUDO="sudo"; fi

echo "Installing Caddy..."

# Detect OS and architecture
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
  x86_64) ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
esac

# Download latest Caddy release
LATEST_RELEASE=$(curl -s https://api.github.com/repos/caddyserver/caddy/releases/latest | grep -oP '"tag_name": "\K[^"]*')
DOWNLOAD_URL="https://github.com/caddyserver/caddy/releases/download/${LATEST_RELEASE}/caddy_${LATEST_RELEASE#v}_${OS}_${ARCH}.tar.gz"

echo "Downloading Caddy from ${DOWNLOAD_URL}..."
cd /tmp
curl -L -o caddy.tar.gz "$DOWNLOAD_URL"
tar xzf caddy.tar.gz caddy
rm caddy.tar.gz

# Move to binary location
$SUDO mv caddy /usr/local/bin/caddy
$SUDO chmod +x /usr/local/bin/caddy

# Create caddy user/group if it doesn't exist
$SUDO useradd --system --home /var/lib/caddy --shell /bin/false caddy 2>/dev/null || true

echo "Caddy installed successfully"
/usr/local/bin/caddy version
