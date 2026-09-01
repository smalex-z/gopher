#!/usr/bin/env bash
# sign-release.sh — sign a release's SHA256SUMS.txt with the OFFLINE minisign
# release key, attach the signature, and (for drafts) publish the release.
#
# Run this on the machine that holds the secret key (never the VPS, never CI),
# AFTER the release workflow finishes:
#
#   scripts/sign-release.sh v1.0.0 [path-to-secret-key]
#
# Secret key defaults to ~/.minisign/minisign.key (minisign -G's default).
#
# Flow with immutable releases (enabled on the repo): CI creates STABLE
# releases as drafts, because a published release freezes instantly — assets
# locked, no deletion — so the signature must land before publish. This script
# downloads the draft's SHA256SUMS.txt (signing exactly what CI uploaded),
# signs it, uploads the .minisig, and flips the draft live as its final step.
# Publishing is the atomic "go live": updaters never see the release unsigned.
#
# Prereleases publish directly from CI (unsigned — tolerated on alpha/beta
# channels) and freeze as-is; they cannot be signed after the fact.
set -euo pipefail

REPO="smalex-z/gopher"
TAG="${1:?usage: sign-release.sh vX.Y.Z [path-to-secret-key]}"
SECKEY="${2:-$HOME/.minisign/minisign.key}"

command -v minisign >/dev/null 2>&1 || { echo "ERROR: minisign not installed (apt install minisign / brew install minisign)" >&2; exit 1; }
command -v gh >/dev/null 2>&1 || { echo "ERROR: gh CLI not installed" >&2; exit 1; }
[ -f "$SECKEY" ] || { echo "ERROR: secret key not found at $SECKEY" >&2; exit 1; }

IS_DRAFT="$(gh release view "$TAG" --repo "$REPO" --json isDraft --jq .isDraft 2>/dev/null)" \
  || { echo "ERROR: no release found for $TAG — did the release workflow finish?" >&2; exit 1; }
if [ "$IS_DRAFT" != "true" ]; then
  echo "WARN: $TAG is already published. With immutable releases enabled, published"
  echo "      releases are frozen and the signature upload below will be rejected —"
  echo "      this only works for releases published before immutability was enabled."
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "→ Downloading SHA256SUMS.txt and binaries for $TAG (draft=$IS_DRAFT)..."
gh release download "$TAG" --repo "$REPO" --pattern "SHA256SUMS.txt" -D "$TMP"
gh release download "$TAG" --repo "$REPO" --pattern "gopher-linux-*" -D "$TMP"

# Pre-sign coherence check: recompute each downloaded binary's sha256 and
# compare against the sums file being signed. This proves sums ↔ assets are
# CONSISTENT — it cannot prove the build is honest (a compromised CI produces
# matching sums for a malicious binary; only reproducible builds would catch
# that). Its job is narrower and matters because publish is irreversible under
# immutable releases: a truncated upload or asset/sums mismatch frozen into a
# published release burns that version number forever. Catch it in the draft.
echo "→ Verifying published assets against SHA256SUMS.txt..."
FAIL=0
FOUND=0
for f in "$TMP"/gopher-linux-*; do
  [ -e "$f" ] || continue
  FOUND=1
  base="$(basename "$f")"
  want="$(awk -v n="$base" '{p=$2; sub(/^\*/,"",p); sub(/.*\//,"",p); if (p==n) print $1}' "$TMP/SHA256SUMS.txt" | head -1)"
  got="$(sha256sum "$f" | awk '{print $1}')"
  if [ -z "$want" ]; then
    echo "  ERROR: no entry for $base in SHA256SUMS.txt"; FAIL=1
  elif [ "$want" != "$got" ]; then
    echo "  MISMATCH: $base — sums say $want, asset is $got"; FAIL=1
  else
    echo "  $base ✓ $got"
  fi
done
[ "$FOUND" -eq 1 ] || { echo "ERROR: no gopher-linux-* assets found on $TAG" >&2; exit 1; }
[ "$FAIL" -eq 0 ] || { echo "ERROR: published assets do not match SHA256SUMS.txt — refusing to sign" >&2; exit 1; }

echo
echo "About to sign these checksums as \"gopher release $TAG\":"
sed 's/^/    /' "$TMP/SHA256SUMS.txt"
printf 'Proceed? [y/N] '
read -r ANSWER
case "$ANSWER" in
  y|Y|yes|YES) ;;
  *) echo "Aborted — nothing signed, nothing uploaded."; exit 1 ;;
esac

echo "→ Signing (you will be prompted for the key password)..."
minisign -S -s "$SECKEY" -m "$TMP/SHA256SUMS.txt" -t "gopher release $TAG"

# Sanity: verify the fresh signature against the pubkey embedded in the source
# tree, so a wrong key (old/backup/other project) is caught before upload.
# Anchored to the `var` declaration: the doc comment above it contains the
# literal text `ReleaseSigningPubKey = "..."`, so an unanchored grep matched
# both lines and fed minisign a garbage multi-line "key".
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EMBEDDED_PUB="$(grep -E '^var ReleaseSigningPubKey *= *"' "$ROOT/internal/build/release_signing.go" | head -1 | grep -oE '"[^"]*"' | tr -d '"')"
case "$EMBEDDED_PUB" in
  RW*)
    minisign -V -m "$TMP/SHA256SUMS.txt" -P "$EMBEDDED_PUB" >/dev/null
    echo "  signature verifies against the pubkey embedded in the source tree ✓"
    ;;
  "")
    echo "  NOTE: internal/build/release_signing.go has no pubkey set — updaters won't enforce this signature until it does."
    ;;
  *)
    echo "ERROR: extracted an implausible pubkey from release_signing.go: '$EMBEDDED_PUB'" >&2
    exit 1
    ;;
esac

echo "→ Uploading SHA256SUMS.txt.minisig..."
gh release upload "$TAG" "$TMP/SHA256SUMS.txt.minisig" --repo "$REPO" --clobber

if [ "$IS_DRAFT" = "true" ]; then
  echo "→ Publishing $TAG (release becomes immutable now)..."
  gh release edit "$TAG" --repo "$REPO" --draft=false
  echo "✓ Release $TAG signed and published."
else
  echo "✓ Signature uploaded to already-published $TAG."
fi