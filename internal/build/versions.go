package build

import "strings"

// Pinned versions of the external binaries Gopher installs. These are the
// single source of truth — install code (Go) and rendered install scripts both
// read from here. Bump them ONLY in a release, after testing the newer version,
// so fresh installs are reproducible and never surprised by an upstream breaking
// release.
const (
	// CaddyVersion is the apt/yum package version installed on the edge
	// (e.g. "apt-get install caddy=<CaddyVersion>"). Must exist in the Caddy
	// cloudsmith repo.
	CaddyVersion = "2.10.2"

	// RatholeVersion is the rathole release tag downloaded for the edge and
	// origins. The deployed build must carry the noise + notify features Gopher
	// relies on; do not move off a tested tag casually.
	RatholeVersion = "v0.5.0"

	// RatholeRepo is the GitHub repo rathole releases are fetched from.
	RatholeRepo = "rathole-org/rathole"

	// AgentVersion is the gopher-agent build version. It is the single source
	// of truth for both cmd/agent (what the running agent reports) and
	// internal/service/health.go's targetAgentVersion (what the edge expects
	// — a reachable agent reporting anything older gets auto-upgraded).
	// Previously these were two separately-maintained constants that had to be
	// bumped in lockstep by hand; a real incident shipped a agent-side fix
	// without bumping either, so the edge's version comparison saw no change
	// and never pushed the fix to any already-bootstrapped machine, with zero
	// error signal. Collapsing them to one constant makes that class of bug
	// structurally impossible rather than relying on a human remembering.
	//
	// Bump this whenever cmd/agent's behavior changes in a way the edge should
	// know about (see cmd/agent/main.go's per-version changelog comment).
	AgentVersion = "0.2.6"
)

// InjectVersions substitutes the pinned-version placeholder tokens in an install
// script with their concrete values. Keeps shell scripts free of hardcoded
// versions so the constants above remain the single source of truth.
func InjectVersions(script string) string {
	return placeholderReplacer.Replace(script)
}

var placeholderReplacer = strings.NewReplacer(
	"__RATHOLE_VERSION__", RatholeVersion,
	"__RATHOLE_REPO__", RatholeRepo,
	"__CADDY_VERSION__", CaddyVersion,
)
