#!/bin/bash
set -e

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

echo "Building Gopher..."

echo "→ Installing frontend dependencies..."
cd "$ROOT/frontend"
npm ci

echo "→ Building frontend..."
npm run build

echo "→ Building Go binary..."
cd "$ROOT"
go build -o gopher ./cmd/server/...

echo "✓ Build complete: $ROOT/gopher"
echo "  Run with: ./gopher [--port 8080] [--db ./gopher.db]"
