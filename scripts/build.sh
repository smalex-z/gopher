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
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
go build -ldflags "-X github.com/smalex-z/gopher/internal/build.Version=${VERSION}" -o gopher ./cmd/server/...

echo "✓ Build complete: $ROOT/gopher"
echo "  Run with: ./gopher [--port 4321] [--db ./gopher.db]"
