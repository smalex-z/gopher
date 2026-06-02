package service

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// Regression coverage for the noise-migration bug that broke proxmox-base
// for ~24h in production. detectCustomRatholeServices walks the BEGIN/END
// CUSTOM CONFIGURATION block and pulls out [server.services.X] names that
// will silently stop working after the migration flips the server to noise.
// We had no test for it because the migration assumed every service lived
// in the DB; the production incident proved that wrong.

func writeTempToml(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "server.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestDetectCustomRatholeServices_FindsOneService(t *testing.T) {
	body := `[server]
bind_addr = "0.0.0.0:2333"

# gopher-tunnel-start: abc
[server.services.tunnel-abc]
token = "x"
bind_addr = "0.0.0.0:1024"
# gopher-tunnel-end: abc

# ===== BEGIN CUSTOM CONFIGURATION =====
[server.services.proxmox-base]
token = "secret"
bind_addr = "0.0.0.0:5400"
# ===== END CUSTOM CONFIGURATION =====
`
	got := detectCustomRatholeServices(writeTempToml(t, body))
	want := []string{"proxmox-base"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDetectCustomRatholeServices_FindsMultipleServices(t *testing.T) {
	body := `[server]
bind_addr = "0.0.0.0:2333"

# ===== BEGIN CUSTOM CONFIGURATION =====
[server.services.proxmox-base]
token = "a"
bind_addr = "0.0.0.0:5400"

[server.services.media_server]
token = "b"
bind_addr = "0.0.0.0:5401"

[server.services.foo-bar-123]
token = "c"
bind_addr = "0.0.0.0:5402"
# ===== END CUSTOM CONFIGURATION =====
`
	got := detectCustomRatholeServices(writeTempToml(t, body))
	sort.Strings(got)
	want := []string{"foo-bar-123", "media_server", "proxmox-base"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDetectCustomRatholeServices_IgnoresManagedSectionsAboveMarker(t *testing.T) {
	// Critical: any [server.services.*] above the BEGIN marker is Gopher-
	// managed and must NOT be flagged as custom. Otherwise the migration
	// would surface a warning for every tunnel the operator added through
	// the dashboard — false-positive noise that buries the real signal.
	body := `[server]
bind_addr = "0.0.0.0:2333"

[server.services.tunnel-managed-1]
token = "m1"
bind_addr = "0.0.0.0:1024"

[server.services.machine-managed-2-ssh]
token = "m2"
bind_addr = "0.0.0.0:1025"

# ===== BEGIN CUSTOM CONFIGURATION =====
[server.services.user-added]
token = "u"
bind_addr = "0.0.0.0:5400"
# ===== END CUSTOM CONFIGURATION =====
`
	got := detectCustomRatholeServices(writeTempToml(t, body))
	want := []string{"user-added"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDetectCustomRatholeServices_EmptyCustomBlockReturnsEmpty(t *testing.T) {
	body := `[server]
bind_addr = "0.0.0.0:2333"

[server.services.tunnel-managed]
token = "m"
bind_addr = "0.0.0.0:1024"

# ===== BEGIN CUSTOM CONFIGURATION =====
# ===== END CUSTOM CONFIGURATION =====
`
	got := detectCustomRatholeServices(writeTempToml(t, body))
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestDetectCustomRatholeServices_NoBeginMarkerReturnsEmpty(t *testing.T) {
	// Fresh-install case: server.toml may not have the markers yet (the
	// reconcile adds them). Don't error, return empty.
	body := `[server]
bind_addr = "0.0.0.0:2333"
`
	got := detectCustomRatholeServices(writeTempToml(t, body))
	if len(got) != 0 {
		t.Errorf("expected empty slice for marker-less config, got %v", got)
	}
}

func TestDetectCustomRatholeServices_MissingFileReturnsEmpty(t *testing.T) {
	got := detectCustomRatholeServices("/no/such/file/exists")
	if len(got) != 0 {
		t.Errorf("expected empty slice for missing file, got %v", got)
	}
}

func TestDetectCustomRatholeServices_DedupesRepeatedNames(t *testing.T) {
	// Pathological hand-edited config — same service declared twice in the
	// custom block. The detector should de-dup so the dashboard banner says
	// "1 service" not "2 services".
	body := `[server]

# ===== BEGIN CUSTOM CONFIGURATION =====
[server.services.proxmox-base]
token = "a"
bind_addr = "0.0.0.0:5400"

[server.services.proxmox-base]
token = "b"
bind_addr = "0.0.0.0:5401"
# ===== END CUSTOM CONFIGURATION =====
`
	got := detectCustomRatholeServices(writeTempToml(t, body))
	want := []string{"proxmox-base"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
