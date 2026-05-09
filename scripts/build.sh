#!/bin/bash
set -e

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

echo "Building Gopher..."

echo "→ Installing frontend dependencies..."
cd "$ROOT/frontend"
npm ci

echo "→ Building frontend..."
npm run build

echo "→ Building gopher-agent (linux/amd64 + linux/arm64)..."
cd "$ROOT"
mkdir -p cmd/server/agents
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o cmd/server/agents/gopher-agent-linux-amd64 ./cmd/agent/
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o cmd/server/agents/gopher-agent-linux-arm64 ./cmd/agent/

echo "→ Building Go binary..."
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
go build -ldflags "-X github.com/smalex-z/gopher/internal/build.Version=${VERSION}" -o gopher ./cmd/server/...

echo "✓ Build complete: $ROOT/gopher"
echo "  Run with: ./gopher [--port 4321] [--db ./gopher.db]"
