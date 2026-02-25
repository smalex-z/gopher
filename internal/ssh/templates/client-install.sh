#!/bin/bash
set -e
RATHOLE_VERSION="v0.5.0"
ARCH=$(uname -m)
if [ "$ARCH" = "x86_64" ]; then
    ARCH="x86_64"
elif [ "$ARCH" = "aarch64" ]; then
    ARCH="aarch64"
fi
curl -Lo /tmp/rathole.tar.gz "https://github.com/rapiz1/rathole/releases/download/${RATHOLE_VERSION}/rathole-${ARCH}-unknown-linux-musl.tar.gz"
tar -xzf /tmp/rathole.tar.gz -C /tmp
mv /tmp/rathole /usr/local/bin/rathole
chmod +x /usr/local/bin/rathole
mkdir -p /etc/rathole
