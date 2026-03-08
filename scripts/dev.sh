#!/bin/bash
set -e

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

echo "Starting Gopher in development mode..."

# Start backend
echo "Starting Go backend on :8080..."
(cd "$ROOT" && go run ./cmd/server/...) &
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
