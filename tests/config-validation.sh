#!/bin/bash
# tests/config-validation.sh
# Tests that config generation produces valid, well-formed output.
# Run from the repository root: ./tests/config-validation.sh

set -euo pipefail

GOPHER_PORT=8181
GOPHER_DB="test-config.db"
COOKIE_JAR=""
GOPHER_PID=""

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}✅ $1${NC}"; }
skip() { echo -e "${YELLOW}⚠️  $1${NC}"; }
fail() { echo -e "${RED}❌ $1${NC}"; exit 1; }

cleanup() {
    [[ -n "$GOPHER_PID" ]] && kill "$GOPHER_PID" 2>/dev/null || true
    [[ -n "$COOKIE_JAR" ]] && rm -f "$COOKIE_JAR"
    rm -f "$GOPHER_DB"
}
trap cleanup EXIT

echo "🧪 Config Validation Tests"
echo "=========================="

# ── Preflight ──────────────────────────────────────────────────────────────────
if [[ ! -x ./gopher ]]; then
    fail "gopher binary not found. Run ./scripts/build.sh first."
fi
command -v jq >/dev/null 2>&1 || fail "jq is required but not installed"
command -v sqlite3 >/dev/null 2>&1 || fail "sqlite3 is required but not installed"

rm -f "$GOPHER_DB"
COOKIE_JAR=$(mktemp /tmp/gopher-cookies.XXXXX)

# ── Start Server ───────────────────────────────────────────────────────────────
echo ""
echo "1. Starting Gopher on port $GOPHER_PORT..."
./gopher --db "$GOPHER_DB" --port "$GOPHER_PORT" >/dev/null 2>&1 &
GOPHER_PID=$!

# 30s ceiling — CI runners hit the previous 15s cap intermittently
# under cold-cache conditions; see critical-path.sh for the same fix.
for i in $(seq 1 60); do
    if curl -sf "http://localhost:$GOPHER_PORT/api/status" >/dev/null 2>&1; then
        pass "Server ready"
        break
    fi
    sleep 0.5
    [[ $i -eq 60 ]] && fail "Server did not start within 30 seconds"
done

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

# ── Set Domain via local/skip ─────────────────────────────────────────────────
echo ""
echo "3. Configuring domain via local/skip..."
RESP=$(curl -sf -b "$COOKIE_JAR" \
    -X POST "http://localhost:$GOPHER_PORT/api/local/skip" \
    -H "Content-Type: application/json" \
    -d '{"domain":"example.com"}')
echo "$RESP" | jq -e '.success == true' >/dev/null \
    || fail "local/skip failed — response: $RESP"
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

# Optional: validate with caddy if installed
if command -v caddy >/dev/null 2>&1; then
    TMPFILE=$(mktemp /tmp/test-XXXXX.Caddyfile)
    echo "$CADDYFILE" > "$TMPFILE"
    if caddy validate --config "$TMPFILE" >/dev/null 2>&1; then
        pass "Caddyfile syntax valid (caddy validate)"
    else
        fail "Caddyfile syntax invalid (caddy validate)"
    fi
    rm -f "$TMPFILE"
else
    skip "caddy not installed — skipping syntax validation"
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
