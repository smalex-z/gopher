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
func (s *BootstrapService) RecoverClientConfig(agentToken, requestHost string) (string, *db.Machine, error) {
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
	return toml, machine, nil
}
