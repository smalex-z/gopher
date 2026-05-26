package service

import (
	"fmt"
	"log"

	"github.com/smalex-z/gopher/internal/config"
	"github.com/smalex-z/gopher/internal/db"
)

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

	_, noisePub, err := EnsureRatholeNoiseKeys()
	if err != nil {
		return fmt.Errorf("generate keys: %w", err)
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
			log.Printf("rathole noise migration: push to %s (%s) failed: %v — machine will be unreachable after server flip until re-bootstrapped", m.ID, m.Name, perr)
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
