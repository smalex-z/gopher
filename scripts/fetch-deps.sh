#!/usr/bin/env bash
# Fetches the external binaries Gopher embeds, into internal/embedbin/bin/
# (gitignored). Run before a release/deploy `go build` so go:embed picks up the
# real binaries (dev builds work without it — see internal/embedbin). Versions are read
# from internal/build/versions.go so this stays in sync with the Go source of
# truth.
#
#   Caddy   — matching edge arch only (amd64 OR arm64), since only the edge runs it.
#   rathole — every origin arch (x86_64, aarch64, armv7): the edge runs its own
#             and serves the rest to origins.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$REPO_ROOT/internal/embedbin/bin"
VERS="$REPO_ROOT/internal/build/versions.go"

ver() { grep -E "$1 *=" "$VERS" | grep -oE '"[^"]+"' | head -1 | tr -d '"'; }
CADDY_VERSION="$(ver CaddyVersion)"
RATHOLE_VERSION="$(ver RatholeVersion)"
RATHOLE_REPO="$(ver RatholeRepo)"
GOARCH="${GOARCH:-$(go env GOARCH)}"

# verify_sha256 <file> <expected> <label> — refuse to stage an artifact whose
# content doesn't match the pin in versions.go. Version-pinned URLs freeze the
# address, not the bytes; this is what actually stops a compromised upstream
# release asset from being embedded into a gopher build.
verify_sha256() {
  local got
  got="$(sha256sum "$1" | awk '{print $1}')"
  if [ "$got" != "$2" ]; then
    echo "fetch-deps: SHA256 MISMATCH for $3" >&2
    echo "  expected: $2" >&2
    echo "  got:      $got" >&2
    echo "  Refusing to stage. If the upstream version was deliberately bumped," >&2
    echo "  update the pinned hash in internal/build/versions.go alongside it." >&2
    exit 1
  fi
}

mkdir -p "$BIN"

case "$GOARCH" in
  amd64) CADDY_ARCH=amd64; CADDY_SHA="$(ver CaddySHA256AMD64)" ;;
  arm64) CADDY_ARCH=arm64; CADDY_SHA="$(ver CaddySHA256ARM64)" ;;
  *) echo "fetch-deps: unsupported edge GOARCH=$GOARCH (edges are amd64 or arm64)" >&2; exit 1 ;;
esac

# Idempotent: skip the downloads when the staged binaries already match the
# pinned versions + arch (so build.sh/reinstall.sh don't refetch ~40MB every
# build). A version bump in versions.go changes the stamp and forces a refetch.
STAMP="$BIN/.versions"
WANT="caddy=${CADDY_VERSION}/${CADDY_ARCH} rathole=${RATHOLE_VERSION}"
if [ -f "$BIN/caddy" ] && [ -f "$BIN/rathole-x86_64" ] && [ -f "$BIN/rathole-aarch64" ] && \
   [ -f "$BIN/rathole-armv7" ] && [ "$(cat "$STAMP" 2>/dev/null)" = "$WANT" ]; then
  echo "fetch-deps: caddy ${CADDY_VERSION} + rathole ${RATHOLE_VERSION} already staged — skipping"
  exit 0
fi

echo "Fetching caddy ${CADDY_VERSION} (linux/${CADDY_ARCH})..."
CADDY_TMP="$(mktemp -d)"
curl -fsSL "https://github.com/caddyserver/caddy/releases/download/v${CADDY_VERSION}/caddy_${CADDY_VERSION}_linux_${CADDY_ARCH}.tar.gz" \
  -o "$CADDY_TMP/caddy.tar.gz"
verify_sha256 "$CADDY_TMP/caddy.tar.gz" "$CADDY_SHA" "caddy ${CADDY_VERSION} linux/${CADDY_ARCH}"
tar -xzf "$CADDY_TMP/caddy.tar.gz" -C "$BIN" caddy
rm -rf "$CADDY_TMP"
chmod +x "$BIN/caddy"

fetch_rathole() {
  local tag="$1" out="$2" sha="$3" tmp
  echo "Fetching rathole ${RATHOLE_VERSION} (${tag})..."
  tmp="$(mktemp -d)"
  curl -fsSL "https://github.com/${RATHOLE_REPO}/releases/download/${RATHOLE_VERSION}/rathole-${tag}.zip" -o "$tmp/r.zip"
  verify_sha256 "$tmp/r.zip" "$sha" "rathole ${RATHOLE_VERSION} ${tag}"
  unzip -oq "$tmp/r.zip" -d "$tmp"
  mv "$tmp/rathole" "$BIN/$out"
  chmod +x "$BIN/$out"
  rm -rf "$tmp"
}
fetch_rathole "x86_64-unknown-linux-gnu"       "rathole-x86_64"  "$(ver RatholeSHA256X8664)"
fetch_rathole "aarch64-unknown-linux-musl"     "rathole-aarch64" "$(ver RatholeSHA256AARCH64)"
fetch_rathole "armv7-unknown-linux-musleabihf" "rathole-armv7"   "$(ver RatholeSHA256ARMV7)"

echo "$WANT" > "$STAMP"
echo "Done. Embedded binaries staged in $BIN"
