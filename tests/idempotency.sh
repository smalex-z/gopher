#!/bin/bash
# tests/idempotency.sh
# Tests that operations are safe to repeat and that duplicates are rejected.
# Run from the repository root: ./tests/idempotency.sh

set -euo pipefail

GOPHER_PORT=8181
GOPHER_DB="test-idempotency.db"
COOKIE_JAR=""
GOPHER_PID=""

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

pass() { echo -e "${GREEN}✅ $1${NC}"; }
fail() { echo -e "${RED}❌ $1${NC}"; exit 1; }

cleanup() {
    [[ -n "$GOPHER_PID" ]] && kill "$GOPHER_PID" 2>/dev/null || true
    [[ -n "$COOKIE_JAR" ]] && rm -f "$COOKIE_JAR"
    rm -f "$GOPHER_DB"
}
trap cleanup EXIT

echo "🧪 Idempotency Tests"
echo "===================="

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
curl -sf -c "$COOKIE_JAR" \
    -X POST "http://localhost:$GOPHER_PORT/api/auth/setup" \
    -H "Content-Type: application/json" \
    -d '{"password":"gopher-test-pass"}' >/dev/null
pass "Auth configured"

# ── Setup: create a machine ────────────────────────────────────────────────────
RESP=$(curl -sf -b "$COOKIE_JAR" \
    -X POST "http://localhost:$GOPHER_PORT/api/machines" \
    -H "Content-Type: application/json" \
    -d '{"name":"idempotency-machine","username":"ubuntu"}')
MACHINE_ID=$(echo "$RESP" | jq -r '.data.id // empty')
[[ -n "$MACHINE_ID" ]] || fail "Machine creation failed — response: $RESP"
pass "Test machine created (id: $MACHINE_ID)"

# ── Test 1: Create multiple tunnels ────────────────────────────────────────────
echo ""
echo "2. Testing multiple tunnel creation..."
RESP1=$(curl -sf -b "$COOKIE_JAR" \
    -X POST "http://localhost:$GOPHER_PORT/api/tunnels" \
    -H "Content-Type: application/json" \
    -d "{\"machine_id\":\"$MACHINE_ID\",\"name\":\"tunnel-1\",\"subdomain\":\"\",\"local_port\":8080}")
T1_ID=$(echo "$RESP1" | jq -r '.data.id // empty')
[[ -n "$T1_ID" ]] || fail "First tunnel creation failed — response: $RESP1"
pass "First tunnel created (id: $T1_ID)"

RESP2=$(curl -sf -b "$COOKIE_JAR" \
    -X POST "http://localhost:$GOPHER_PORT/api/tunnels" \
    -H "Content-Type: application/json" \
    -d "{\"machine_id\":\"$MACHINE_ID\",\"name\":\"tunnel-2\",\"subdomain\":\"\",\"local_port\":8081}")
T2_ID=$(echo "$RESP2" | jq -r '.data.id // empty')
[[ -n "$T2_ID" ]] || fail "Second tunnel creation failed — response: $RESP2"
pass "Second tunnel created (id: $T2_ID)"

TUNNEL_COUNT=$(sqlite3 "$GOPHER_DB" "SELECT COUNT(*) FROM tunnels;")
[[ "$TUNNEL_COUNT" -eq 2 ]] \
    || fail "Expected 2 tunnels in DB, found $TUNNEL_COUNT"
pass "DB has exactly 2 tunnels"

# ── Test 2: Duplicate rathole port is rejected ─────────────────────────────────
echo ""
echo "3. Testing duplicate rathole port rejection..."
RATHOLE_PORT=$(echo "$RESP1" | jq -r '.data.rathole_port')

HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -b "$COOKIE_JAR" \
    -X POST "http://localhost:$GOPHER_PORT/api/tunnels" \
    -H "Content-Type: application/json" \
    -d "{\"machine_id\":\"$MACHINE_ID\",\"name\":\"tunnel-dup\",\"subdomain\":\"\",\"local_port\":8090,\"rathole_port\":$RATHOLE_PORT}")
[[ "$HTTP_CODE" == "409" ]] \
    || fail "Duplicate rathole port should return 409 Conflict, got HTTP $HTTP_CODE"
pass "Duplicate rathole port correctly rejected (409)"

# ── Test 3: Delete non-existent tunnel is 404, not a crash ────────────────────
echo ""
echo "4. Testing delete of non-existent tunnel..."
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -b "$COOKIE_JAR" \
    -X DELETE "http://localhost:$GOPHER_PORT/api/tunnels/does-not-exist-999")
[[ "$HTTP_CODE" == "404" ]] \
    || fail "Expected 404 for non-existent tunnel, got HTTP $HTTP_CODE"
# Server must still respond (not have crashed)
STATUS=$(curl -sf "http://localhost:$GOPHER_PORT/api/status")
echo "$STATUS" | jq -e '.success == true' >/dev/null \
    || fail "Server crashed after deleting non-existent tunnel"
pass "Delete non-existent tunnel returns 404 and server still up"

# ── Test 4: Delete non-existent machine is handled gracefully ─────────────────
echo ""
echo "5. Testing delete of non-existent machine..."
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -b "$COOKIE_JAR" \
    -X DELETE "http://localhost:$GOPHER_PORT/api/machines/does-not-exist-999")
[[ "$HTTP_CODE" == "404" ]] \
    || fail "Expected 404 for non-existent machine, got HTTP $HTTP_CODE"
STATUS=$(curl -sf "http://localhost:$GOPHER_PORT/api/status")
echo "$STATUS" | jq -e '.success == true' >/dev/null \
    || fail "Server crashed after deleting non-existent machine"
pass "Delete non-existent machine returns 404 and server still up"

# ── Test 5: Auth setup is idempotent (second call returns 409) ─────────────────
echo ""
echo "6. Testing auth setup idempotency..."
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
    -X POST "http://localhost:$GOPHER_PORT/api/auth/setup" \
    -H "Content-Type: application/json" \
    -d '{"password":"another-password"}')
[[ "$HTTP_CODE" == "409" ]] \
    || fail "Second auth setup should return 409 Conflict, got HTTP $HTTP_CODE"
pass "Auth setup correctly rejects second call (409)"

# ── Test 6: Invalid port is rejected ──────────────────────────────────────────
echo ""
echo "7. Testing invalid port rejection..."
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -b "$COOKIE_JAR" \
    -X POST "http://localhost:$GOPHER_PORT/api/tunnels" \
    -H "Content-Type: application/json" \
    -d "{\"machine_id\":\"$MACHINE_ID\",\"name\":\"tunnel-bad-port\",\"subdomain\":\"\",\"local_port\":99999}")
[[ "$HTTP_CODE" == "400" ]] \
    || fail "Invalid port should return 400 Bad Request, got HTTP $HTTP_CODE"
pass "Invalid port rejected (400)"

echo ""
echo "=============================="
echo -e "${GREEN}✅ ALL IDEMPOTENCY TESTS PASSED${NC}"
echo "=============================="
