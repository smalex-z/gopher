package embedbin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtract_WritesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	p, wrote, err := Extract(dir, Binary{Name: "tool", Version: "1", Data: []byte("AAAA")})
	if err != nil || !wrote {
		t.Fatalf("Extract: wrote=%v err=%v, want wrote=true nil", wrote, err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "AAAA" {
		t.Fatalf("content = %q, want AAAA", got)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755 (executable)", fi.Mode().Perm())
	}
}

func TestExtract_SkipsWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	b := Binary{Name: "tool", Version: "1", Data: []byte("AAAA")}
	if _, _, err := Extract(dir, b); err != nil {
		t.Fatal(err)
	}
	// Tamper with the on-disk copy; a skipped extract must leave it untouched.
	p := filepath.Join(dir, "tool")
	if err := os.WriteFile(p, []byte("TAMPERED"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, wrote, err := Extract(dir, b) // same version + same bytes -> skip
	if err != nil || wrote {
		t.Fatalf("Extract: wrote=%v err=%v, want wrote=false nil", wrote, err)
	}
	if got, _ := os.ReadFile(p); string(got) != "TAMPERED" {
		t.Fatalf("skipped extract rewrote the file: %q", got)
	}
}

// The xcaddy case: adding a Caddy module rebuilds the binary without moving the
// upstream version, so the bytes change while Version stays put. A
// version-keyed stamp would skip the rewrite and leave the old module-less
// binary in place while config generation started emitting directives it can't
// parse. The stamp must follow the content.
func TestExtract_RewritesWhenContentChangesButVersionDoesNot(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := Extract(dir, Binary{Name: "tool", Version: "2.10.0", Data: []byte("CADDY")}); err != nil {
		t.Fatal(err)
	}
	p, wrote, err := Extract(dir, Binary{Name: "tool", Version: "2.10.0", Data: []byte("CADDY+CACHE")})
	if err != nil || !wrote {
		t.Fatalf("Extract: wrote=%v err=%v, want wrote=true (content changed at same version)", wrote, err)
	}
	if got, _ := os.ReadFile(p); string(got) != "CADDY+CACHE" {
		t.Fatalf("content = %q, want CADDY+CACHE", got)
	}
}

// Installs predating the content-hash stamp hold a bare version string. The
// first start after upgrading must not trust it — it says nothing about the
// bytes on disk — so it re-extracts once and restamps.
func TestExtract_RewritesOnLegacyVersionOnlyStamp(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tool"), []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	stampPath := filepath.Join(dir, ".tool.version")
	if err := os.WriteFile(stampPath, []byte("2.10.0"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := Binary{Name: "tool", Version: "2.10.0", Data: []byte("NEW")}
	p, wrote, err := Extract(dir, b)
	if err != nil || !wrote {
		t.Fatalf("Extract: wrote=%v err=%v, want wrote=true (legacy stamp must not match)", wrote, err)
	}
	if got, _ := os.ReadFile(p); string(got) != "NEW" {
		t.Fatalf("content = %q, want NEW", got)
	}
	// And it must settle: the next start with the same binary skips.
	if _, wrote, err = Extract(dir, b); err != nil || wrote {
		t.Fatalf("second Extract: wrote=%v err=%v, want wrote=false (stamp now current)", wrote, err)
	}
}

func TestExtract_RewritesOnVersionChange(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := Extract(dir, Binary{Name: "tool", Version: "1", Data: []byte("OLD")}); err != nil {
		t.Fatal(err)
	}
	p, wrote, err := Extract(dir, Binary{Name: "tool", Version: "2", Data: []byte("NEW")})
	if err != nil || !wrote {
		t.Fatalf("Extract: wrote=%v err=%v, want wrote=true (version changed)", wrote, err)
	}
	if got, _ := os.ReadFile(p); string(got) != "NEW" {
		t.Fatalf("content = %q, want NEW", got)
	}
}

func TestExtract_RewritesWhenBinaryDeletedButStampKept(t *testing.T) {
	dir := t.TempDir()
	b := Binary{Name: "tool", Version: "1", Data: []byte("AAAA")}
	if _, _, err := Extract(dir, b); err != nil {
		t.Fatal(err)
	}
	// Stamp says "1" but the binary is gone — must re-extract, not trust the stamp.
	if err := os.Remove(filepath.Join(dir, "tool")); err != nil {
		t.Fatal(err)
	}
	_, wrote, err := Extract(dir, b)
	if err != nil || !wrote {
		t.Fatalf("Extract: wrote=%v err=%v, want wrote=true (binary was missing)", wrote, err)
	}
}

func TestExtract_NoTempFileLeftBehind(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := Extract(dir, Binary{Name: "tool", Version: "1", Data: []byte("AAAA")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tool.tmp")); !os.IsNotExist(err) {
		t.Fatal("tool.tmp left behind after extract")
	}
}
