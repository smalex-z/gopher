package service

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/db"
)

// Client-config drift reconciliation — the server half of config self-healing.
//
// server.toml is rebuilt from the DB on every reconcile, but client.toml was
// only ever written on events: bootstrap, tunnel changes, the migrations, and
// the failed-push retry. Anything that changed the file OUTSIDE those events
// drifted forever while every health sensor stayed green — the agent checks
// process/unit/connection health (not content), the edge checks reachability
// (not parity), and no event fires. Observed in the field as a permanently
// offline tunnel on a perfectly healthy machine after a hand-edit; the same
// signature comes from a truncated write, a partially-restored backup, or a
// VM rolled back to an old snapshot.
//
// The sweep closes the loop: periodically, for each connected agent machine,
// fetch client.toml over the back-channel, recompute the canonical merge from
// the DB, and push only when the result differs — updateClientTomlViaAgent's
// no-op skip makes an in-sync sweep write-free, and the merge preserves the
// operator's custom sections by construction. Agent machines only: sweeping
// SSH-only machines would cost an SSH dial per machine per sweep; they keep
// the event-driven behavior.
//
// The complementary agent half (0.2.8 watchdog) covers drift the sweep can't
// reach: a config broken badly enough to sever the back-channel itself
// (corrupted remote_addr, deleted transport block) makes the agent refetch
// from the edge after repeated failed starts or wedge restarts.

// clientDriftSweepInterval is how often the parity sweep runs. The first
// sweep is delayed a full interval after startup so it never interleaves
// with the startup migrations' own config pushes.
const clientDriftSweepInterval = 5 * time.Minute

// ReconcileClientConfig fetches machine's live client.toml via the agent,
// recomputes the canonical merge from the DB, and pushes it back if it
// differs. Returns whether a semantic repair happened — a push that only
// normalized formatting (e.g. a bootstrap-written file converging to merge
// output) reports false so the sweep doesn't cry drift over whitespace.
func (s *LocalSetupService) ReconcileClientConfig(machine *db.Machine) (bool, error) {
	if machine == nil {
		return false, fmt.Errorf("nil machine")
	}
	settings, err := db.GetSettings()
	if err != nil {
		return false, fmt.Errorf("load settings: %w", err)
	}
	machineTunnels, err := db.GetTunnelsByMachine(machine.ID)
	if err != nil {
		return false, fmt.Errorf("load tunnels: %w", err)
	}
	ratholeHost := ratholeHostFromSettings(settings)

	semantic := false
	transform := func(existing string) (string, error) {
		merged, merr := mergeClientManagedConfig(existing, machine, machineTunnels, ratholeHost, settings.RatholeNoisePubKey)
		if merr != nil {
			return "", merr
		}
		if merged != existing {
			semantic = configContentDiffers(existing, merged)
		}
		return merged, nil
	}
	if err := s.updateClientTomlViaAgent(machine, transform); err != nil {
		return false, err
	}
	return semantic, nil
}

// configContentDiffers reports whether two configs differ in substance, not
// just formatting: it compares the multiset of non-empty, whitespace-trimmed
// lines. A removed tunnel section, an edited token or remote_addr, or
// truncated content all change the line set; re-ordered sections or collapsed
// blank lines don't.
func configContentDiffers(a, b string) bool {
	return normalizedConfigLines(a) != normalizedConfigLines(b)
}

func normalizedConfigLines(content string) string {
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}
