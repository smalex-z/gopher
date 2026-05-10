#!/bin/bash
# Local-only dev runner. Runs the Go backend with --dev so it cannot touch any
# system-managed file (rathole config, Caddy conf.d, sudoers, authorized_keys),
# against an isolated DB in /tmp so a stale ./gopher.db in the repo can't ever
# get reconciled into production's /etc/rathole/server.toml again.
#
# Production gopher.service uses port 4321 and /var/lib/gopher/gopher.db; this
# script uses :4322 and /tmp/gopher-dev.db so both can coexist on the same host.
set -e

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEV_DB="${GOPHER_DEV_DB:-/tmp/gopher-dev.db}"
DEV_PORT="${GOPHER_DEV_PORT:-4322}"

echo "Starting Gopher in development mode..."
echo "  --dev flag: enabled (no system writes)"
echo "  DB:   $DEV_DB"
echo "  Port: $DEV_PORT"

# Start backend
echo "Starting Go backend on :$DEV_PORT..."
(cd "$ROOT" && go run ./cmd/server/... --dev --db "$DEV_DB" --port "$DEV_PORT") &
BACKEND_PID=$!

# Start frontend dev server
echo "Starting Vite dev server on :5173..."
(cd "$ROOT/frontend" && npm run dev) &
FRONTEND_PID=$!

cleanup() {
  echo "Shutting down..."
  kill "$BACKEND_PID" "$FRONTEND_PID" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

wait
