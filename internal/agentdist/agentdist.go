// Package agentdist is the runtime registry of sha256 hashes for the
// gopher-agent binaries embedded in cmd/server. The binaries themselves are
// //go:embed'ed in package main (cmd/server/agents.go) where library code
// can't reach them, so main computes their hashes once at startup and
// publishes them here for the two consumers that need to hand out an
// authoritative checksum over an authenticated channel:
//
//   - AgentInstaller.UpgradeAgent puts them in the bearer-authed /self-update
//     trigger body (which rides the noise-encrypted rathole back-channel), so
//     the agent can verify the binary it then downloads over a channel it
//     does NOT trust (TLS verification is skipped there for IP/self-signed
//     edge certs — see cmd/agent/selfupdate.go).
//   - The bootstrap.sh / migrate.sh renderers inject them into the script
//     text, which the operator fetched TLS-verified — so the scripts'
//     cert-tolerant download fallbacks stay integrity-checked.
//
// Empty registry (dev builds without staged agents) disables both: consumers
// fall back to the legacy same-channel .sha256 sidecar.
package agentdist

import "sync"

var (
	mu     sync.RWMutex
	hashes map[string]string // arch tag ("amd64", "arm64", "armv7") → hex sha256
)

// SetHashes publishes the embedded agent binary hashes. Called once from
// cmd/server startup; later calls replace the map wholesale.
func SetHashes(m map[string]string) {
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	mu.Lock()
	hashes = cp
	mu.Unlock()
}

// All returns a copy of the registry; empty when no agent binaries are
// embedded (dev builds).
func All() map[string]string {
	mu.RLock()
	defer mu.RUnlock()
	cp := make(map[string]string, len(hashes))
	for k, v := range hashes {
		cp[k] = v
	}
	return cp
}
