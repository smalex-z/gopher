#!/bin/bash
# uninstall.sh — Remove Gopher and (optionally) Caddy and/or rathole-server.
#
# Usage:
#   sudo ./scripts/uninstall.sh [OPTIONS]
#
# Options:
#   --remove-caddy      Stop, disable, and purge Caddy and all its config files.
#                       Without this flag, only the Gopher-managed blocks are
#                       stripped from /etc/caddy/Caddyfile.
#   --remove-rathole    Stop, disable, and remove the rathole binary and all its
#                       config files (/etc/rathole/).
#                       Without this flag, only the Gopher-managed entries are
#                       stripped from /etc/rathole/server.toml.
#   --domain DOMAIN     The domain used during Gopher setup (e.g. example.com).
#                       Required to remove the router.DOMAIN Caddy block that
#                       Caddy blocks that Gopher inserted above the custom section.
#   --db PATH           Path to the Gopher SQLite database (default: ./gopher.db).
#                       The script will ask before deleting it.
#   -y, --yes           Non-interactive: skip the database-removal confirmation.
#   -h, --help          Show this help text.
#
# Notes:
#   - This script must be run as root or with sudo.
#   - Caddy and rathole config files are backed up before modification
#     (*.gopher-backup next to the originals).
#   - User content that was placed INSIDE the custom section by Gopher's merge
#     step will be preserved in the stripped Caddyfile.

set -euo pipefail

REMOVE_CADDY=false
REMOVE_RATHOLE=false
DOMAIN=""
DB_PATH="./gopher.db"
YES=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --remove-caddy)   REMOVE_CADDY=true; shift ;;
        --remove-rathole) REMOVE_RATHOLE=true; shift ;;
        --domain)         DOMAIN="${2:-}"; shift 2 ;;
        --db)             DB_PATH="${2:-}"; shift 2 ;;
        -y|--yes)         YES=true; shift ;;
        -h|--help)
            sed -n '/^# uninstall/,/^[^#]/p' "$0" | head -n -1 | sed 's/^# \{0,2\}//'
            exit 0
            ;;
        *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

info()    { echo "  [INFO]  $*"; }
success() { echo "  [OK]    $*"; }
warn()    { echo "  [WARN]  $*"; }

require_sudo() {
    if [[ $EUID -ne 0 ]] && ! sudo -n true 2>/dev/null; then
        echo "ERROR: This script requires root or passwordless sudo." >&2
        exit 1
    fi
}

sudo_run() { sudo -- "$@"; }

# strip_caddyfile PATH DOMAIN
#   Removes Gopher-managed blocks from the Caddyfile at PATH.
#   DOMAIN may be empty; the function will attempt to auto-detect it.
strip_caddyfile() {
    local path="$1" domain="$2"
    sudo_run python3 - "$path" "$domain" <<'PYEOF'
import sys, re

path   = sys.argv[1]
domain = sys.argv[2] if len(sys.argv) > 2 else ""

BEGIN = "# ===== BEGIN CUSTOM CONFIGURATION ====="
END   = "# ===== END CUSTOM CONFIGURATION ====="

with open(path) as fh:
    content = fh.read()

# Auto-detect domain if not supplied by looking for a router.DOMAIN block.
if not domain:
    m = re.search(r'^router\.(\S+)\s*\{', content, re.MULTILINE)
    if m:
        domain = m.group(1)
        print(f"    Auto-detected domain: {domain}")

def remove_block(text, host_prefix):
    """Remove the Caddy site block whose opening line starts with host_prefix."""
    lines  = text.split("\n")
    result = []
    depth  = 0
    skip   = False
    for line in lines:
        stripped = line.strip()
        if not skip and stripped.startswith(host_prefix) and stripped.endswith("{"):
            skip  = True
            depth = 1
            continue
        if skip:
            depth += line.count("{") - line.count("}")
            if depth <= 0:
                skip = False
            continue
        result.append(line)
    return "\n".join(result)

# --- 1. Extract and clean user content inside the custom section ---
# Tunnel blocks are inserted just before the END marker (inside the custom
# section). They look like:  subdomain.DOMAIN {\n    reverse_proxy ...\n}
user_lines = []
if BEGIN in content and END in content:
    b_idx        = content.index(BEGIN)
    e_idx        = content.index(END) + len(END)
    section_body = content[b_idx + len(BEGIN) : content.index(END)]
    raw_lines    = section_body.split("\n")

    # Standard header comment lines inserted by Gopher — always remove these.
    skip_comments = {
        "# Everything below this line will NOT be overwritten on local setup.",
        "# Add any custom Caddy directives or site blocks here.",
        "# Everything below this line will NOT be overwritten.",
        "# Add your own Caddy site blocks here.",
    }

    if domain:
        # Remove Gopher-managed tunnel blocks whose header ends with .DOMAIN {
        # e.g.  photos.example.com {
        # This is intentionally strict (requires the line to END with .domain {)
        # so user blocks for unrelated domains are never accidentally removed.
        tunnel_header_re = re.compile(
            r'^[\w\-\*]+\.' + re.escape(domain) + r'\s*\{$'
        )
        filtered = []
        depth = 0
        skip  = False
        for line in raw_lines:
            stripped = line.strip()
            if stripped in skip_comments:
                continue
            if not skip and tunnel_header_re.match(stripped):
                skip  = True
                depth = 1
                continue
            if skip:
                depth += line.count("{") - line.count("}")
                if depth <= 0:
                    skip = False
                continue
            filtered.append(line)
        user_lines = filtered
    else:
        user_lines = [l for l in raw_lines if l.strip() not in skip_comments]

    # Remove the entire BEGIN…END block from content.
    before  = content[:b_idx].rstrip()
    after   = content[e_idx:].lstrip("\n")
    content = (before + "\n" + after) if after else (before + "\n")

# --- 2. Remove Gopher's router dashboard block (lives above the custom section) ---
if domain:
    content = remove_block(content, f"router.{domain}")

# --- 3. Re-attach preserved user content (if any non-blank lines survived) ---
preserved = "\n".join(user_lines).strip()
if preserved:
    content = content.rstrip("\n") + "\n\n" + preserved + "\n"

content = content.strip() + "\n"

with open(path, "w") as fh:
    fh.write(content)

print("    Caddyfile cleaned.")
PYEOF
}

# strip_rathole_config PATH
#   Strips Gopher-managed entries from /etc/rathole/server.toml.
#   Gopher entries sit ABOVE the custom section; the custom section is preserved.
strip_rathole_config() {
    local path="$1"
    sudo_run python3 - "$path" <<'PYEOF'
import sys, re

path = sys.argv[1]

BEGIN = "# ===== BEGIN CUSTOM CONFIGURATION ====="
END   = "# ===== END CUSTOM CONFIGURATION ====="

# Match both new short 16-char hex tokens and old full UUIDs (backward compat).
SHORT_HEX = re.compile(r'^[0-9a-f]{16}$')
UUID_PAT  = re.compile(
    r'^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
)

def is_gopher_section(line):
    s = line.strip()
    # Remove the harmless placeholder Gopher injects when no services are active.
    if s == "[server.services.placeholder]":
        return True
    if s.startswith("[server.services.machine-") and s.endswith("-ssh]"):
        tok = s[len("[server.services.machine-"):-len("-ssh]")]
        return bool(SHORT_HEX.match(tok) or UUID_PAT.match(tok))
    if s.startswith("[server.services.tunnel-") and s.endswith("]"):
        tok = s[len("[server.services.tunnel-"):-1]
        return bool(SHORT_HEX.match(tok) or UUID_PAT.match(tok))
    return False

def strip_gopher_sections(text):
    lines  = text.split("\n")
    result = []
    skip   = False
    for line in lines:
        s = line.strip()
        if is_gopher_section(s):
            skip = True
            continue
        if skip and s.startswith("["):
            skip = False
        if not skip:
            result.append(line)
    return "\n".join(result)

with open(path) as fh:
    content = fh.read()

# Split: header zone (above BEGIN) + custom section (BEGIN onward, verbatim).
# Gopher-managed service entries live only in the header zone.
# The custom section is user-owned and must not be modified.
custom_section = ""
if BEGIN in content:
    b_idx          = content.index(BEGIN)
    custom_section = content[b_idx:]   # includes BEGIN…END and any trailing text
    content        = content[:b_idx]

# Remove only Gopher's managed service blocks from the header zone.
header = strip_gopher_sections(content).rstrip("\n") + "\n"

# Reassemble: clean header + custom section.
if custom_section.strip():
    result = header + "\n" + custom_section
else:
    result = header

with open(path, "w") as fh:
    fh.write(result.strip() + "\n")

print("    server.toml cleaned.")
PYEOF
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

require_sudo

echo ""
echo "========================================"
echo "  Gopher Uninstall"
echo "========================================"
echo ""

# --- Step 1: Stop/remove gopher systemd service (if installed as one) -------
echo "[1/5] Stopping Gopher service..."
if systemctl list-units --full --all 2>/dev/null | grep -q 'gopher.service'; then
    sudo_run systemctl stop gopher.service    2>/dev/null || true
    sudo_run systemctl disable gopher.service 2>/dev/null || true
    success "gopher.service stopped and disabled."
fi
if [[ -f /etc/systemd/system/gopher.service ]]; then
    sudo_run rm -f /etc/systemd/system/gopher.service
    sudo_run systemctl daemon-reload
    success "gopher.service unit removed."
fi
# Also terminate any gopher process running directly (e.g. sudo ./gopher).
sudo_run pkill -x gopher 2>/dev/null || true

# --- Step 2: Remove the gopher binary ---------------------------------------
echo "[2/5] Removing Gopher binary..."
removed=false
for bin_path in /usr/local/bin/gopher /usr/bin/gopher; do
    if [[ -f "$bin_path" ]]; then
        sudo_run rm -f "$bin_path"
        success "Removed $bin_path"
        removed=true
    fi
done
if [[ "$removed" == "false" ]]; then
    info "Gopher binary not found in standard locations; skipping."
fi

# --- Step 3: Caddy cleanup --------------------------------------------------
echo "[3/5] Cleaning up Caddy..."
if [[ "$REMOVE_CADDY" == "true" ]]; then
    sudo_run systemctl stop    caddy 2>/dev/null || true
    sudo_run systemctl disable caddy 2>/dev/null || true
    if command -v apt-get &>/dev/null; then
        sudo_run apt-get purge -y caddy 2>/dev/null || true
    fi
    sudo_run rm -rf /etc/caddy
    success "Caddy fully removed."
elif [[ -f /etc/caddy/Caddyfile ]]; then
    info "Backing up /etc/caddy/Caddyfile → Caddyfile.gopher-backup"
    sudo_run cp /etc/caddy/Caddyfile /etc/caddy/Caddyfile.gopher-backup
    strip_caddyfile /etc/caddy/Caddyfile "$DOMAIN"
    sudo_run systemctl reload caddy 2>/dev/null || true
    success "Caddyfile restored (backup at /etc/caddy/Caddyfile.gopher-backup)."
else
    info "No Caddyfile found; skipping."
fi

# --- Step 4: Rathole cleanup ------------------------------------------------
echo "[4/5] Cleaning up rathole..."
if [[ "$REMOVE_RATHOLE" == "true" ]]; then
    sudo_run systemctl stop    rathole-server 2>/dev/null || true
    sudo_run systemctl disable rathole-server 2>/dev/null || true
    sudo_run rm -f /etc/systemd/system/rathole-server.service
    sudo_run systemctl daemon-reload 2>/dev/null || true
    sudo_run rm -f /usr/local/bin/rathole
    sudo_run rm -rf /etc/rathole
    success "rathole fully removed."
elif [[ -f /etc/rathole/server.toml ]]; then
    info "Backing up /etc/rathole/server.toml → server.toml.gopher-backup"
    sudo_run cp /etc/rathole/server.toml /etc/rathole/server.toml.gopher-backup
    strip_rathole_config /etc/rathole/server.toml
    # Send SIGHUP to reload config in-place; fall back to restart.
    sudo_run pkill -HUP -x rathole 2>/dev/null || sudo_run systemctl restart rathole-server 2>/dev/null || true
    success "server.toml restored (backup at /etc/rathole/server.toml.gopher-backup)."
else
    info "No server.toml found; skipping."
fi

# --- Step 5: Remove Gopher database -----------------------------------------
echo "[5/5] Gopher database..."
if [[ -f "$DB_PATH" ]]; then
    if [[ "$YES" == "true" ]]; then
        confirm="y"
    else
        read -r -p "  Remove Gopher database at '$DB_PATH'? [y/N] " confirm || confirm="n"
    fi
    if [[ "${confirm,,}" == "y" ]]; then
        rm -f "$DB_PATH"
        success "Database removed."
    else
        info "Database kept at $DB_PATH."
    fi
else
    info "Database not found at $DB_PATH; skipping."
fi

echo ""
echo "========================================"
echo "  Gopher uninstallation complete."
echo "========================================"
echo ""
