#!/bin/bash
# tests/critical-path.sh
# Tests the complete happy-path workflow: auth → machine CRUD → tunnel CRUD → DB state.
# Run from the repository root: ./tests/critical-path.sh

set -euo pipefail

GOPHER_PORT=8181
GOPHER_DB="test-critical.db"
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

echo "🧪 Critical Path Test"
echo "====================="

# ── Preflight ──────────────────────────────────────────────────────────────────
if [[ ! -x ./gopher ]]; then
    fail "gopher binary not found. Run ./scripts/build.sh first."
fi
command -v jq >/dev/null 2>&1 || fail "jq is required but not installed"
command -v sqlite3 >/dev/null 2>&1 || fail "sqlite3 is required but not installed"

rm -f "$GOPHER_DB"
COOKIE_JAR=$(mktemp /tmp/gopher-cookies.XXXXX)

# ── 1. Start Server ────────────────────────────────────────────────────────────
echo ""
echo "1. Starting Gopher on port $GOPHER_PORT..."
./gopher --db "$GOPHER_DB" --port "$GOPHER_PORT" >/dev/null 2>&1 &
GOPHER_PID=$!

# Poll until ready (up to 15s)
for i in $(seq 1 30); do
    if curl -sf "http://localhost:$GOPHER_PORT/api/status" >/dev/null 2>&1; then
        pass "Server ready (${i} × 0.5s)"
        break
    fi
    sleep 0.5
    if [[ $i -eq 30 ]]; then
        fail "Server did not start within 15 seconds"
    fi
done

# ── 2. Auth Setup ──────────────────────────────────────────────────────────────
echo ""
echo "2. Setting up authentication..."
RESP=$(curl -sf -c "$COOKIE_JAR" \
    -X POST "http://localhost:$GOPHER_PORT/api/auth/setup" \
    -H "Content-Type: application/json" \
    -d '{"password":"gopher-test-pass"}')

echo "$RESP" | jq -e '.success == true' >/dev/null \
    || fail "Auth setup failed — response: $RESP"
pass "Auth configured"

# Confirm session cookie was set
grep -q "gopher_session" "$COOKIE_JAR" \
    || fail "No session cookie received after setup"
pass "Session cookie received"

# ── 3. Status endpoint (public) ────────────────────────────────────────────────
echo ""
echo "3. Checking public status endpoint..."
STATUS=$(curl -sf "http://localhost:$GOPHER_PORT/api/status")
echo "$STATUS" | jq -e '.success == true' >/dev/null \
    || fail "Status endpoint returned unexpected response: $STATUS"
pass "Status endpoint ok"

# ── 4. Create Machine ──────────────────────────────────────────────────────────
echo ""
echo "4. Creating machine..."
RESP=$(curl -sf -b "$COOKIE_JAR" \
    -X POST "http://localhost:$GOPHER_PORT/api/machines" \
    -H "Content-Type: application/json" \
    -d '{"name":"test-machine","username":"ubuntu"}')

MACHINE_ID=$(echo "$RESP" | jq -r '.data.id // empty')
[[ -n "$MACHINE_ID" ]] || fail "Machine creation failed — response: $RESP"
pass "Machine created (id: $MACHINE_ID)"

# ── 5. Get Machine ─────────────────────────────────────────────────────────────
echo ""
echo "5. Fetching machine by id..."
RESP=$(curl -sf -b "$COOKIE_JAR" \
    "http://localhost:$GOPHER_PORT/api/machines/$MACHINE_ID")
GOT_ID=$(echo "$RESP" | jq -r '.data.id // empty')
[[ "$GOT_ID" == "$MACHINE_ID" ]] \
    || fail "GET machine returned wrong id: $GOT_ID"
pass "Machine GET ok"

# ── 6. List Machines ───────────────────────────────────────────────────────────
echo ""
echo "6. Listing machines..."
RESP=$(curl -sf -b "$COOKIE_JAR" "http://localhost:$GOPHER_PORT/api/machines")
COUNT=$(echo "$RESP" | jq '.data | length')
[[ "$COUNT" -eq 1 ]] || fail "Expected 1 machine in list, got $COUNT"
pass "Machine list ok (count: $COUNT)"

# ── 7. Create Tunnel ───────────────────────────────────────────────────────────
echo ""
echo "7. Creating tunnel..."
RESP=$(curl -sf -b "$COOKIE_JAR" \
    -X POST "http://localhost:$GOPHER_PORT/api/tunnels" \
    -H "Content-Type: application/json" \
    -d "{\"machine_id\":\"$MACHINE_ID\",\"name\":\"test-service\",\"subdomain\":\"\",\"local_port\":8080}")

TUNNEL_ID=$(echo "$RESP" | jq -r '.data.id // empty')
[[ -n "$TUNNEL_ID" ]] || fail "Tunnel creation failed — response: $RESP"
pass "Tunnel created (id: $TUNNEL_ID)"

# Verify port in response
echo "$RESP" | jq -e '.data.local_port == 8080' >/dev/null \
    || fail "Tunnel local_port mismatch in response"
pass "Tunnel fields correct"

# ── 8. Verify DB State ─────────────────────────────────────────────────────────
echo ""
echo "8. Verifying database state..."
MACHINE_COUNT=$(sqlite3 "$GOPHER_DB" "SELECT COUNT(*) FROM machines;")
TUNNEL_COUNT=$(sqlite3 "$GOPHER_DB" "SELECT COUNT(*) FROM tunnels;")
[[ "$MACHINE_COUNT" -eq 1 ]] || fail "Expected 1 machine in DB, got $MACHINE_COUNT"
[[ "$TUNNEL_COUNT" -eq 1 ]] || fail "Expected 1 tunnel in DB, got $TUNNEL_COUNT"
pass "DB state correct (machines: $MACHINE_COUNT, tunnels: $TUNNEL_COUNT)"

# ── 9. Delete Tunnel ───────────────────────────────────────────────────────────
echo ""
echo "9. Deleting tunnel..."
DELETE_RESP=$(curl -s -b "$COOKIE_JAR" -w "\n%{http_code}" \
    -X DELETE "http://localhost:$GOPHER_PORT/api/tunnels/$TUNNEL_ID")
HTTP_CODE=$(echo "$DELETE_RESP" | tail -1)
BODY=$(echo "$DELETE_RESP" | head -n -1)
[[ "$HTTP_CODE" == "204" ]] || fail "Tunnel DELETE returned HTTP $HTTP_CODE (expected 204) — response: $BODY"

TUNNEL_COUNT=$(sqlite3 "$GOPHER_DB" "SELECT COUNT(*) FROM tunnels;")
[[ "$TUNNEL_COUNT" -eq 0 ]] || fail "Tunnel still in DB after delete (count: $TUNNEL_COUNT)"
pass "Tunnel deleted"

# ── 10. Delete Machine ─────────────────────────────────────────────────────────
echo ""
echo "10. Deleting machine..."
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -b "$COOKIE_JAR" \
    -X DELETE "http://localhost:$GOPHER_PORT/api/machines/$MACHINE_ID")
[[ "$HTTP_CODE" == "204" ]] || fail "Machine DELETE returned HTTP $HTTP_CODE (expected 204)"

MACHINE_COUNT=$(sqlite3 "$GOPHER_DB" "SELECT COUNT(*) FROM machines;")
[[ "$MACHINE_COUNT" -eq 0 ]] || fail "Machine still in DB after delete (count: $MACHINE_COUNT)"
pass "Machine deleted"

# ── 11. Auth Guard Check ───────────────────────────────────────────────────────
echo ""
echo "11. Verifying auth guard on protected endpoints..."
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
    "http://localhost:$GOPHER_PORT/api/machines")
[[ "$HTTP_CODE" == "401" ]] || fail "Unauthenticated request to /api/machines returned $HTTP_CODE (expected 401)"
pass "Auth guard working"

echo ""
echo "=============================="
echo -e "${GREEN}✅ ALL CRITICAL PATH TESTS PASSED${NC}"
echo "=============================="
