package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	osuser "os/user"
	"strings"

	"github.com/smalex-z/gopher/internal/build"
	"github.com/smalex-z/gopher/internal/embedbin"
	"github.com/smalex-z/gopher/internal/paths"
	"github.com/smalex-z/gopher/internal/service"
	"github.com/smalex-z/gopher/internal/supervisor"
)

// startBundledChildren extracts the embedded caddy/rathole and brings them up
// under a supervisor that gopher owns. It returns (nil, nil) — "not
// supervising" — unless ALL of the following hold, so it's safe to compile in
// and to run tests against a live edge:
//
//  1. GOPHER_MANAGED=1 — set only by the systemd unit (see install.go). `go
//     test`, `go run`, and stray executions don't set it, so they never run the
//     DESTRUCTIVE legacy migration or fight a live edge's services.
//  2. Embedded() — real binaries were compiled in (not a dev/IDE build).
//  3. The consolidated /etc/gopher config exists — created by the migration
//     below or a fresh install; until then there's nothing valid to run, so we
//     leave caddy/rathole to their current manager.
//
// caddy must bind 80/443; gopher runs unprivileged and spawns it as a child, so
// the cap is granted on the extracted binary via setcap — no unit change or
// self-restart needed (requires NoNewPrivileges unset on gopher.service, which
// it is).
// migrateEdgeLayoutIfManaged runs the legacy -> /etc/gopher migration on a
// managed, embedded edge. It MUST be called before the boot-time reconcile so
// the reconcile reads the migrated config (preserving the user's custom blocks)
// rather than an empty tree, and so Caddy's certs are in place before the
// supervised Caddy starts. Idempotent (marker-gated inside the service).
func migrateEdgeLayoutIfManaged() {
	if os.Getenv("GOPHER_MANAGED") != "1" || !embedbin.Embedded() {
		return
	}
	if migrated, err := service.MigrateEdgeLayout(os.Stdout); err != nil {
		log.Printf("edge layout migration: %v", err)
	} else if migrated {
		log.Printf("migrated legacy edge layout to %s", paths.ConfigDir)
	}
}

func startBundledChildren() (*supervisor.Supervisor, error) {
	if os.Getenv("GOPHER_MANAGED") != "1" || !embedbin.Embedded() {
		return nil, nil
	}

	if _, err := os.Stat(paths.RatholeConfig); err != nil {
		log.Printf("supervisor: %s not present yet — leaving caddy/rathole to their current manager", paths.RatholeConfig)
		return nil, nil
	}

	// A box whose /opt/gopher predates the current install convention (never
	// went through gopher install's chownRecursive, or was hand-set-up long
	// ago) has BinDir's parent owned root:root — gopher can read/exec into
	// it but can't create bin/ itself. That's not a one-off: it's the exact
	// same "state gopher assumes but was never actually put in place" class
	// as the GOPHER_MANAGED gap, just for a directory instead of an env var.
	// Fix it here, every start, the same sudo any gopher install already
	// has everywhere — not a manual chown someone has to SSH in for.
	if err := ensureDirWritableBySelf(paths.BinDir); err != nil {
		return nil, fmt.Errorf("ensure %s is writable: %w", paths.BinDir, err)
	}
	if err := embedbin.ExtractAll(paths.BinDir, embedbin.RunBundle()); err != nil {
		return nil, err
	}
	// File capability so the unprivileged, gopher-spawned caddy can bind 80/443.
	// Re-applied each start because extraction (temp + rename) drops file caps.
	if out, err := exec.Command("sudo", "-n", "setcap", "cap_net_bind_service=+ep", paths.CaddyBin).CombinedOutput(); err != nil { // #nosec G204 — fixed args
		log.Printf("supervisor: setcap on %s failed (caddy may not bind 80/443): %v: %s", paths.CaddyBin, err, out)
	}

	sup := supervisor.New(os.Stdout,
		supervisor.Spec{
			Name: "caddy",
			Path: paths.CaddyBin,
			Args: []string{"run", "--config", paths.CaddyfilePath, "--adapter", "caddyfile"},
			// Caddy stores ACME certs/data under $XDG_DATA_HOME/caddy, keeping
			// them in /var/lib/gopher/caddy (= paths.CaddyData). Also pin
			// XDG_CONFIG_HOME here: unset, Caddy's config-autosave falls
			// back to $HOME/.config (= paths.Root/.config on this user),
			// which hits the exact same not-actually-owned-by-gopher wall as
			// BinDir above on an unmigrated box — redirecting it into
			// StateDir (already proven writable; it's where certs live)
			// avoids the failure instead of needing its own chown fix.
			Env: []string{"XDG_DATA_HOME=" + paths.StateDir, "XDG_CONFIG_HOME=" + paths.StateDir},
		},
		supervisor.Spec{
			Name: "rathole",
			Path: paths.RatholeBin,
			Args: []string{paths.RatholeConfig}, // server mode auto-detected from [server] config
		},
	)
	log.Printf("supervisor: starting bundled caddy %s + rathole %s as children", build.CaddyVersion, build.RatholeVersion)
	sup.Start(context.Background())
	return sup, nil
}

// ensureDirWritableBySelf makes dir exist and be owned by the current user,
// escalating via sudo only if the plain attempt fails. The fast path costs
// nothing on every normal start (a fresh gopher install already chowns this
// tree — cmd/server/install.go's chownRecursive); the sudo fallback is what
// makes a box that skipped that step (or predates it entirely) self-heal
// instead of failing startup and needing a manual chown.
func ensureDirWritableBySelf(dir string) error {
	if err := os.MkdirAll(dir, 0755); err == nil {
		return nil
	}
	u, err := osuser.Current()
	if err != nil {
		return fmt.Errorf("resolve current user: %w", err)
	}
	if out, err := exec.Command("sudo", "-n", "mkdir", "-p", dir).CombinedOutput(); err != nil { // #nosec G204 — fixed args
		return fmt.Errorf("sudo mkdir -p %s: %w (%s)", dir, err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("sudo", "-n", "chown", u.Username+":"+u.Username, dir).CombinedOutput(); err != nil { // #nosec G204 — fixed args
		return fmt.Errorf("sudo chown %s %s: %w (%s)", u.Username, dir, err, strings.TrimSpace(string(out)))
	}
	return nil
}
