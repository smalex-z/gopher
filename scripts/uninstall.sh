#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
gopher_bin="$repo_root/gopher"

echo "[DEPRECATED] scripts/uninstall.sh is now a compatibility wrapper."
echo "Use ./gopher uninstall going forward."

if [[ ! -x "$gopher_bin" ]]; then
  echo "ERROR: $gopher_bin not found or not executable. Build first with ./scripts/build.sh" >&2
  exit 1
fi

exec "$gopher_bin" uninstall "$@"
