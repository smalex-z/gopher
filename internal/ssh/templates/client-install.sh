#!/bin/bash
set -e

# Skip if rathole is already installed
if command -v rathole &>/dev/null; then
  echo "rathole already installed at $(command -v rathole), skipping download."
  exit 0
fi

RATHOLE_VERSION="__RATHOLE_VERSION__"
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  ARCH_TAG="x86_64-unknown-linux-gnu" ;;
  aarch64) ARCH_TAG="aarch64-unknown-linux-musl" ;;
  armv7l)  ARCH_TAG="armv7-unknown-linux-musleabihf" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

RATHOLE_URL="https://github.com/__RATHOLE_REPO__/releases/download/${RATHOLE_VERSION}/rathole-${ARCH_TAG}.zip"
echo "Downloading rathole from $RATHOLE_URL ..."
rm -rf /tmp/rathole-dl && mkdir -p /tmp/rathole-dl
curl -fsSL "$RATHOLE_URL" -o /tmp/rathole-dl/rathole.zip
unzip -q /tmp/rathole-dl/rathole.zip -d /tmp/rathole-dl/
mkdir -p "$HOME/.local/bin"
mv /tmp/rathole-dl/rathole "$HOME/.local/bin/rathole"
chmod +x "$HOME/.local/bin/rathole"
rm -rf /tmp/rathole-dl
echo "rathole installed successfully."
