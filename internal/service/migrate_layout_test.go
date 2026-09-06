package service

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smalex-z/gopher/internal/paths"
)

// redirectEdgePaths points every path MigrateEdgeLayout touches into a temp
// dir and restores the originals afterwards. paths' values are vars for
// exactly this purpose.
func redirectEdgePaths(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()
	saved := []struct {
		p   *string
		sub string
	}{
		{&paths.StateDir, "var-lib-gopher"},
		{&paths.ConfigDir, "etc-gopher"},
		{&paths.CaddyfilePath, "etc-gopher/caddy/Caddyfile"},
		{&paths.CaddyConfDir, "etc-gopher/caddy/conf.d"},
		{&paths.RatholeConfig, "etc-gopher/rathole/server.toml"},
		{&paths.CaddyData, "var-lib-gopher/caddy"},
		{&paths.LegacyRatholeConfig, "etc-rathole/server.toml"},
		{&paths.LegacyCaddyfile, "etc-caddy/Caddyfile"},
		{&paths.LegacyCaddyConfDir, "etc-caddy/conf.d"},
		{&paths.LegacyCaddyData, "var-lib-caddy/data"},
	}
	orig := make([]string, len(saved))
	for i, s := range saved {
		orig[i] = *s.p
		*s.p = filepath.Join(root, s.sub)
	}
	if err := os.MkdirAll(filepath.Join(root, "var-lib-gopher"), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	t.Cleanup(func() {
		for i, s := range saved {
			*s.p = orig[i]
		}
	})
	return root
}

// interceptSudoCmds swaps runLocalCmd for a stub that records every argv,
// really executes the file operations (cp/rm/sh -c, sans sudo) so the
// migration's copy logic is exercised for real, and no-ops the system
// commands — a test must NEVER systemctl-mask caddy or pkill rathole on the
// machine running it.
func interceptSudoCmds(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	orig := runLocalCmd
	runLocalCmd = func(w io.Writer, name string, args ...string) error {
		argv := append([]string{name}, args...)
		calls = append(calls, argv)
		// Strip the sudo -n prefix; tempdir paths need no elevation.
		for len(argv) > 0 && (argv[0] == "sudo" || argv[0] == "-n") {
			argv = argv[1:]
		}
		if len(argv) == 0 {
			return nil
		}
		switch argv[0] {
		case "cp", "rm", "sh":
			cmd := exec.Command(argv[0], argv[1:]...) // #nosec G204 — test stub, tempdir args
			cmd.Stdout, cmd.Stderr = w, w
			return cmd.Run()
		default: // systemctl, pkill, chown, daemon-reload… — never for real
			return nil
		}
	}
	t.Cleanup(func() { runLocalCmd = orig })
	return &calls
}

func sawCommand(calls [][]string, want ...string) bool {
	for _, argv := range calls {
		if strings.Contains(strings.Join(argv, " "), strings.Join(want, " ")) {
			return true
		}
	}
	return false
}

// Fresh install: no legacy files → no-op, marker written, second run no-op.
func TestMigrateEdgeLayout_FreshInstallNoop(t *testing.T) {
	initTestDB(t)
	redirectEdgePaths(t)
	calls := interceptSudoCmds(t)

	var out bytes.Buffer
	migrated, err := MigrateEdgeLayout(&out)
	if err != nil {
		t.Fatalf("MigrateEdgeLayout: %v", err)
	}
	if migrated {
		t.Error("fresh install must not report migrated")
	}
	if sawCommand(*calls, "systemctl") {
		t.Error("fresh install must not touch systemd units")
	}
	marker := paths.StateDir + "/.edge-migrated"
	if b, err := os.ReadFile(marker); err != nil || string(b) != "fresh\n" {
		t.Errorf("marker = %q, %v; want \"fresh\" written", b, err)
	}

	if migrated, err = MigrateEdgeLayout(&out); err != nil || migrated {
		t.Errorf("second run = (%v, %v), want no-op", migrated, err)
	}
}

// Legacy layout present → configs and certs land in the new trees with
// content preserved, legacy trees stay for rollback, marker set, idempotent.
func TestMigrateEdgeLayout_LegacyMigratesAndPreserves(t *testing.T) {
	initTestDB(t)
	redirectEdgePaths(t)
	calls := interceptSudoCmds(t)

	writeFileMkdir := func(p, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFileMkdir(paths.LegacyRatholeConfig, "[server]\n# ===== BEGIN CUSTOM CONFIGURATION =====\n[server.services.user-own]\n# ===== END CUSTOM CONFIGURATION =====\n")
	writeFileMkdir(paths.LegacyCaddyfile, "import /etc/caddy/conf.d/*.caddy\n")
	writeFileMkdir(paths.LegacyCaddyConfDir+"/gopher-t1.caddy", "t1.example.com {\n}\n")
	writeFileMkdir(paths.LegacyCaddyData+"/certificates/wild.crt", "CERTBYTES")

	var out bytes.Buffer
	migrated, err := MigrateEdgeLayout(&out)
	if err != nil {
		t.Fatalf("MigrateEdgeLayout: %v\noutput:\n%s", err, out.String())
	}
	if !migrated {
		t.Fatal("legacy layout should report migrated=true")
	}

	if b, err := os.ReadFile(paths.RatholeConfig); err != nil || !strings.Contains(string(b), "user-own") {
		t.Errorf("server.toml not copied with custom block: %q, %v", b, err)
	}
	if b, err := os.ReadFile(paths.CaddyData + "/certificates/wild.crt"); err != nil || string(b) != "CERTBYTES" {
		t.Errorf("caddy cert not preserved: %q, %v", b, err)
	}
	if _, err := os.Stat(paths.CaddyConfDir + "/gopher-t1.caddy"); err != nil {
		t.Errorf("conf.d block not copied: %v", err)
	}
	if b, err := os.ReadFile(paths.CaddyfilePath); err != nil || !strings.Contains(string(b), "conf.d") {
		t.Errorf("rebuilt Caddyfile missing conf.d import: %q, %v", b, err)
	}
	// COPY not move — the legacy trees are the rollback path.
	for _, p := range []string{paths.LegacyRatholeConfig, paths.LegacyCaddyfile, paths.LegacyCaddyData + "/certificates/wild.crt"} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("legacy file %s must survive migration: %v", p, err)
		}
	}
	// The legacy services must be neutralized (recorded, not executed here).
	for _, unit := range []string{"rathole-server", "caddy"} {
		if !sawCommand(*calls, "systemctl", "disable", "--now", unit) {
			t.Errorf("expected disable --now %s", unit)
		}
	}
	if !sawCommand(*calls, "pkill") {
		t.Error("expected orphaned-rathole pkill step")
	}

	if migrated, err = MigrateEdgeLayout(&out); err != nil || migrated {
		t.Errorf("re-run = (%v, %v), want idempotent no-op", migrated, err)
	}
}
