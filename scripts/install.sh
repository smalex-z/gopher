#!/bin/bash
# Install the latest Gopher release binary to /usr/local/bin/gopher.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/smalex-z/gopher/main/scripts/install.sh | bash
#   curl -fsSL ... | bash -s -- --prerelease   # include pre-releases

set -e

REPO="smalex-z/gopher"
INSTALL_DIR="/usr/local/bin"
BINARY_NAME="gopher"
PRERELEASE=false

for arg in "$@"; do
  case "$arg" in
    --prerelease) PRERELEASE=true ;;
    *) echo "Unknown argument: $arg" >&2; exit 1 ;;
  esac
done

# Detect architecture
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  *)
    echo "Unsupported architecture: $ARCH" >&2
    exit 1
    ;;
esac

# Require Linux
if [ "$(uname -s)" != "Linux" ]; then
  echo "Gopher only runs on Linux." >&2
  exit 1
fi

# Require curl or wget
if command -v curl &>/dev/null; then
  fetch() { curl -fsSL "$1"; }
elif command -v wget &>/dev/null; then
  fetch() { wget -qO- "$1"; }
else
  echo "curl or wget is required." >&2
  exit 1
fi

echo "→ Fetching release info from GitHub..."

if [ "$PRERELEASE" = true ]; then
  # Latest release of any kind (includes pre-releases)
  TAG=$(fetch "https://api.github.com/repos/${REPO}/releases" \
    | grep -m1 '"tag_name"' | cut -d'"' -f4)
  echo "  Using latest pre-release: $TAG"
else
  # Latest stable release only
  TAG=$(fetch "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | cut -d'"' -f4)
  echo "  Using latest stable release: $TAG"
fi

if [ -z "$TAG" ]; then
  echo "Could not determine release tag. Check https://github.com/${REPO}/releases" >&2
  exit 1
fi

ASSET="gopher-linux-${ARCH}"
URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET}"

echo "→ Downloading ${ASSET} (${TAG})..."
TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT

if command -v curl &>/dev/null; then
  curl -fsSL "$URL" -o "$TMP"
else
  wget -qO "$TMP" "$URL"
fi

chmod +x "$TMP"

echo "→ Installing to ${INSTALL_DIR}/${BINARY_NAME} (may require sudo)..."
if [ -w "$INSTALL_DIR" ]; then
  mv "$TMP" "${INSTALL_DIR}/${BINARY_NAME}"
else
  sudo mv "$TMP" "${INSTALL_DIR}/${BINARY_NAME}"
fi

echo ""
echo "✓ Gopher ${TAG} installed to ${INSTALL_DIR}/${BINARY_NAME}"
echo ""
echo "Next steps:"
echo "  sudo gopher install   # install as a systemd service"
echo "  gopher                # or run directly"
