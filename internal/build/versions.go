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

	// Artifact pins: sha256 of the exact release archives fetch-deps.sh
	// downloads and embeds. Version pinning alone freezes the URL, not the
	// content — without these, whoever controls (or compromises) the upstream
	// release assets controls what gets embedded into every gopher release and
	// runs supervised on every edge. fetch-deps.sh refuses to stage an archive
	// whose hash doesn't match. Bumping CaddyVersion/RatholeVersion therefore
	// deliberately includes updating these hashes (compute with sha256sum on
	// the freshly downloaded archives, cross-checking upstream's own published
	// checksums where they exist — caddy ships a *_checksums.txt per release).
	CaddySHA256AMD64 = "5c218bc34c9197369263da7e9317a83acdbd80ef45d94dca5eff76e727c67cdd" // caddy_2.10.2_linux_amd64.tar.gz
	CaddySHA256ARM64 = "501e955fa634c5aab63247458c3ac655cfdd6cbf1e0436528f41248451c190ac" // caddy_2.10.2_linux_arm64.tar.gz

	RatholeSHA256X8664   = "3e7d0d0f365120cd3cd351d147d1a12ee960c8068b464d4dd533a3821873b80e" // rathole-x86_64-unknown-linux-gnu.zip
	RatholeSHA256AARCH64 = "fa4a6fc63d86f8f1faa7c103a845e4715ce79a048455c0eec897b27237576564" // rathole-aarch64-unknown-linux-musl.zip
	RatholeSHA256ARMV7   = "e8662d80d2cc9acc5f8f4d8a1c1a5ff7717b2fa71919a405d0eed8b64c8c1d88" // rathole-armv7-unknown-linux-musleabihf.zip

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
	AgentVersion = "0.2.10"
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
