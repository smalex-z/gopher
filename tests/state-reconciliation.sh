#!/bin/bash
# tests/state-reconciliation.sh
# Tests that cascade deletes and state cleanup work correctly.
# Run from the repository root: ./tests/state-reconciliation.sh

set -euo pipefail

GOPHER_PORT=8181
GOPHER_DB="test-state.db"
COOKIE_JAR=""
GOPHER_PID=""
GOPHER_LOG=""

# shellcheck disable=SC1091
source "$(dirname "$0")/lib.sh"

trap cleanup_gopher_artefacts EXIT

echo "🧪 State Reconciliation Tests"
echo "============================="

# ── Preflight ──────────────────────────────────────────────────────────────────
if [[ ! -x ./gopher ]]; then
    fail "gopher binary not found. Run ./scripts/build.sh first."
fi
command -v jq >/dev/null 2>&1 || fail "jq is required but not installed"
command -v sqlite3 >/dev/null 2>&1 || fail "sqlite3 is required but not installed"

COOKIE_JAR=$(mktemp /tmp/gopher-cookies.XXXXX)
GOPHER_LOG=$(mktemp /tmp/gopher-stderr.XXXXX)

# ── Start Server ───────────────────────────────────────────────────────────────
echo ""
echo "1. Starting Gopher on port $GOPHER_PORT..."
start_gopher_with_retry

# ── Auth ───────────────────────────────────────────────────────────────────────
curl -sf -c "$COOKIE_JAR" \
    -X POST "http://localhost:$GOPHER_PORT/api/auth/setup" \
    -H "Content-Type: application/json" \
    -d '{"password":"gopher-test-pass"}' >/dev/null
pass "Auth configured"

# ── Create Two Machines ────────────────────────────────────────────────────────
echo ""
echo "2. Creating two machines..."
RESP=$(curl -sf -b "$COOKIE_JAR" \
    -X POST "http://localhost:$GOPHER_PORT/api/machines" \
    -H "Content-Type: application/json" \
    -d '{"name":"machine-one","username":"ubuntu"}')
M1=$(echo "$RESP" | jq -r '.data.id // empty')
[[ -n "$M1" ]] || fail "Machine 1 creation failed — response: $RESP"

RESP=$(curl -sf -b "$COOKIE_JAR" \
    -X POST "http://localhost:$GOPHER_PORT/api/machines" \
    -H "Content-Type: application/json" \
    -d '{"name":"machine-two","username":"ubuntu"}')
M2=$(echo "$RESP" | jq -r '.data.id // empty')
[[ -n "$M2" ]] || fail "Machine 2 creation failed — response: $RESP"
pass "Two machines created (m1: $M1, m2: $M2)"

# ── Create Tunnels ─────────────────────────────────────────────────────────────
echo ""
echo "3. Creating 3 tunnels (2 on m1, 1 on m2)..."
RESP=$(curl -sf -b "$COOKIE_JAR" \
    -X POST "http://localhost:$GOPHER_PORT/api/tunnels" \
    -H "Content-Type: application/json" \
    -d "{\"machine_id\":\"$M1\",\"name\":\"tunnel-1\",\"subdomain\":\"\",\"local_port\":8080}")
T1=$(echo "$RESP" | jq -r '.data.id // empty')
[[ -n "$T1" ]] || fail "Tunnel 1 creation failed"

RESP=$(curl -sf -b "$COOKIE_JAR" \
    -X POST "http://localhost:$GOPHER_PORT/api/tunnels" \
    -H "Content-Type: application/json" \
    -d "{\"machine_id\":\"$M1\",\"name\":\"tunnel-2\",\"subdomain\":\"\",\"local_port\":8081}")
T2=$(echo "$RESP" | jq -r '.data.id // empty')
[[ -n "$T2" ]] || fail "Tunnel 2 creation failed"

RESP=$(curl -sf -b "$COOKIE_JAR" \
    -X POST "http://localhost:$GOPHER_PORT/api/tunnels" \
    -H "Content-Type: application/json" \
    -d "{\"machine_id\":\"$M2\",\"name\":\"tunnel-3\",\"subdomain\":\"\",\"local_port\":8082}")
T3=$(echo "$RESP" | jq -r '.data.id // empty')
[[ -n "$T3" ]] || fail "Tunnel 3 creation failed"

pass "3 tunnels created"

# Verify baseline state
M1_TUNNELS=$(sqlite3 "$GOPHER_DB" "SELECT COUNT(*) FROM tunnels WHERE machine_id='$M1';")
[[ "$M1_TUNNELS" -eq 2 ]] \
    || fail "Expected 2 tunnels for machine 1 before delete, found $M1_TUNNELS"

TOTAL_TUNNELS=$(sqlite3 "$GOPHER_DB" "SELECT COUNT(*) FROM tunnels;")
[[ "$TOTAL_TUNNELS" -eq 3 ]] \
    || fail "Expected 3 total tunnels before delete, found $TOTAL_TUNNELS"
pass "Baseline state correct (m1: 2 tunnels, m2: 1 tunnel, total: 3)"

# ── Test 1: Cascade Delete (machine 1 + its 2 tunnels) ────────────────────────
echo ""
echo "4. Deleting machine 1 (should cascade-delete its 2 tunnels)..."
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -b "$COOKIE_JAR" \
    -X DELETE "http://localhost:$GOPHER_PORT/api/machines/$M1")
[[ "$HTTP_CODE" == "204" ]] \
    || fail "Machine 1 DELETE returned HTTP $HTTP_CODE (expected 204)"

# Machine 1 should be gone
M1_COUNT=$(sqlite3 "$GOPHER_DB" "SELECT COUNT(*) FROM machines WHERE id='$M1';")
[[ "$M1_COUNT" -eq 0 ]] || fail "Machine 1 still in DB after delete"
pass "Machine 1 removed from DB"

# Machine 1's tunnels should be gone
M1_TUNNELS=$(sqlite3 "$GOPHER_DB" "SELECT COUNT(*) FROM tunnels WHERE machine_id='$M1';")
[[ "$M1_TUNNELS" -eq 0 ]] \
    || fail "Machine 1's tunnels still in DB after cascade delete (found $M1_TUNNELS)"
pass "Machine 1's tunnels cascade-deleted (found 0)"

# Machine 2's tunnel should still exist
REMAINING=$(sqlite3 "$GOPHER_DB" "SELECT COUNT(*) FROM tunnels;")
[[ "$REMAINING" -eq 1 ]] \
    || fail "Expected 1 remaining tunnel (m2's), found $REMAINING"
pass "Machine 2's tunnel still intact (total tunnels: $REMAINING)"

# Machine 2 should still be present
M2_COUNT=$(sqlite3 "$GOPHER_DB" "SELECT COUNT(*) FROM machines WHERE id='$M2';")
[[ "$M2_COUNT" -eq 1 ]] || fail "Machine 2 was unexpectedly deleted"
pass "Machine 2 unaffected"

# ── Test 2: API-level state consistency ────────────────────────────────────────
echo ""
echo "5. Verifying API-level state consistency..."
LIST=$(curl -sf -b "$COOKIE_JAR" "http://localhost:$GOPHER_PORT/api/machines")
API_MACHINE_COUNT=$(echo "$LIST" | jq '.data | length')
[[ "$API_MACHINE_COUNT" -eq 1 ]] \
    || fail "Expected 1 machine via API, got $API_MACHINE_COUNT"
pass "Machine list via API shows 1 machine"

LIST=$(curl -sf -b "$COOKIE_JAR" "http://localhost:$GOPHER_PORT/api/tunnels")
API_TUNNEL_COUNT=$(echo "$LIST" | jq '.data | length')
[[ "$API_TUNNEL_COUNT" -eq 1 ]] \
    || fail "Expected 1 tunnel via API, got $API_TUNNEL_COUNT"
pass "Tunnel list via API shows 1 tunnel"

# The remaining tunnel should belong to machine 2
REMAINING_MID=$(echo "$LIST" | jq -r '.data[0].machine_id')
[[ "$REMAINING_MID" == "$M2" ]] \
    || fail "Remaining tunnel belongs to '$REMAINING_MID', expected '$M2'"
pass "Remaining tunnel correctly belongs to machine 2"

# ── Test 3: Fetch deleted machine returns 404 ──────────────────────────────────
echo ""
echo "6. Fetching deleted machine should return 404..."
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -b "$COOKIE_JAR" \
    "http://localhost:$GOPHER_PORT/api/machines/$M1")
[[ "$HTTP_CODE" == "404" ]] \
    || fail "GET deleted machine returned HTTP $HTTP_CODE (expected 404)"
pass "Deleted machine returns 404"

# ── Test 4: Clean up everything ────────────────────────────────────────────────
echo ""
echo "7. Cleaning up remaining data..."
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -b "$COOKIE_JAR" \
    -X DELETE "http://localhost:$GOPHER_PORT/api/machines/$M2")
[[ "$HTTP_CODE" == "204" ]] \
    || fail "Machine 2 DELETE returned HTTP $HTTP_CODE"

FINAL_MACHINES=$(sqlite3 "$GOPHER_DB" "SELECT COUNT(*) FROM machines;")
FINAL_TUNNELS=$(sqlite3 "$GOPHER_DB" "SELECT COUNT(*) FROM tunnels;")
[[ "$FINAL_MACHINES" -eq 0 ]] || fail "Expected 0 machines, found $FINAL_MACHINES"
[[ "$FINAL_TUNNELS" -eq 0 ]] || fail "Expected 0 tunnels, found $FINAL_TUNNELS"
pass "All data cleaned up (machines: $FINAL_MACHINES, tunnels: $FINAL_TUNNELS)"

echo ""
echo "=============================="
echo -e "${GREEN}✅ ALL STATE RECONCILIATION TESTS PASSED${NC}"
echo "=============================="
