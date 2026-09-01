package service

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/smalex-z/gopher/internal/db"
)

// ErrUnknownAgentToken is returned by RecoverClientConfig when no machine
// matches the presented bearer token. Handlers map it to 401; anything else
// out of RecoverClientConfig is a server-side failure, not an auth failure.
var ErrUnknownAgentToken = errors.New("unknown agent token")

// RecoverClientConfig regenerates a machine's complete managed client.toml
// from the DB, authenticated by the machine's per-agent bearer token.
//
// This is the server half of the agent's dial-home recovery (agent 0.2.6+):
// when an origin's client.toml is missing or unrepairable, every inbound
// repair channel — gRPC config push, SSH — is gone with it, because they all
// ride the tunnel that file is the credentials for. The agent still holds its
// identity (config.env) and the edge is public, so the agent dials out for
// the authoritative copy. Managed sections only: custom user entries in the
// lost file are not in the DB and cannot be resurrected.
// requestHost is the Host the agent's recovery request arrived on (the
// handler's r.Host) and becomes the config's remote_addr — the same derivation
// bootstrap's Register uses. It must NOT come from settings.Domain: the agent
// provably reached us at requestHost (it's their persisted GOPHER_EDGE_URL,
// router.<domain> in a standard install), whereas the apex domain often points
// somewhere else entirely (an org's main site), which would hand back a config
// whose tunnel can never reconnect.
//
// currentConfig is the agent's on-disk config, when it still has one ("" for
// a missing file). The agent only dials home when that config is provably bad
// — missing, unparseable, or one rathole keeps failing/wedging on — so
// EVERYTHING managed is rebuilt from the DB, including the [client] block: a
// hand-corrupted remote_addr or a snapshot-stale token must not survive the
// way a normal event-push's merge would preserve them. Only the operator's
// custom sections are carried over — they exist nowhere but that file.
func (s *BootstrapService) RecoverClientConfig(agentToken, requestHost, currentConfig string) (string, *db.Machine, error) {
	machine, err := db.GetMachineByAgentToken(agentToken)
	if err != nil {
		return "", nil, ErrUnknownAgentToken
	}
	settings, err := db.GetSettings()
	if err != nil {
		return "", nil, fmt.Errorf("settings lookup: %w", err)
	}
	tunnels, err := db.GetTunnelsByMachine(machine.ID)
	if err != nil {
		return "", nil, fmt.Errorf("load tunnels for %s: %w", machine.ID, err)
	}
	ratholeHost := strings.TrimSpace(requestHost)
	if h, _, err := net.SplitHostPort(ratholeHost); err == nil {
		ratholeHost = h
	}
	if ratholeHost == "" {
		ratholeHost = ratholeHostFromSettings(settings)
	}
	toml, err := mergeClientManagedConfig("", machine, tunnels, ratholeHost, settings.RatholeNoisePubKey)
	if err != nil {
		return "", nil, fmt.Errorf("generate client config for %s: %w", machine.ID, err)
	}
	if custom := extractCustomClientServices(currentConfig); custom != "" {
		toml = strings.TrimRight(toml, "\n") + "\n\n" + custom + "\n"
	}
	return toml, machine, nil
}

// extractCustomClientServices returns only the operator's own
// [client.services.<name>] sections from a suspect config — the one thing in
// that file which exists nowhere else and is therefore worth carrying into a
// rebuild. Everything else is discarded ON PURPOSE. The previous approach —
// "keep whatever survives stripping the managed parts" — preserved corruption
// as faithfully as custom content: a garbage line has no section structure,
// survived every strip, got appended to the rebuilt config, and re-poisoned
// it (field test 2026-09-01: the refetch loop only converged because a
// strip-order accident swallowed the debris on the second pass). The agent
// sends this config precisely because rathole keeps rejecting it, so loose
// lines outside a custom section are debris until proven otherwise — and an
// operator's real escape-hatch config is exactly extra service sections.
func extractCustomClientServices(content string) string {
	var out []string
	keep := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			keep = strings.HasPrefix(trimmed, "[client.services.") &&
				!strings.HasPrefix(trimmed, "[client.services.tunnel-") &&
				!strings.HasPrefix(trimmed, "[client.services.machine-")
		}
		if keep {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
