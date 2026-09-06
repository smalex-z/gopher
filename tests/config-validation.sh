#!/bin/bash
# tests/config-validation.sh
# Tests that config generation produces valid, well-formed output.
# Run from the repository root: ./tests/config-validation.sh

set -euo pipefail

GOPHER_PORT=8181
GOPHER_DB="test-config.db"
COOKIE_JAR=""
# shellcheck disable=SC2034  # set here, consumed by sourced lib.sh
GOPHER_PID=""
GOPHER_LOG=""

YELLOW='\033[1;33m'

# shellcheck disable=SC1091
source "$(dirname "$0")/lib.sh"

skip() { echo -e "${YELLOW}⚠️  $1${NC}"; }

trap cleanup_gopher_artefacts EXIT

echo "🧪 Config Validation Tests"
echo "=========================="

# ── Preflight ──────────────────────────────────────────────────────────────────
if [[ ! -x ./gopher ]]; then
    fail "gopher binary not found. Run ./scripts/build.sh first."
fi
command -v jq >/dev/null 2>&1 || fail "jq is required but not installed"
command -v sqlite3 >/dev/null 2>&1 || fail "sqlite3 is required but not installed"

COOKIE_JAR=$(mktemp /tmp/gopher-cookies.XXXXX)
# shellcheck disable=SC2034  # set here, consumed by sourced lib.sh
GOPHER_LOG=$(mktemp /tmp/gopher-stderr.XXXXX)

# ── Start Server ───────────────────────────────────────────────────────────────
echo ""
echo "1. Starting Gopher on port $GOPHER_PORT..."
start_gopher_with_retry

# ── Auth ───────────────────────────────────────────────────────────────────────
echo ""
echo "2. Setting up auth..."
RESP=$(curl -sf -c "$COOKIE_JAR" \
    -X POST "http://localhost:$GOPHER_PORT/api/auth/setup" \
    -H "Content-Type: application/json" \
    -d '{"password":"gopher-test-pass"}')
echo "$RESP" | jq -e '.success == true' >/dev/null \
    || fail "Auth setup failed — response: $RESP"
pass "Auth configured"

# ── Set Domain directly ───────────────────────────────────────────────────────
# The POST /api/local/skip endpoint was removed with the setup-wizard skip flow;
# the domain is now set only during the full install, which can't run in CI.
# Write it straight to the settings row (created by the auth-setup step above) —
# the server reads AppSettings fresh from the DB on every config operation, so a
# direct update is picked up. busy_timeout waits out the running server's writer.
echo ""
echo "3. Configuring domain (example.com)..."
sqlite3 "$GOPHER_DB" "PRAGMA busy_timeout=5000; UPDATE app_settings SET domain='example.com' WHERE id='singleton';"
DOMAIN_SET=$(sqlite3 "$GOPHER_DB" "SELECT domain FROM app_settings WHERE id='singleton';")
[[ "$DOMAIN_SET" == "example.com" ]] || fail "Domain set failed — got: '$DOMAIN_SET'"
pass "Domain set to example.com"

# ── Create Machine + Tunnel ───────────────────────────────────────────────────
echo ""
echo "4. Creating test data (machine + tunnel)..."
RESP=$(curl -sf -b "$COOKIE_JAR" \
    -X POST "http://localhost:$GOPHER_PORT/api/machines" \
    -H "Content-Type: application/json" \
    -d '{"name":"cfg-machine","username":"ubuntu"}')
MACHINE_ID=$(echo "$RESP" | jq -r '.data.id // empty')
[[ -n "$MACHINE_ID" ]] || fail "Machine creation failed — response: $RESP"

RESP=$(curl -sf -b "$COOKIE_JAR" \
    -X POST "http://localhost:$GOPHER_PORT/api/tunnels" \
    -H "Content-Type: application/json" \
    -d "{\"machine_id\":\"$MACHINE_ID\",\"name\":\"myapp\",\"subdomain\":\"myapp\",\"local_port\":3000}")
TUNNEL_ID=$(echo "$RESP" | jq -r '.data.id // empty')
[[ -n "$TUNNEL_ID" ]] || fail "Tunnel creation failed — response: $RESP"
RATHOLE_PORT=$(echo "$RESP" | jq -r '.data.rathole_port')
pass "Test data created (machine: $MACHINE_ID, tunnel: $TUNNEL_ID, rathole_port: $RATHOLE_PORT)"

# ── Caddyfile Test ─────────────────────────────────────────────────────────────
echo ""
echo "5. Testing Caddyfile generation..."
CADDYFILE=$(curl -sf -b "$COOKIE_JAR" \
    "http://localhost:$GOPHER_PORT/api/debug/caddyfile")

echo "$CADDYFILE" | grep -q "example.com" \
    || fail "Caddyfile missing domain 'example.com'"
pass "Caddyfile contains domain"

echo "$CADDYFILE" | grep -q "myapp.example.com" \
    || fail "Caddyfile missing subdomain block 'myapp.example.com'"
pass "Caddyfile contains tunnel subdomain block"

echo "$CADDYFILE" | grep -q "reverse_proxy localhost:$RATHOLE_PORT" \
    || fail "Caddyfile missing reverse_proxy for rathole port $RATHOLE_PORT"
pass "Caddyfile reverse_proxy points to correct rathole port"

echo "$CADDYFILE" | grep -q "BEGIN CUSTOM CONFIGURATION" \
    || fail "Caddyfile missing custom configuration sentinel"
pass "Caddyfile has custom configuration sentinel"

# Optional: validate with caddy. Prefer the bundled binary the edge supervises
# (/opt/gopher/bin/caddy); fall back to a caddy on PATH for legacy installs.
CADDY_BIN=$(command -v caddy 2>/dev/null || true)
[ -z "$CADDY_BIN" ] && [ -x /opt/gopher/bin/caddy ] && CADDY_BIN=/opt/gopher/bin/caddy
if [ -n "$CADDY_BIN" ]; then
    TMPFILE=$(mktemp /tmp/test-XXXXX.Caddyfile)
    echo "$CADDYFILE" > "$TMPFILE"
    if "$CADDY_BIN" validate --config "$TMPFILE" --adapter caddyfile >/dev/null 2>&1; then
        pass "Caddyfile syntax valid (caddy validate)"
    else
        fail "Caddyfile syntax invalid (caddy validate)"
    fi
    rm -f "$TMPFILE"
else
    skip "caddy binary not found — skipping syntax validation"
fi

# ── Rathole Server Config Test ────────────────────────────────────────────────
echo ""
echo "6. Testing rathole server config generation..."
RATHOLE_CFG=$(curl -sf -b "$COOKIE_JAR" \
    "http://localhost:$GOPHER_PORT/api/debug/rathole-server")

echo "$RATHOLE_CFG" | grep -q "\[server\]" \
    || fail "Rathole config missing [server] section"
pass "Rathole config has [server] section"

echo "$RATHOLE_CFG" | grep -q "bind_addr" \
    || fail "Rathole config missing bind_addr"
pass "Rathole config has bind_addr"

echo "$RATHOLE_CFG" | grep -q "tunnel-$TUNNEL_ID" \
    || fail "Rathole config missing tunnel entry for $TUNNEL_ID"
pass "Rathole config contains tunnel entry"

echo "$RATHOLE_CFG" | grep -q "$RATHOLE_PORT" \
    || fail "Rathole config missing rathole port $RATHOLE_PORT"
pass "Rathole config has correct port"

echo "$RATHOLE_CFG" | grep -q "BEGIN CUSTOM CONFIGURATION" \
    || fail "Rathole config missing custom configuration sentinel"
pass "Rathole config has custom configuration sentinel"

# ── Multiple Tunnel Config Test ───────────────────────────────────────────────
echo ""
echo "7. Testing config with multiple tunnels..."
RESP=$(curl -sf -b "$COOKIE_JAR" \
    -X POST "http://localhost:$GOPHER_PORT/api/tunnels" \
    -H "Content-Type: application/json" \
    -d "{\"machine_id\":\"$MACHINE_ID\",\"name\":\"api\",\"subdomain\":\"api\",\"local_port\":4000}")
TUNNEL2_ID=$(echo "$RESP" | jq -r '.data.id // empty')
[[ -n "$TUNNEL2_ID" ]] || fail "Second tunnel creation failed — response: $RESP"

CADDYFILE=$(curl -sf -b "$COOKIE_JAR" \
    "http://localhost:$GOPHER_PORT/api/debug/caddyfile")
echo "$CADDYFILE" | grep -q "myapp.example.com" \
    || fail "Caddyfile missing first tunnel after second was added"
echo "$CADDYFILE" | grep -q "api.example.com" \
    || fail "Caddyfile missing second tunnel"
pass "Caddyfile correctly shows both tunnels"

echo ""
echo "=============================="
echo -e "${GREEN}✅ ALL CONFIG VALIDATION TESTS PASSED${NC}"
echo "=============================="
