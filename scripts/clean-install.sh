#!/bin/bash
# Dev script: uninstall everything and rebuild from scratch

set -e

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

echo "🧹 Uninstalling Gopher..."
sudo "$ROOT/gopher" uninstall --skip-prompts || true

echo ""
echo "🏗️  Building from scratch..."
bash "$ROOT/scripts/build.sh"

echo ""
echo "✓ Clean build complete"
echo ""
echo "Next steps:"
echo "  1. ./gopher install   (will prompt for sudo password)"
echo "  2. ./gopher           (start the server)"
