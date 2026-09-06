package build

import "runtime"

// RatholeTarget is one origin architecture the edge bundles rathole for and
// serves to bootstrapping origins — replacing the per-origin download from
// GitHub. ReleaseTag is the arch suffix in rathole's release asset name
// (rathole-<ReleaseTag>.zip); Uname is the `uname -m` value an origin reports,
// used to hand back the matching binary at bootstrap time.
type RatholeTarget struct {
	Name       string // short id used in the embedded asset filename
	ReleaseTag string // rathole GitHub release asset arch tag
	Uname      string // origin's `uname -m`
}

// RatholeTargets is the full set of origin architectures the edge embeds
// rathole for. The edge serves the matching binary to each origin and runs the
// entry matching its own arch as rathole-server. Every gopher build embeds ALL
// of these (the edge is a distribution hub for origins), unlike Caddy which is
// embedded only for the edge's own arch.
//
// armv7 is included so 32-bit ARM origins (Raspberry Pi 2/older, some routers)
// keep working. All Linux arches are fully managed: the gopher-agent is built
// for armv7 too (GOARM=7, hard-float — matches rathole's musleabihf ABI), so
// armv7 origins get the agent like any other Linux box rather than being a
// tunnel-only second class. (macOS/Windows are the tunnel-only tier, by OS, not
// by CPU arch.) Mirrors the arch mapping in templates/bootstrap.sh.
var RatholeTargets = []RatholeTarget{
	{Name: "x86_64", ReleaseTag: "x86_64-unknown-linux-gnu", Uname: "x86_64"},
	{Name: "aarch64", ReleaseTag: "aarch64-unknown-linux-musl", Uname: "aarch64"},
	{Name: "armv7", ReleaseTag: "armv7-unknown-linux-musleabihf", Uname: "armv7l"},
}

// EdgeRatholeTarget returns the target matching the edge's own architecture —
// the binary used to run rathole-server locally. Edges are amd64 or arm64.
func EdgeRatholeTarget() RatholeTarget {
	if runtime.GOARCH == "arm64" {
		return RatholeTargets[1] // aarch64
	}
	return RatholeTargets[0] // amd64 -> x86_64
}

// RatholeTargetForUname maps an origin's `uname -m` to its embed target.
// ok is false for architectures the edge does not bundle rathole for.
func RatholeTargetForUname(uname string) (RatholeTarget, bool) {
	for _, t := range RatholeTargets {
		if t.Uname == uname {
			return t, true
		}
	}
	return RatholeTarget{}, false
}
