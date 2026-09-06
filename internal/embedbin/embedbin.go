// Package embedbin materializes the external binaries Gopher bundles (Caddy and
// rathole) from the embedded payload onto disk, so the supervisor can exec them
// by path. The edge runs caddy + its own-arch rathole; it also serves rathole
// for every origin arch (RatholeForOrigin).
//
// Extraction is content-gated: a binary is (re)written only when it's missing or
// its recorded stamp differs from the embedded one, so ordinary restarts do no
// disk writes. The payload (payload.go) is embedded from internal/embedbin/bin/,
// populated by scripts/fetch-deps.sh before a release build; a committed
// bin/.gitkeep lets plain `go build`/`go test` work with no binaries present
// (Embedded() then reports false), matching the cmd/server/agents.go convention.
package embedbin

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

// Binary is one bundled executable to materialize on disk.
type Binary struct {
	Name    string // file name under the target dir, e.g. "caddy"
	Version string // human-readable version, e.g. build.CaddyVersion
	Data    []byte // the embedded bytes
}

// stamp is the value recorded beside an extracted binary and compared on the
// next start to decide whether to rewrite it.
//
// It is keyed on a hash of the bytes, not on b.Version alone, because a version
// string and the file it names can diverge: a Caddy rebuilt via xcaddy to add a
// module keeps its upstream version but is a *different binary*. A
// version-keyed stamp would match, skip the rewrite, and leave the old
// module-less binary running while config generation began emitting directives
// it cannot parse — a broken edge on upgrade, for a version bump we never made.
// Hashing the content catches that, and every other way the bytes can change
// (build flags, toolchain) without the version moving.
//
// The version prefix is cosmetic: it keeps the stamp file legible to a human
// debugging an install. Only the whole string is ever compared.
func stamp(b Binary) string {
	return fmt.Sprintf("%s/%x", b.Version, sha256.Sum256(b.Data))
}

// Extract materializes b into dir/<b.Name> with mode 0755, but only when it's
// missing or its recorded stamp differs from b's — so ordinary restarts do no
// disk writes. The write is atomic (temp file + rename): that avoids a
// half-written executable if gopher is killed mid-extract, and lets us replace a
// binary that is *currently running* (rename swaps the path while the running
// process keeps the old inode; an in-place truncating write would fail ETXTBSY).
//
// It returns the destination path and whether it actually (re)wrote the file.
func Extract(dir string, b Binary) (path string, wrote bool, err error) {
	path = filepath.Join(dir, b.Name)
	stampPath := filepath.Join(dir, "."+b.Name+".version")
	want := stamp(b)

	// Already these exact bytes and the binary is present? Nothing to do.
	if cur, readErr := os.ReadFile(stampPath); readErr == nil && string(cur) == want {
		if _, statErr := os.Stat(path); statErr == nil {
			return path, false, nil
		}
	}

	if err = os.MkdirAll(dir, 0o755); err != nil {
		return "", false, fmt.Errorf("embedbin: mkdir %s: %w", dir, err)
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, b.Data, 0o755); err != nil {
		return "", false, fmt.Errorf("embedbin: write %s: %w", tmp, err)
	}
	// WriteFile's mode is masked by umask; force 0755 so it's executable.
	if err = os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return "", false, fmt.Errorf("embedbin: chmod %s: %w", tmp, err)
	}
	if err = os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", false, fmt.Errorf("embedbin: rename %s -> %s: %w", tmp, path, err)
	}
	// Best-effort stamp; if it fails we just re-extract next start (harmless).
	_ = os.WriteFile(stampPath, []byte(want), 0o644)
	return path, true, nil
}

// ExtractAll materializes every binary in bins into dir, stopping at the first
// error.
func ExtractAll(dir string, bins []Binary) error {
	for _, b := range bins {
		if _, _, err := Extract(dir, b); err != nil {
			return err
		}
	}
	return nil
}
