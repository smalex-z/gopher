package service

import (
	"fmt"
	"log"

	"github.com/smalex-z/gopher/internal/db"
)

// MigrateServerHostToRouter moves an existing edge off using the bare apex
// domain as the rathole transport host and onto router.<domain>.
//
// Why: ServerHost is the host baked into every client's `remote_addr` (and the
// jumpbox SSH host). Defaulting it to the apex couples every tunnel to whatever
// the operator does with their root domain — point the apex at a landing page
// or a redirect and all tunnels + jumpboxes die at once. router.<domain> already
// resolves to the edge via the same wildcard that serves the dashboard, so it's
// a free, stable home for the transport that the apex can't disturb.
//
// Idempotent and self-gating: it only acts when ServerHost is still exactly the
// bare Domain (a pre-router install). Fresh installs write router.<domain>
// directly, and an operator-set custom ServerHost is left alone. Once flipped,
// ServerHost != Domain and the routine no-ops forever — no marker file needed.
//
// The push is best-effort with the same retry semantics as the noise migration:
// a machine we can't reach gets ConfigPushPending set, and the health loop
// re-pushes when it reconnects. remote_addr is client-side only, so there is no
// server.toml reconcile to do here.
func (s *LocalSetupService) MigrateServerHostToRouter() error {
	settings, err := db.GetSettings()
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	// Gate: only a completed install whose ServerHost is still the bare apex.
	if !settings.LocalSetupDone || settings.Domain == "" || settings.ServerHost != settings.Domain {
		return nil
	}

	newHost := "router." + settings.Domain
	if err := db.MutateSettings(func(a *db.AppSettings) error {
		a.ServerHost = newHost
		return nil
	}); err != nil {
		return fmt.Errorf("persist ServerHost: %w", err)
	}
	settings.ServerHost = newHost // keep the local copy in sync for the pushes below
	log.Printf("server-host migration: ServerHost %s -> %s; pushing updated remote_addr to the fleet", settings.Domain, newHost)

	machines, err := db.GetMachines()
	if err != nil {
		return fmt.Errorf("load machines: %w", err)
	}

	ratholeHost := ratholeHostFromSettings(settings)
	pushed, failed := 0, 0
	for i := range machines {
		m := &machines[i]
		if m.TunnelPort == 0 && m.AgentRemotePort == 0 {
			continue // genuinely unbootstrapped; agent-only machines still get pushed via the agent
		}
		machineTunnels, terr := db.GetTunnelsByMachine(m.ID)
		if terr != nil {
			log.Printf("server-host migration: skip %s (%s): load tunnels: %v", m.ID, m.Name, terr)
			failed++
			continue
		}
		transformer := func(existing string) (string, error) {
			return mergeClientManagedConfig(existing, m, machineTunnels, ratholeHost, settings.RatholeNoisePubKey)
		}
		if perr := s.updateClientToml(m, transformer); perr != nil {
			log.Printf("server-host migration: push to %s (%s) failed: %v — flagged for retry on next reconnect", m.ID, m.Name, perr)
			if cerr := db.SetMachineConfigPushPending(m.ID, true); cerr != nil {
				log.Printf("server-host migration: set config_push_pending for %s: %v", m.ID, cerr)
			}
			failed++
			continue
		}
		pushed++
	}
	log.Printf("server-host migration: complete — %d pushed, %d flagged for retry", pushed, failed)
	return nil
}
