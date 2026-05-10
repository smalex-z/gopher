package service

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/db"
)

// AgentInstaller produces the operator-paste command that installs the
// gopher-agent on an existing (already-bootstrapped) machine. The actual
// install runs as root on the target via the migrate.sh template.
//
// It does not SSH into the machine: the install needs root, the SSH user
// only has narrow NOPASSWD sudo, and there's no software path around that
// without operator interaction. Surfacing the one-liner is the honest UX.
type AgentInstaller struct {
	local *LocalSetupService
}

func NewAgentInstaller(local *LocalSetupService) *AgentInstaller {
	return &AgentInstaller{local: local}
}

// MigrateInstructions is what the dashboard's Install Agent button returns.
// The operator pastes Command on the machine. The agent registers itself once
// the migrate script finishes; HealthService detects it and flips
// Machine.AgentInstalled=true on the next poll.
type MigrateInstructions struct {
	Command     string `json:"command"`
	Instruction string `json:"instruction"`
}

// Install allocates per-machine agent fields if missing, reconciles the
// VPS-side rathole config so the back-channel bind exists, and returns the
// operator-paste command that installs the agent on the target.
//
// Why operator-paste: agent install needs root on the target (creates a
// system user, writes /etc/systemd/system/, drops a sudoers entry, installs
// to /usr/local/bin). Existing machines bootstrapped before the agent
// existed and have only narrow NOPASSWD sudo on a single helper script —
// not enough for any of that. The dashboard cannot fabricate root on the
// target. The operator's first paste creates the privileged actor (the
// agent itself), and from then on every operation is dashboard-driven.
func (i *AgentInstaller) Install(machineID string) (*MigrateInstructions, error) {
	machine, err := db.GetMachine(machineID)
	if err != nil {
		return nil, err
	}

	if err := i.allocateAgentFields(machine); err != nil {
		return nil, err
	}

	settings, err := db.GetSettings()
	if err != nil {
		return nil, fmt.Errorf("settings lookup: %w", err)
	}
	vpsURL, err := buildAgentDownloadBaseURL(settings)
	if err != nil {
		return nil, err
	}

	// Reconcile so the new agent's server.services bind exists by the time
	// the operator pastes the migrate command.
	if err := i.local.ReconcileServerConfig(); err != nil {
		log.Printf("agent install (machine %s): VPS rathole reconcile failed: %v", machine.ID, err)
	}

	// Mint a short-lived migration token. migrate.sh on the target machine
	// will POST it to /api/migrate to retrieve the per-machine secrets it
	// needs (agent token, port, rathole token) — mirrors the bootstrap.sh /
	// /api/bootstrap callback pattern. The token is the only thing that
	// touches shell history.
	const migrationTokenTTL = 1 * time.Hour
	token := shortToken()
	if err := db.CreateMigrationToken(token, machine.ID, migrationTokenTTL); err != nil {
		return nil, fmt.Errorf("create migration token: %w", err)
	}

	cmd := fmt.Sprintf("curl -fsSL %s/static/migrate.sh | sudo bash -s -- %s", vpsURL, token)
	return &MigrateInstructions{
		Command: cmd,
		Instruction: "Run this on the machine (one-time per machine; requires sudo). " +
			"The agent registers itself once installed — the dashboard badge flips " +
			"green on the next health check (≤60s).",
	}, nil
}

// allocateAgentFields generates per-machine agent secrets and ports if the
// machine record is missing them. Pre-agent-era machines have these fields
// at their zero value; new bootstraps populate them at registration time.
func (i *AgentInstaller) allocateAgentFields(machine *db.Machine) error {
	if machine.TunnelPort == 0 {
		return fmt.Errorf("machine has no tunnel port; bootstrap may be incomplete")
	}
	dirty := false
	if machine.AgentLocalPort == 0 {
		machine.AgentLocalPort = agentLocalPortDefault
		dirty = true
	}
	if machine.AgentRemotePort == 0 {
		port, err := db.NextRatholePort()
		if err != nil {
			return fmt.Errorf("allocate agent remote port: %w", err)
		}
		machine.AgentRemotePort = port
		dirty = true
	}
	if machine.AgentToken == "" {
		machine.AgentToken = shortToken()
		dirty = true
	}
	if machine.AgentRatholeToken == "" {
		machine.AgentRatholeToken = shortToken()
		dirty = true
	}
	if dirty {
		if err := db.UpdateMachine(machine); err != nil {
			return fmt.Errorf("persist agent field allocation: %w", err)
		}
	}
	return nil
}

// buildAgentDownloadBaseURL returns the URL prefix the client machine should
// curl to fetch the agent binary. Three cases:
//
//   - settings.ServerHost includes a scheme (http:// or https://) — treat as
//     a full override and use as-is. Power-users overriding for split DNS,
//     internal load balancers, etc.
//   - LocalSetupDone (Caddy is the entry point) — the dashboard lives at
//     router.<domain>; that's the only hostname Caddy has a TLS site for.
//   - Pre-Caddy bootstrap — hit the dashboard directly on its non-TLS port.
func buildAgentDownloadBaseURL(settings *db.AppSettings) (string, error) {
	if settings == nil {
		return "", fmt.Errorf("nil settings")
	}
	if settings.ServerHost != "" {
		if strings.HasPrefix(settings.ServerHost, "http://") || strings.HasPrefix(settings.ServerHost, "https://") {
			return strings.TrimRight(settings.ServerHost, "/"), nil
		}
	}
	host := settings.ServerHost
	if host == "" {
		host = settings.Domain
	}
	if host == "" {
		return "", fmt.Errorf("no VPS host configured; set domain or server_host first")
	}
	if settings.LocalSetupDone {
		// Caddy fronts the dashboard at router.<domain>. ServerHost-as-host
		// is honoured if explicitly set (operator may already have given us
		// the dashboard hostname directly).
		if settings.ServerHost == "" {
			host = "router." + host
		}
		return "https://" + host, nil
	}
	// Pre-Caddy / skipCaddy install path: the URL has to land on the
	// dashboard's HTTP listener directly. main.go binds the listener to
	// 127.0.0.1 whenever BindIP is set (treating BindIP as "the dashboard
	// is private, Caddy will reverse-proxy to it"), so a pre-Caddy fetch
	// to http://<bindIP>:<port> would 502. Refuse with a clear error
	// instead of returning a URL that silently fails on the client.
	if settings.BindIP != "" && !settings.LocalSetupDone {
		return "", fmt.Errorf("agent install over plain HTTP isn't supported when bind_ip is set and Caddy isn't running yet — finish the local install or expose the dashboard on a public address first")
	}
	return fmt.Sprintf("http://%s:%d", host, dashboardPort), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
