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

	toml, machine, err := (&BootstrapService{}).RecoverClientConfig("agent-bearer-tok", "router.uclaacm.com:443")
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
	if _, _, err := (&BootstrapService{}).RecoverClientConfig("wrong-token", "router.uclaacm.com"); err != ErrUnknownAgentToken {
		t.Fatalf("err = %v, want ErrUnknownAgentToken", err)
	}
}
