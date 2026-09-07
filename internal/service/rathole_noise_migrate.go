package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/config"
	"github.com/smalex-z/gopher/internal/db"
	"github.com/smalex-z/gopher/internal/paths"
	sshpkg "github.com/smalex-z/gopher/internal/ssh"
)

// Overridable in tests so the fleet-wait path can be exercised without
// actually sleeping for minutes.
var (
	// noiseMigrationFleetWait bounds how long the migration waits for the
	// fleet to come back after a restart before giving up FOR THIS BOOT. The
	// upgrade path restarts gopher, which restarts the supervised rathole,
	// which drops every machine's control channel; clients reconnect within
	// seconds, but a loaded or slow origin can take longer. Giving up is not
	// a failure — the migration simply defers to the next boot, leaving the
	// install exactly as it was (plaintext, everything working).
	noiseMigrationFleetWait = 5 * time.Minute
	// noiseMigrationProbeInterval is how often the fleet is re-probed while
	// waiting for machines to reconnect.
	noiseMigrationProbeInterval = 10 * time.Second
	// noiseMigrationProbeTimeout bounds a single machine's reachability probe.
	noiseMigrationProbeTimeout = 10 * time.Second
	// noiseMigrationSettleWait bounds how long the migration waits AFTER the
	// server flips for the fleet to come back on the encrypted transport. A
	// client that received the new config reconnects within one rathole retry
	// cycle, so this only needs to cover a slow origin. Anything still missing
	// when it expires means the config did not actually take effect out there
	// and the migration rolls the whole install back to plaintext.
	noiseMigrationSettleWait = 90 * time.Second
)

var ratholeServerTomlPath = paths.RatholeConfig

// detectCustomRatholeServices reads the rathole server config and returns the
// names of [server.services.X] sections inside the BEGIN/END CUSTOM
// CONFIGURATION marker block. These are services the operator added by hand
// (the documented escape hatch) — they're NOT in Gopher's DB and therefore
// CAN'T be reached by the migration's automatic config push. They need a
// manual update to the client side or they silently break on noise flip.
//
// Returns an empty slice (not nil) when:
//   - the file doesn't exist (pre-install)
//   - the markers are missing (fresh install before first reconcile)
//   - the custom block is present but empty
//
// Parser is intentionally simple: marker-delimited substring + a regex for
// section headers. We don't want a full TOML parse here because the custom
// block can legitimately contain syntax that hasn't been validated yet
// (operator typing it in via the dashboard) and we don't want to refuse to
// surface a warning just because their custom block has a stray bracket.
func detectCustomRatholeServices(path string) []string {
	body, err := os.ReadFile(path) // #nosec G304 — fixed path
	if err != nil {
		return []string{}
	}
	const beginMarker = "# ===== BEGIN CUSTOM CONFIGURATION ====="
	const endMarker = "# ===== END CUSTOM CONFIGURATION ====="
	content := string(body)
	bIdx := strings.Index(content, beginMarker)
	if bIdx == -1 {
		return []string{}
	}
	below := content[bIdx+len(beginMarker):]
	eIdx := strings.Index(below, endMarker)
	if eIdx == -1 {
		// Marker malformed — treat the whole tail as the custom block. Bias
		// toward over-reporting rather than missing a custom service.
		eIdx = len(below)
	}
	customBody := below[:eIdx]

	// Match [server.services.<name>] section headers. Only inside the custom
	// block; gopher-managed sections live above the BEGIN marker and won't
	// be parsed here.
	re := regexp.MustCompile(`(?m)^\s*\[server\.services\.([A-Za-z0-9_\-]+)\]\s*$`)
	matches := re.FindAllStringSubmatch(customBody, -1)
	out := make([]string, 0, len(matches))
	seen := map[string]bool{}
	for _, m := range matches {
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// RetryPendingConfigPush is the ConfigPusher hook the health service calls
// when a machine that previously failed a push (e.g. during the noise
// migration) becomes reachable again. Replays the same merge logic the
// migration would have used — current DB state + current settings — so
// regardless of how stale the machine's local config is, one successful
// retry brings it fully in sync.
//
// On success, the push pipeline (updateClientToml) clears the flag. On
// failure, the flag stays set and the health service will retry on the
// next reconnect.
func (s *LocalSetupService) RetryPendingConfigPush(machine *db.Machine) error {
	if machine == nil {
		return fmt.Errorf("nil machine")
	}
	settings, err := db.GetSettings()
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	machineTunnels, err := db.GetTunnelsByMachine(machine.ID)
	if err != nil {
		return fmt.Errorf("load tunnels: %w", err)
	}
	ratholeHost := ratholeHostFromSettings(settings)
	noisePub := settings.RatholeNoisePubKey
	transformer := func(existing string) (string, error) {
		return mergeClientManagedConfig(existing, machine, machineTunnels, ratholeHost, noisePub)
	}
	return s.updateClientToml(machine, transformer)
}

// EnsureRatholeNoiseKeys generates and persists a fresh X25519 keypair the
// first time it's called on a given install; subsequent calls are no-ops.
// Returns the (pub, priv) pair in base64 form regardless of whether they
// were freshly generated or already present.
//
// Uses MutateSettings so two concurrent callers can't both decide the keys
// are missing and race to generate two different pairs — the second caller
// observes the first's write inside the same SQLite transaction.
func EnsureRatholeNoiseKeys() (priv, pub string, err error) {
	err = db.MutateSettings(func(s *db.AppSettings) error {
		if s.RatholeNoisePrivKey != "" && s.RatholeNoisePubKey != "" {
			priv, pub = s.RatholeNoisePrivKey, s.RatholeNoisePubKey
			return nil
		}
		newPriv, newPub, gerr := config.GenerateNoiseKeypair()
		if gerr != nil {
			return fmt.Errorf("generate noise keypair: %w", gerr)
		}
		s.RatholeNoisePrivKey = newPriv
		s.RatholeNoisePubKey = newPub
		priv, pub = newPriv, newPub
		return nil
	})
	return priv, pub, err
}

// migratableMachines returns the machines the noise migration must carry
// across the transport flip: anything actually bootstrapped. A machine with
// neither an SSH tunnel port nor an agent port has never completed bootstrap,
// so there is no client.toml out there to break.
func migratableMachines(machines []db.Machine) []*db.Machine {
	out := make([]*db.Machine, 0, len(machines))
	for i := range machines {
		m := &machines[i]
		if m.TunnelPort == 0 && m.AgentRemotePort == 0 {
			continue
		}
		out = append(out, m)
	}
	return out
}

// probeMachineReachable reports whether the server can currently reach the
// machine's origin through the tunnel — read-only, no config is written.
//
// This is the precondition the migration's push step needs, so it must mirror
// updateClientToml's transport selection EXACTLY: agent gRPC first, then SSH
// over the tunnel. Probing the agent alone would call a perfectly reachable
// machine unreachable whenever its agent is down or stale but SSH still works
// — and since an unreachable machine defers the whole migration, an agent-only
// probe would block the upgrade forever on a machine the push could have
// handled fine.
//
// It deliberately does not settle for a TCP dial to the forwarded port:
// rathole binds a service's listener whether or not a client holds the
// control channel, so a successful dial proves nothing about the origin
// actually being there.
func (s *LocalSetupService) probeMachineReachable(m *db.Machine) error {
	var agentErr error
	if m.AgentInstalled && m.AgentRemotePort > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), noiseMigrationProbeTimeout)
		defer cancel()
		if _, err := NewAgentClient(m).GetRatholeConfig(ctx); err == nil {
			return nil
		} else {
			agentErr = err
		}
	}

	// SSH fallback — the same one the push falls back to.
	sshKey, err := db.GetSSHKeyForMachine(m)
	if err != nil || sshKey.PrivateKey == "" {
		if agentErr != nil {
			return fmt.Errorf("agent unreachable (%v) and no stored SSH private key to fall back on", agentErr)
		}
		return fmt.Errorf("no agent and no stored SSH private key — cannot reach this machine to migrate it")
	}
	client, err := sshpkg.NewClient(TunnelDialHost(m), m.TunnelPort, m.Username, sshKey.PrivateKey)
	if err != nil {
		if agentErr != nil {
			return fmt.Errorf("agent unreachable (%v) and ssh unreachable: %w", agentErr, err)
		}
		return fmt.Errorf("ssh unreachable: %w", err)
	}
	_ = client.Close()
	return nil
}

// awaitFleetReachable blocks until every machine answers a reachability probe
// or the deadline passes, and returns the machines still unreachable.
//
// Called WITHOUT reconcileMu held: this can wait minutes, and holding the
// reconcile lock that long would block every dashboard action that touches
// rathole config (tunnel create, bootstrap, machine delete).
func (s *LocalSetupService) awaitFleetReachable(fleet []*db.Machine, within time.Duration) []string {
	deadline := time.Now().Add(within)
	pending := append([]*db.Machine(nil), fleet...)
	for {
		var stillPending []*db.Machine
		var reasons []string
		for _, m := range pending {
			if err := s.probeMachineReachable(m); err != nil {
				stillPending = append(stillPending, m)
				reasons = append(reasons, fmt.Sprintf("%s (%v)", m.Name, err))
			}
		}
		if len(stillPending) == 0 {
			return nil
		}
		log.Printf("rathole noise migration: waiting for %d machine(s) to come back: %s", len(stillPending), strings.Join(reasons, "; "))
		if time.Now().After(deadline) {
			names := make([]string, 0, len(stillPending))
			for _, m := range stillPending {
				names = append(names, m.Name)
			}
			log.Printf("rathole noise migration: still unreachable after %s: %s", within, strings.Join(reasons, "; "))
			return names
		}
		pending = stillPending
		time.Sleep(noiseMigrationProbeInterval)
	}
}

// recordNoiseMigrationBlocked persists (or clears) the list of machines that
// blocked the migration, so the dashboard can tell the operator exactly which
// origins to bring back — and that nothing has been changed in the meantime.
func recordNoiseMigrationBlocked(names []string) {
	payload := ""
	if len(names) > 0 {
		if b, err := json.Marshal(names); err == nil {
			payload = string(b)
		}
	}
	if err := db.MutateSettings(func(a *db.AppSettings) error {
		a.RatholeNoiseBlockedMachines = payload
		return nil
	}); err != nil {
		log.Printf("rathole noise migration: persist blocked-machine list: %v", err)
	}
}

// clearRatholeNoiseKeys removes a keypair minted for a migration that then had
// to be abandoned. Critical on the abort path: ReconcileServerConfig emits the
// [server.transport] noise block whenever the private key is non-empty, so
// leaving a key behind would let the very next unrelated reconcile flip the
// server to noise with the fleet still on plaintext — the exact mass-outage
// this migration exists to avoid.
func clearRatholeNoiseKeys() {
	if err := db.MutateSettings(func(a *db.AppSettings) error {
		a.RatholeNoisePrivKey = ""
		a.RatholeNoisePubKey = ""
		return nil
	}); err != nil {
		log.Printf("rathole noise migration: roll back noise keypair: %v", err)
	}
}

// rollbackNoisePushes re-pushes a plaintext client.toml to machines that
// already received the noise config before the migration aborted. Without
// this they would sit holding a noise config while the server stays plaintext
// and fail to reconnect every few seconds — broken by a migration that never
// even completed. Best-effort: a machine that has gone unreachable in the
// meantime keeps its ConfigPushPending flag and is repaired by the health
// loop's retry.
func (s *LocalSetupService) rollbackNoisePushes(pushed []*db.Machine, ratholeHost string) {
	for _, m := range pushed {
		machineTunnels, terr := db.GetTunnelsByMachine(m.ID)
		if terr != nil {
			log.Printf("rathole noise migration: rollback %s: load tunnels: %v", m.Name, terr)
			continue
		}
		transformer := func(existing string) (string, error) {
			return mergeClientManagedConfig(existing, m, machineTunnels, ratholeHost, "")
		}
		if err := s.updateClientToml(m, transformer); err != nil {
			log.Printf("rathole noise migration: rollback %s to plaintext failed: %v", m.Name, err)
		}
	}
}

// MigrateRatholeNoise is the one-shot upgrade migration that converts a
// running install from rathole's plaintext TCP transport to encrypted noise.
//
// The invariant: an upgrade must never leave a machine stranded. A plaintext
// client cannot talk to a noise server and vice versa, so the fleet can only
// move as a unit. Therefore the migration is all-or-nothing:
//
//  1. Wait (unlocked) for every bootstrapped machine to be reachable. The
//     upgrade just restarted rathole, so the fleet is mid-reconnect; probing
//     immediately is meaningless.
//
//  2. If any machine is still unreachable, ABORT before minting keys or
//     touching a single config. The install stays exactly as it was —
//     plaintext, every tunnel up — and the blocked machines are recorded for
//     the dashboard so the operator can bring them back (or run migrate.sh on
//     them). The migration retries on the next boot.
//
//  3. Otherwise mint the keypair and push a noise-ready client.toml to every
//     machine over the still-working plaintext tunnel. If ANY push fails,
//     roll the already-pushed machines back to plaintext, drop the keypair,
//     and abort without flipping.
//
//  4. Only once every machine has confirmed its new config, reconcile
//     server.toml so the server speaks noise. Worst case per machine is a
//     single rathole reconnect cycle (~5-10s).
//
// Steps 3 and 4 hold reconcileMu for their whole duration. Without that, an
// unrelated concurrent caller (a tunnel create, a new bootstrap — anything
// the already-live dashboard can trigger) could call ReconcileServerConfig
// after the key is committed but before the fleet is pushed, flipping the
// server early and mass-disconnecting every machine not yet reached.
//
// This function MUST be called after the supervisor has started rathole. It
// used to run during the startup reconcile, roughly 200ms before rathole
// existed, so every push failed instantly against a dead tunnel and step 4
// then dropped the entire fleet on every single upgrade.
func (s *LocalSetupService) MigrateRatholeNoise() error {
	if devMode {
		return nil
	}

	// Cheap pre-checks before committing to a possibly multi-minute wait.
	settings, err := db.GetSettings()
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	// Pre-wizard installs have nothing to migrate — no server.toml exists yet,
	// no machines are registered. EnsureRatholeNoiseKeys will mint the keypair
	// on first ReconcileServerConfig after the wizard finishes.
	if !settings.IsSetup {
		return nil
	}
	if settings.RatholeNoisePrivKey != "" {
		return nil // already migrated
	}

	machines, err := db.GetMachines()
	if err != nil {
		return fmt.Errorf("load machines: %w", err)
	}
	fleet := migratableMachines(machines)

	log.Printf("rathole noise migration: starting (this install is on plaintext rathole transport, upgrading to encrypted; %d machine(s) to carry across)", len(fleet))

	// Step 1+2: wait for the fleet, and defer the whole migration if anyone is
	// missing. Deliberately outside reconcileMu — see awaitFleetReachable.
	if blocked := s.awaitFleetReachable(fleet, noiseMigrationFleetWait); len(blocked) > 0 {
		recordNoiseMigrationBlocked(blocked)
		log.Printf("rathole noise migration: DEFERRED — %d machine(s) unreachable: %s", len(blocked), strings.Join(blocked, ", "))
		log.Printf("rathole noise migration: nothing was changed; the install stays on plaintext transport and every tunnel keeps working.")
		log.Printf("rathole noise migration: bring those machines online (or re-run migrate.sh on them) and the migration runs on the next restart.")
		return nil
	}

	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()

	// Re-read under the lock: another path may have migrated while we waited.
	settings, err = db.GetSettings()
	if err != nil {
		return fmt.Errorf("reload settings: %w", err)
	}
	if settings.RatholeNoisePrivKey != "" {
		return nil
	}

	// Detect user-managed services in server.toml's custom block BEFORE the
	// reconcile. Once the server flips to noise, their plaintext clients
	// will fail to reconnect — we want a paper trail (log + persisted
	// warning surfaced to the dashboard) so the operator knows what just
	// silently broke and can update those clients with the noise pubkey.
	customServices := detectCustomRatholeServices(ratholeServerTomlPath)

	_, noisePub, err := EnsureRatholeNoiseKeys()
	if err != nil {
		return fmt.Errorf("generate keys: %w", err)
	}

	if len(customServices) > 0 {
		log.Printf("rathole noise migration: detected %d user-managed services in the server.toml custom block — these need a manual client.toml update with the noise pubkey or they'll fail to reconnect", len(customServices))
		for _, name := range customServices {
			log.Printf("    [server.services.%s]", name)
		}
		log.Printf("rathole noise migration: noise public key for manual updates: %s", noisePub)

		// Persist for the dashboard banner. New warning supersedes any prior
		// dismissal — if there's something newly broken to surface, the
		// operator deserves a fresh banner.
		if payload, jerr := json.Marshal(customServices); jerr == nil {
			perr := db.MutateSettings(func(a *db.AppSettings) error {
				a.RatholeCustomServicesWarning = string(payload)
				a.RatholeCustomServicesWarningDismissed = false
				return nil
			})
			if perr != nil {
				log.Printf("rathole noise migration: persist custom-services warning: %v", perr)
			}
		}
	}

	ratholeHost := ratholeHostFromSettings(settings)

	// Step 3: push to every machine BEFORE reconciling the server, so each
	// client is already holding the noise pubkey when the server flips.
	pushed := make([]*db.Machine, 0, len(fleet))
	for _, m := range fleet {
		machineTunnels, terr := db.GetTunnelsByMachine(m.ID)
		if terr != nil {
			s.rollbackNoisePushes(pushed, ratholeHost)
			clearRatholeNoiseKeys()
			return fmt.Errorf("abort before flip: load tunnels for %s: %w", m.Name, terr)
		}
		transformer := func(existing string) (string, error) {
			return mergeClientManagedConfig(existing, m, machineTunnels, ratholeHost, noisePub)
		}
		if perr := s.updateClientToml(m, transformer); perr != nil {
			log.Printf("rathole noise migration: push to %s (%s) failed after it probed reachable: %v", m.ID, m.Name, perr)
			log.Printf("rathole noise migration: ABORTING before the server flip and rolling %d already-updated machine(s) back to plaintext — no tunnel is dropped", len(pushed))
			s.rollbackNoisePushes(pushed, ratholeHost)
			clearRatholeNoiseKeys()
			recordNoiseMigrationBlocked([]string{m.Name})
			return fmt.Errorf("abort before flip: push to %s: %w", m.Name, perr)
		}
		pushed = append(pushed, m)
	}

	// Step 4: every machine is holding a noise-ready config — flip the server.
	// reconcileServerConfigLocked, not ReconcileServerConfig: this function
	// already holds reconcileMu (see doc comment above) — calling the locking
	// wrapper here would deadlock on itself.
	if err := s.reconcileServerConfigLocked(); err != nil {
		return fmt.Errorf("reconcile server after noise migration: %w", err)
	}

	// Step 5: prove it actually worked.
	//
	// A push that returned success only means the file was written on the
	// origin — NOT that the running rathole-client adopted it. Clients rely on
	// an inotify hot-reload, and an origin whose rathole predates that (or
	// whose watcher is wedged) keeps talking plaintext to a server that now
	// speaks only noise. Without this check the migration cheerfully logged
	// "complete" while the entire fleet sat there failing handshakes — the
	// exact silent outage this whole function exists to prevent.
	//
	// So: watch for the fleet to come back, and if it doesn't, put the install
	// back the way it was. Reverting the server to plaintext is what actually
	// rescues a client that never adopted the new config, so it goes first.
	if stranded := s.awaitFleetReachable(fleet, noiseMigrationSettleWait); len(stranded) > 0 {
		log.Printf("rathole noise migration: ROLLING BACK — %d machine(s) did not come back on the encrypted transport: %s",
			len(stranded), strings.Join(stranded, ", "))
		log.Printf("rathole noise migration: their client.toml was written but the running rathole-client never picked it up (commonly an origin with an old agent/rathole that cannot hot-reload).")

		clearRatholeNoiseKeys()
		if rerr := s.reconcileServerConfigLocked(); rerr != nil {
			// Worst case in the whole function: the server is on noise, the
			// keys are gone, and we could not rewrite the config. Say so
			// loudly and precisely — this one needs hands.
			log.Printf("rathole noise migration: CRITICAL — could not revert server.toml to plaintext: %v", rerr)
			log.Printf("rathole noise migration: restart gopher to force a clean reconcile; the noise keypair has already been dropped so the rebuild will emit plaintext.")
			return fmt.Errorf("rollback failed: %w", rerr)
		}
		// Any machine that DID adopt noise is now the odd one out against a
		// plaintext server — hand it back a plaintext config. Best-effort:
		// unreachable ones keep ConfigPushPending and the health loop retries.
		s.rollbackNoisePushes(pushed, ratholeHost)

		recordNoiseMigrationBlocked(stranded)
		log.Printf("rathole noise migration: rolled back to plaintext transport — tunnels should recover on the next client reconnect. Re-run the agent install (migrate.sh) on the listed machines, then restart gopher to retry.")
		return fmt.Errorf("rolled back: %d machine(s) did not adopt the encrypted transport", len(stranded))
	}

	recordNoiseMigrationBlocked(nil)
	log.Printf("rathole noise migration: complete (%d machines migrated to encrypted transport)", len(pushed))
	return nil
}
