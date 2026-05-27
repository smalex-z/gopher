#!/bin/bash
# tests/lib.sh — shared helpers for the shell integration tests.
#
# Sourced by critical-path.sh, config-validation.sh, idempotency.sh,
# state-reconciliation.sh. Centralises the start-gopher-and-poll dance so a
# fix to one place fixes all four.
#
# Don't run this file directly.

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

pass() { echo -e "${GREEN}✅ $1${NC}"; }
fail() { echo -e "${RED}❌ $1${NC}"; exit 1; }

# start_gopher_with_retry launches ./gopher in the background, polls
# /api/status until either the server is reachable or the process dies,
# and automatically retries once on early exit. Sets the global $GOPHER_PID
# to the running process's PID on success.
#
# Globals it relies on:
#   GOPHER_DB    — path to the SQLite DB file (will be removed + recreated)
#   GOPHER_PORT  — TCP port to bind
#   GOPHER_LOG   — path where gopher's stderr is captured (caller mktemp's it)
#
# Why this exists at all:
#   The previous inline poll loop redirected stderr to /dev/null and never
#   checked whether the process was still alive. On GHA's overlay storage,
#   SQLite occasionally trips a transient SQLITE_IOERR_SHORT_READ (error
#   522) during AutoMigrate's first CREATE TABLE — gopher exits in ~50ms
#   and the test then sat for 30s polling a dead listener before failing
#   with a misleading "didn't start in time" message. This helper (a)
#   captures stderr so the failure mode is visible, (b) breaks the wait
#   the instant the process exits, and (c) retries once because a single
#   retry has cleared the 522 flake every time it's been observed.
start_gopher_with_retry() {
    if [[ -z "${GOPHER_LOG:-}" ]]; then
        fail "start_gopher_with_retry: \$GOPHER_LOG not set (caller must mktemp it)"
    fi
    for attempt in 1 2; do
        rm -f "$GOPHER_DB" "${GOPHER_DB}-wal" "${GOPHER_DB}-shm" "${GOPHER_DB}".bak.*
        ./gopher --db "$GOPHER_DB" --port "$GOPHER_PORT" >"$GOPHER_LOG" 2>&1 &
        GOPHER_PID=$!
        local rc=0
        for i in $(seq 1 60); do
            if curl -sf "http://localhost:$GOPHER_PORT/api/status" >/dev/null 2>&1; then
                pass "Server ready (${i} × 0.5s)"
                return 0
            fi
            if ! kill -0 "$GOPHER_PID" 2>/dev/null; then
                rc=1; break # process exited
            fi
            sleep 0.5
        done
        if [[ $rc -eq 0 ]]; then
            rc=2 # timeout while process still running
        fi
        if [[ $attempt -lt 2 ]]; then
            if [[ $rc -eq 1 ]]; then
                echo "    (gopher exited early on attempt $attempt, retrying once...)"
            else
                echo "    (gopher timed out on attempt $attempt, retrying once...)"
                kill "$GOPHER_PID" 2>/dev/null || true
            fi
            sleep 1 # let the kernel release the unlinked inode
            continue
        fi
        echo "--- gopher stderr (last 50 lines) ---"
        tail -n 50 "$GOPHER_LOG" || true
        echo "--- end stderr ---"
        if [[ $rc -eq 1 ]]; then
            fail "Server process exited during startup (twice)"
        else
            fail "Server did not start within 30 seconds (twice)"
        fi
    done
}

# cleanup_gopher_artefacts removes everything start_gopher_with_retry could
# have created. Call from a `trap ... EXIT` in each test.
cleanup_gopher_artefacts() {
    [[ -n "${GOPHER_PID:-}" ]] && kill "$GOPHER_PID" 2>/dev/null || true
    [[ -n "${COOKIE_JAR:-}" ]] && rm -f "$COOKIE_JAR"
    [[ -n "${GOPHER_LOG:-}" ]] && rm -f "$GOPHER_LOG"
    [[ -n "${GOPHER_DB:-}" ]] && rm -f "$GOPHER_DB" "${GOPHER_DB}-wal" "${GOPHER_DB}-shm" "${GOPHER_DB}".bak.*
}
