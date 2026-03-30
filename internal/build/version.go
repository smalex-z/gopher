package build

// Version is set at build time via:
//
//	-ldflags "-X github.com/smalex-z/gopher/internal/build.Version=vX.Y.Z"
//
// Defaults to "dev" for local/unversioned builds.
var Version = "dev"
