#!/usr/bin/env bash
# sign-release.sh — sign a published release's SHA256SUMS.txt with the OFFLINE
# minisign release key and upload the detached signature to the release.
#
# Run this on the machine that holds the secret key (never the VPS, never CI),
# AFTER the release workflow has published the binaries:
#
#   scripts/sign-release.sh v1.0.0 [path-to-secret-key]
#
# Secret key defaults to ~/.minisign/minisign.key (minisign -G's default).
# The script downloads the PUBLISHED SHA256SUMS.txt (so you sign exactly what
# is up on GitHub — a CI-tampered file would be caught here by eyeball or by
# the embedded-pubkey sanity verify below), signs it, and uploads the
# .minisig. Updaters with build.ReleaseSigningPubKey set refuse unsigned
# stable releases, so for stable this step is mandatory.
set -euo pipefail

REPO="smalex-z/gopher"
TAG="${1:?usage: sign-release.sh vX.Y.Z [path-to-secret-key]}"
SECKEY="${2:-$HOME/.minisign/minisign.key}"

command -v minisign >/dev/null 2>&1 || { echo "ERROR: minisign not installed (apt install minisign / brew install minisign)" >&2; exit 1; }
command -v gh >/dev/null 2>&1 || { echo "ERROR: gh CLI not installed" >&2; exit 1; }
[ -f "$SECKEY" ] || { echo "ERROR: secret key not found at $SECKEY" >&2; exit 1; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "→ Downloading published SHA256SUMS.txt for $TAG..."
gh release download "$TAG" --repo "$REPO" --pattern "SHA256SUMS.txt" -D "$TMP"

echo "→ Signing (you will be prompted for the key password)..."
minisign -S -s "$SECKEY" -m "$TMP/SHA256SUMS.txt" -t "gopher release $TAG"

# Sanity: verify the fresh signature against the pubkey embedded in the source
# tree, so a wrong key (old/backup/other project) is caught before upload.
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EMBEDDED_PUB="$(grep -E 'ReleaseSigningPubKey *=' "$ROOT/internal/build/release_signing.go" | grep -oE '"[^"]*"' | tr -d '"')"
if [ -n "$EMBEDDED_PUB" ]; then
  minisign -V -m "$TMP/SHA256SUMS.txt" -P "$EMBEDDED_PUB" >/dev/null
  echo "  signature verifies against the pubkey embedded in the source tree ✓"
else
  echo "  NOTE: internal/build/release_signing.go has no pubkey set yet — updaters won't enforce this signature until it does."
fi

echo "→ Uploading SHA256SUMS.txt.minisig to $TAG..."
gh release upload "$TAG" "$TMP/SHA256SUMS.txt.minisig" --repo "$REPO" --clobber

echo "✓ Release $TAG signed."
