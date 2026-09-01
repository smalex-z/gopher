package service

import (
	"strings"
	"testing"
	"time"

	"github.com/smalex-z/gopher/internal/db"
)

func seedAgentMachine(t *testing.T) *db.Machine {
	t.Helper()
	m := &db.Machine{
		ID:                "btb1",
		Name:              "btb",
		Username:          "ubuntu",
		TunnelPort:        1036,
		RatholeSSHToken:   "ssh-tok",
		AgentToken:        "agent-bearer-tok",
		AgentRatholeToken: "agent-rathole-tok",
		AgentLocalPort:    4322,
		AgentRemotePort:   41036,
		Status:            "offline",
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}
	if err := db.CreateMachine(m); err != nil {
		t.Fatalf("seed machine: %v", err)
	}
	return m
}

// remote_addr must be the host the agent reached us on (its GOPHER_EDGE_URL,
// router.<domain> in a standard install) — NOT settings.Domain. The apex often
// points somewhere else entirely (e.g. an org's main website on different
// hosting), and a recovered config aimed there can never reconnect the tunnel.
func TestRecoverClientConfig_RemoteAddrComesFromRequestHost(t *testing.T) {
	initTestDB(t)
	if err := db.MutateSettings(func(s *db.AppSettings) error {
		s.Domain = "uclaacm.com" // apex ≠ VPS; must not leak into remote_addr
		return nil
	}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	seedAgentMachine(t)

	toml, machine, err := (&BootstrapService{}).RecoverClientConfig("agent-bearer-tok", "router.uclaacm.com:443", "")
	if err != nil {
		t.Fatalf("RecoverClientConfig: %v", err)
	}
	if machine.Name != "btb" {
		t.Errorf("machine = %q, want btb", machine.Name)
	}
	if !strings.Contains(toml, `remote_addr = "router.uclaacm.com:2333"`) {
		t.Errorf("remote_addr must use the request host (port stripped), got:\n%s", toml)
	}
	if strings.Contains(toml, `remote_addr = "uclaacm.com:2333"`) {
		t.Errorf("remote_addr must never fall back to the apex domain, got:\n%s", toml)
	}
	if !strings.Contains(toml, "machine-btb1-agent") || !strings.Contains(toml, "machine-btb1-ssh") {
		t.Errorf("recovered config missing managed sections:\n%s", toml)
	}
}

func TestRecoverClientConfig_UnknownTokenRejected(t *testing.T) {
	initTestDB(t)
	seedAgentMachine(t)
	if _, _, err := (&BootstrapService{}).RecoverClientConfig("wrong-token", "router.uclaacm.com", ""); err != ErrUnknownAgentToken {
		t.Fatalf("err = %v, want ErrUnknownAgentToken", err)
	}
}

// The submitted config is by definition suspect (the agent only dials home
// when rathole keeps failing with it), so everything managed — the [client]
// block included — is rebuilt from the DB; a corrupted remote_addr must not
// survive. Only the operator's custom sections carry over: they exist
// nowhere but that file.
func TestRecoverClientConfig_RebuildsManagedKeepsCustom(t *testing.T) {
	initTestDB(t)
	seedAgentMachine(t)

	const suspect = `[client]
remote_addr = "corrupted-by-hand.example:9999"

[client.transport]
type = "noise"

[client.transport.noise]
remote_public_key = "stale-key"

# gopher-machine-start: btb1
[client.services.machine-btb1-ssh]
type = "tcp"
token = "stale-ssh-tok"
local_addr = "0.0.0.0:22"
# gopher-machine-end: btb1

[client.services.my-custom-thing]
type = "tcp"
token = "operator-secret"
local_addr = "localhost:9000"
`
	toml, _, err := (&BootstrapService{}).RecoverClientConfig("agent-bearer-tok", "router.uclaacm.com", suspect)
	if err != nil {
		t.Fatalf("RecoverClientConfig: %v", err)
	}
	if !strings.Contains(toml, `remote_addr = "router.uclaacm.com:2333"`) {
		t.Errorf("corrupted remote_addr must be rebuilt from the request host:\n%s", toml)
	}
	if strings.Contains(toml, "corrupted-by-hand") || strings.Contains(toml, "stale-ssh-tok") || strings.Contains(toml, "stale-key") {
		t.Errorf("suspect managed content must not survive the rebuild:\n%s", toml)
	}
	if !strings.Contains(toml, `token = "ssh-tok"`) {
		t.Errorf("SSH token must come from the DB:\n%s", toml)
	}
	if !strings.Contains(toml, "[client.services.my-custom-thing]") || !strings.Contains(toml, `token = "operator-secret"`) {
		t.Errorf("operator custom section must be carried over:\n%s", toml)
	}
}

// Field regression (2026-09-01): a pure-garbage submitted config has no
// section structure, so the old strip-based salvage kept the garbage as
// "custom content" and appended it to the rebuilt config — re-poisoning it
// and costing an extra refetch cycle (it only converged because a strip-order
// accident ate the debris on the second pass). Debris must never survive;
// real custom sections must.
func TestRecoverClientConfig_DiscardsDebrisKeepsCustomSections(t *testing.T) {
	initTestDB(t)
	seedAgentMachine(t)
	svc := &BootstrapService{}

	// Pure garbage in → clean config out.
	toml, _, err := svc.RecoverClientConfig("agent-bearer-tok", "router.uclaacm.com", "this is [not toml\n")
	if err != nil {
		t.Fatalf("RecoverClientConfig(garbage): %v", err)
	}
	if strings.Contains(toml, "not toml") {
		t.Errorf("corruption debris must not survive the rebuild:\n%s", toml)
	}

	// Garbage AND a real custom section → keep the section, drop the debris.
	mixed := "half a line of junk\n[client.services.my-custom]\ntype = \"tcp\"\ntoken = \"opsecret\"\nlocal_addr = \"localhost:9000\"\n"
	toml, _, err = svc.RecoverClientConfig("agent-bearer-tok", "router.uclaacm.com", mixed)
	if err != nil {
		t.Fatalf("RecoverClientConfig(mixed): %v", err)
	}
	if strings.Contains(toml, "junk") {
		t.Errorf("debris outside custom sections must be dropped:\n%s", toml)
	}
	if !strings.Contains(toml, "[client.services.my-custom]") || !strings.Contains(toml, `token = "opsecret"`) {
		t.Errorf("custom section must survive alongside debris removal:\n%s", toml)
	}
}
