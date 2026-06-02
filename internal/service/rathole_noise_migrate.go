package service

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"

	"github.com/smalex-z/gopher/internal/config"
	"github.com/smalex-z/gopher/internal/db"
)

const ratholeServerTomlPath = "/etc/rathole/server.toml"

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

// MigrateRatholeNoise is the one-shot upgrade migration that hot-converts a
// running install from rathole's plaintext TCP transport to encrypted noise.
//
// The order is load-bearing:
//
//  1. Generate the keypair. Any subsequent server-config rebuild (including
//     one triggered by another goroutine) will start emitting [server.transport].
//     We do NOT reconcile yet — flipping the server first would sever every
//     plaintext client and the subsequent client.toml push wouldn't have a
//     working tunnel to ride on.
//
//  2. Push a fresh client.toml to every machine over the still-working
//     plaintext tunnel. mergeClientManagedConfig already re-reads the noise
//     pubkey from settings and emits [client.transport], so each pushed
//     config is noise-ready the moment rathole-client's notify watcher fires.
//     A client that switches to noise before the server does will fail-reconnect
//     every 5s — acceptable, the next step fixes it within seconds.
//
//  3. Reconcile server.toml so the server starts speaking noise. Clients that
//     just received their updated config reconnect; the worst-case outage per
//     machine is one rathole reconnect cycle (~5–10s).
//
// Offline machines are logged and skipped — their plaintext client.toml will
// fail to connect once step 3 completes, and the operator has to re-bootstrap
// them or trigger any config-touching action (which calls
// mergeClientManagedConfig) once they're reachable again. We do not retry
// here because retrying inside startup would block the dashboard from
// coming up.
func (s *LocalSetupService) MigrateRatholeNoise() error {
	if devMode {
		return nil
	}

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

	log.Printf("rathole noise migration: starting (this install is on plaintext rathole transport, upgrading to encrypted)")

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
		log.Printf("rathole noise migration: detected %d user-managed services in /etc/rathole/server.toml custom block — these need a manual client.toml update with the noise pubkey or they'll fail to reconnect after the server flips:", len(customServices))
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

	machines, err := db.GetMachines()
	if err != nil {
		return fmt.Errorf("load machines: %w", err)
	}

	// Push to every machine BEFORE reconciling the server, so each client
	// is already holding the noise pubkey when the server flips.
	pushed, failed := 0, 0
	for i := range machines {
		m := &machines[i]
		if m.TunnelPort == 0 || m.RatholeSSHToken == "" {
			continue // unbootstrapped row, nothing to push
		}
		machineTunnels, terr := db.GetTunnelsByMachine(m.ID)
		if terr != nil {
			log.Printf("rathole noise migration: skip %s (%s): load tunnels: %v", m.ID, m.Name, terr)
			failed++
			continue
		}
		ratholeHost := ratholeHostFromSettings(settings)
		transformer := func(existing string) (string, error) {
			return mergeClientManagedConfig(existing, m, machineTunnels, ratholeHost, noisePub)
		}
		if perr := s.updateClientToml(m, transformer); perr != nil {
			log.Printf("rathole noise migration: push to %s (%s) failed: %v — flagged for retry on next health-check reconnect", m.ID, m.Name, perr)
			// Mark for the health-loop retry path. When the machine next
			// becomes reachable (operator frees disk / brings it back online),
			// HealthService re-attempts the push and clears the flag on
			// success. Without this flag, the only recovery is operator-
			// triggered (re-bootstrap, manual edit, or the new /rathole-config
			// recovery script).
			if cerr := db.SetMachineConfigPushPending(m.ID, true); cerr != nil {
				log.Printf("rathole noise migration: set config_push_pending for %s: %v", m.ID, cerr)
			}
			failed++
			continue
		}
		pushed++
	}

	// Step 3: flip the server. Any client that received step-2 config
	// reconnects within one rathole retry cycle; any client that didn't
	// drops offline (and was already failing the push above, so it's
	// already broken from the operator's perspective).
	if err := s.ReconcileServerConfig(); err != nil {
		return fmt.Errorf("reconcile server after noise migration: %w", err)
	}

	log.Printf("rathole noise migration: complete (%d machines updated, %d offline/failed)", pushed, failed)
	return nil
}
