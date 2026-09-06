package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitReferences(t *testing.T) {
	dir := t.TempDir()
	unit := filepath.Join(dir, "rathole-client.service")
	if err := os.WriteFile(unit, []byte("[Service]\nExecStart=/usr/local/bin/rathole /etc/rathole/client.toml\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !unitReferences(unit, "/etc/rathole/client.toml") {
		t.Fatal("expected legacy path to be detected in unit")
	}
	if unitReferences(unit, "/etc/gopher/rathole/client.toml") {
		t.Fatal("did not expect the new path to be present yet")
	}
	if unitReferences(filepath.Join(dir, "nope.service"), "/anything") {
		t.Fatal("missing unit file must report no reference, not error")
	}
}

func TestDuplicateTomlTable(t *testing.T) {
	// The exact shape that took justin-mc offline: the same agent service table
	// declared twice (each in its own marker block).
	dup := `[client]
remote_addr = "edge:2333"

# gopher-machine-agent-start: f3e1a60a7a333072
[client.services.machine-f3e1a60a7a333072-agent]
type = "tcp"
token = "a"
local_addr = "127.0.0.1:1028"
# gopher-machine-agent-end: f3e1a60a7a333072

# gopher-machine-agent-start: f3e1a60a7a333072
[client.services.machine-f3e1a60a7a333072-agent]
type = "tcp"
token = "a"
local_addr = "127.0.0.1:1028"
# gopher-machine-agent-end: f3e1a60a7a333072
`
	name, ok := duplicateTomlTable(dup)
	if !ok || name != "client.services.machine-f3e1a60a7a333072-agent" {
		t.Fatalf("expected duplicate agent table detected, got name=%q ok=%v", name, ok)
	}

	clean := `[client]
remote_addr = "edge:2333"

[client.transport]
type = "noise"

[client.services.machine-x-agent]
type = "tcp"

[client.services.tunnel-y]
type = "tcp"
`
	if name, ok := duplicateTomlTable(clean); ok {
		t.Fatalf("clean config flagged as duplicate: %q", name)
	}

	// `[[...]]` arrays of tables may legally repeat; commented headers ignored.
	arrays := "[[svc]]\nx=1\n[[svc]]\ny=2\n# [client.services.z]\n[client.services.z]\n"
	if _, ok := duplicateTomlTable(arrays); ok {
		t.Fatal("array-of-tables / commented header must not count as a duplicate")
	}
}

func TestDedupeTomlTables(t *testing.T) {
	// Two marker-wrapped agent blocks for the same machine — the justin-mc case.
	in := `[client]
remote_addr = "edge:2333"

# gopher-machine-agent-start: f3e1a60a7a333072
[client.services.machine-f3e1a60a7a333072-agent]
type = "tcp"
token = "keep"
local_addr = "127.0.0.1:1028"
# gopher-machine-agent-end: f3e1a60a7a333072

# gopher-machine-agent-start: f3e1a60a7a333072
[client.services.machine-f3e1a60a7a333072-agent]
type = "tcp"
token = "drop"
local_addr = "127.0.0.1:1028"
# gopher-machine-agent-end: f3e1a60a7a333072
`
	out := dedupeTomlTables(in)
	if _, dup := duplicateTomlTable(out); dup {
		t.Fatalf("dedup left a duplicate:\n%s", out)
	}
	if strings.Count(out, "[client.services.machine-f3e1a60a7a333072-agent]") != 1 {
		t.Fatalf("expected exactly one agent table after dedup:\n%s", out)
	}
	// Keep-first: the surviving block is the one with token "keep".
	if !strings.Contains(out, `token = "keep"`) || strings.Contains(out, `token = "drop"`) {
		t.Fatalf("dedup should keep the first occurrence, got:\n%s", out)
	}
	// The unrelated [client] table and remote_addr must survive untouched.
	if !strings.Contains(out, `remote_addr = "edge:2333"`) {
		t.Fatalf("dedup dropped unrelated config:\n%s", out)
	}
}

func TestDedupeTomlTables_CleanIsUnchanged(t *testing.T) {
	in := `[client]
remote_addr = "edge:2333"

[client.services.tunnel-a]
type = "tcp"

[client.services.machine-x-agent]
type = "tcp"
`
	if out := dedupeTomlTables(in); out != in {
		t.Fatalf("clean config was modified:\n--- in ---\n%s\n--- out ---\n%s", in, out)
	}
}

func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "present")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !fileExists(f) {
		t.Fatal("expected fileExists to report an existing file")
	}
	if fileExists(filepath.Join(dir, "absent")) {
		t.Fatal("expected fileExists to report a missing file as false")
	}
}
