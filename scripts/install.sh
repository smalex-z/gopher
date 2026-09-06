#!/bin/bash
# Install the latest Gopher release binary to /usr/local/bin/gopher.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/smalex-z/gopher/main/scripts/install.sh | bash
#   curl -fsSL ... | bash -s -- --prerelease          # include pre-releases
#   curl -fsSL ... | bash -s -- --version v0.1.0-rc.1 # pin an exact tag
#   GOPHER_VERSION=v0.1.0-rc.1 curl -fsSL ... | bash  # same, via env
#
# Version pinning exists for rc soak testing: /releases/latest excludes
# prereleases, so an rc can only be installed by naming it.

set -euo pipefail

REPO="smalex-z/gopher"
INSTALL_DIR="/usr/local/bin"
BINARY_NAME="gopher"
PRERELEASE=false
PINNED="${GOPHER_VERSION:-}"

while [ $# -gt 0 ]; do
  case "$1" in
    --prerelease) PRERELEASE=true ;;
    --version)
      shift
      [ $# -gt 0 ] || { echo "--version requires a tag argument (e.g. --version v0.1.0)" >&2; exit 1; }
      PINNED="$1"
      ;;
    --version=*) PINNED="${1#--version=}" ;;
    *) echo "Unknown argument: $1" >&2; exit 1 ;;
  esac
  shift
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
  fetch_to() { curl -fsSL "$1" -o "$2"; }
elif command -v wget &>/dev/null; then
  fetch() { wget -qO- "$1"; }
  fetch_to() { wget -qO "$2" "$1"; }
else
  echo "curl or wget is required." >&2
  exit 1
fi

if [ -n "$PINNED" ]; then
  TAG="$PINNED"
  echo "→ Using pinned version: $TAG"
elif [ "$PRERELEASE" = true ]; then
  echo "→ Fetching release info from GitHub..."
  # Latest release of any kind (includes pre-releases). per_page=1 limits the
  # response to a single release object, so a plain grep (no -m1) can't race
  # curl's write with an early pipe close — piping into `grep -m1`/`head -n1`
  # here intermittently killed curl with "curl: (23) Failure writing output
  # to destination" (SIGPIPE from the reader exiting before curl finished
  # writing the full multi-release response).
  TAG=$(fetch "https://api.github.com/repos/${REPO}/releases?per_page=1" \
    | grep '"tag_name"' | cut -d'"' -f4)
  echo "  Using latest pre-release: $TAG"
else
  echo "→ Fetching release info from GitHub..."
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
BASE="https://github.com/${REPO}/releases/download/${TAG}"

echo "→ Downloading ${ASSET} (${TAG})..."
TMP=$(mktemp)
SUMS=$(mktemp)
trap 'rm -f "$TMP" "$SUMS"' EXIT
fetch_to "${BASE}/${ASSET}" "$TMP"

# Verify against the release's published checksums — same integrity bar the
# in-app updater applies. Every release ships SHA256SUMS.txt (CI enforces it),
# so a missing/mismatching checksum means a corrupt or tampered download.
echo "→ Verifying checksum..."
fetch_to "${BASE}/SHA256SUMS.txt" "$SUMS"
EXPECTED=$(grep "dist/${ASSET}\$" "$SUMS" | cut -d' ' -f1)
if [ -z "$EXPECTED" ]; then
  # Older releases listed bare asset names; accept either layout.
  EXPECTED=$(grep -E "(^| |/)${ASSET}\$" "$SUMS" | head -n1 | cut -d' ' -f1)
fi
if [ -z "$EXPECTED" ]; then
  echo "No checksum for ${ASSET} in SHA256SUMS.txt — refusing to install." >&2
  exit 1
fi
ACTUAL=$(sha256sum "$TMP" | cut -d' ' -f1)
if [ "$EXPECTED" != "$ACTUAL" ]; then
  echo "Checksum mismatch for ${ASSET}:" >&2
  echo "  expected: $EXPECTED" >&2
  echo "  actual:   $ACTUAL" >&2
  echo "Refusing to install a corrupt or tampered binary." >&2
  exit 1
fi
echo "  Checksum OK."

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
echo ""
echo "On a cloud VPS (OCI, AWS, etc.), also open TCP 80/443/2333 in the"
echo "provider's own firewall (VCN Security List on OCI, Security Group on"
echo "AWS) — that's a separate layer in front of the OS and Gopher can't"
echo "manage it. See: https://github.com/${REPO}#cloud-level-firewall--security-groups"
