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
# The script downloads the PUBLISHED SHA256SUMS.txt and every other asset in
# the release, recomputes each asset's sha256 locally, and refuses to sign on
# any mismatch — so the key never rubber-stamps whatever a compromised
# pipeline uploaded. Both listings are printed and you confirm before the key
# password prompt. The trusted comment is "gopher release <tag>", which
# updaters REQUIRE to match the tag they are installing (rollback-replay
# protection) — do not sign with another tool or a different comment.
# Updaters with build.ReleaseSigningPubKey set refuse unsigned stable
# releases, so for stable this step is mandatory.
set -euo pipefail

REPO="smalex-z/gopher"
TAG="${1:?usage: sign-release.sh vX.Y.Z [path-to-secret-key]}"
SECKEY="${2:-$HOME/.minisign/minisign.key}"

command -v minisign >/dev/null 2>&1 || { echo "ERROR: minisign not installed (apt install minisign / brew install minisign)" >&2; exit 1; }
command -v gh >/dev/null 2>&1 || { echo "ERROR: gh CLI not installed" >&2; exit 1; }
[ -f "$SECKEY" ] || { echo "ERROR: secret key not found at $SECKEY" >&2; exit 1; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "→ Downloading all published assets for $TAG..."
gh release download "$TAG" --repo "$REPO" -D "$TMP"
[ -f "$TMP/SHA256SUMS.txt" ] || { echo "ERROR: release $TAG has no SHA256SUMS.txt asset" >&2; exit 1; }

# Every entry in the published sums file must match a hash we compute from the
# asset bytes ourselves; a mismatch or missing asset means the release was
# tampered with (or is mid-upload) and must not be signed. Note this proves
# sums↔assets consistency, not that the assets are the ones CI built — that
# is what your eyes on the listing below are for.
echo
echo "Published SHA256SUMS.txt:"
sed 's/^/    /' "$TMP/SHA256SUMS.txt"
echo
echo "Recomputed locally from the downloaded assets:"
while read -r want name; do
  name="${name#\*}"
  base="${name##*/}" # sums entries carry the CI build path (dist/...); assets are basenames
  [ -f "$TMP/$base" ] || { echo "ERROR: $name is listed in SHA256SUMS.txt but not published in the release" >&2; exit 1; }
  got="$(sha256sum "$TMP/$base" | awk '{print $1}')"
  echo "    $got  $base"
  [ "$got" = "$want" ] || { echo "ERROR: hash mismatch for $name — refusing to sign a tampered release" >&2; exit 1; }
done < <(grep -vE '^[[:space:]]*(#|$)' "$TMP/SHA256SUMS.txt")
echo
echo "  every published hash matches the assets ✓"
echo
read -r -p "Sign these sums as \"gopher release $TAG\"? [y/N] " REPLY
case "$REPLY" in [Yy]) ;; *) echo "aborted — nothing signed or uploaded"; exit 1 ;; esac

echo "→ Signing (you will be prompted for the key password)..."
minisign -S -s "$SECKEY" -m "$TMP/SHA256SUMS.txt" -t "gopher release $TAG"

# Sanity: verify the fresh signature against the pubkey embedded in the source
# tree, so a wrong key (old/backup/other project) is caught before upload —
# stable edges would otherwise refuse the release and auto-update bricks for
# this tag. Failing to LOCATE the assignment is a hard error, never a skip: a
# refactor that breaks this extraction must break the script, not the check.
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GO_FILE="$ROOT/internal/build/release_signing.go"
PUB_LINE="$(grep -E 'ReleaseSigningPubKey[[:space:]]*=' "$GO_FILE" | head -1)"
[ -n "$PUB_LINE" ] || { echo "ERROR: could not find the ReleaseSigningPubKey assignment in $GO_FILE — fix this extraction before signing" >&2; exit 1; }
EMBEDDED_PUB="$(printf '%s\n' "$PUB_LINE" | grep -oE '"[^"]*"' | head -1 | tr -d '"')"
if [ -n "$EMBEDDED_PUB" ]; then
  minisign -V -m "$TMP/SHA256SUMS.txt" -P "$EMBEDDED_PUB" >/dev/null
  echo "  signature verifies against the pubkey embedded in the source tree ✓"
else
  echo "  NOTE: $GO_FILE has no pubkey set yet — updaters won't enforce this signature until it does."
fi

echo "→ Uploading SHA256SUMS.txt.minisig to $TAG..."
gh release upload "$TAG" "$TMP/SHA256SUMS.txt.minisig" --repo "$REPO" --clobber

echo "✓ Release $TAG signed."
