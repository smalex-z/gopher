#!/bin/bash
# Install Rathole

set -euo pipefail

echo "Installing Rathole..."

# Detect OS and architecture
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
  x86_64) ARCH="x86_64" ;;
  aarch64) ARCH="aarch64" ;;
esac

# rathole releases use 'linux' not 'linux-gnu'
[[ "$OS" == "linux" ]] && OS_TAG="linux"

# Download latest rathole release
LATEST_RELEASE=$(curl -s https://api.github.com/repos/rapiz1/rathole/releases/latest | grep -oP '"tag_name": "\K[^"]*')
DOWNLOAD_URL="https://github.com/rapiz1/rathole/releases/download/${LATEST_RELEASE}/rathole-${LATEST_RELEASE#v}.${OS_TAG}-${ARCH}.zip"

echo "Downloading Rathole from ${DOWNLOAD_URL}..."
cd /tmp
curl -L -o rathole.zip "$DOWNLOAD_URL"
unzip -q rathole.zip
rm rathole.zip

# Move to binary location
sudo mv rathole /usr/local/bin/rathole
sudo chmod +x /usr/local/bin/rathole

echo "Rathole installed successfully"
/usr/local/bin/rathole --version
