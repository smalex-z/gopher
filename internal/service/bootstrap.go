package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"time"

	"github.com/smalex-z/gopher/internal/config"
	"github.com/smalex-z/gopher/internal/db"
	sshpkg "github.com/smalex-z/gopher/internal/ssh"
)

type BootstrapService struct {
	local *LocalSetupService
}

func NewBootstrapService(local *LocalSetupService) *BootstrapService {
	return &BootstrapService{local: local}
}

// GenerateToken creates a one-time bootstrap token valid for 1 hour.
// tunnelPort optionally pre-assigns the SSH tunnel port (0 = auto-allocate).
// sshKeyID optionally pins the SSH key to install on the machine (empty = use default).
func (s *BootstrapService) GenerateToken(tunnelPort int, sshKeyID string, publicSSH bool) (*db.BootstrapToken, error) {
	bt := &db.BootstrapToken{
		ID:         shortToken(),
		Token:      shortToken(),
		ExpiresAt:  time.Now().Add(time.Hour),
		CreatedAt:  time.Now(),
		TunnelPort: tunnelPort,
		SSHKeyID:   sshKeyID,
		PublicSSH:  publicSSH,
	}
	if err := db.CreateBootstrapToken(bt); err != nil {
		return nil, err
	}
	return bt, nil
}

type BootstrapRequest struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

type BootstrapResponse struct {
	TunnelPort    int    `json:"tunnel_port"`
	RatholeToken  string `json:"rathole_token"`
	VPSPublicKey  string `json:"vps_ssh_public_key"`
	RatholeConfig string `json:"rathole_client_config"`
	VPSHost       string `json:"vps_host"`
	// gopher-agent install hints (the bootstrap script reads these to set up
	// the agent alongside rathole-client). All-or-nothing: if any are zero/empty
	// the script skips the agent install step.
	AgentToken      string `json:"agent_token,omitempty"`
	AgentLocalPort  int    `json:"agent_local_port,omitempty"`
	AgentRemotePort int    `json:"agent_remote_port,omitempty"`
}

// agentLocalPortDefault is the port the agent binds on each client. Fixed for
// simplicity — clients only ever run one agent.
const agentLocalPortDefault = 4322

// Register validates token, provisions a machine, adds the SSH back-tunnel
// to /etc/rathole/server.toml, and returns the rathole client config.
func (s *BootstrapService) Register(req BootstrapRequest, serverHost string) (*BootstrapResponse, error) {
	bt, err := db.GetBootstrapToken(req.Token)
	if err != nil || bt.UsedAt != nil || time.Now().After(bt.ExpiresAt) {
		return nil, fmt.Errorf("invalid or expired token")
	}

	// Retrieve the SSH key to use: token-pinned key → default → auto-generate.
	var sshKey *db.SSHKey
	if bt.SSHKeyID != "" {
		sshKey, err = db.GetSSHKey(bt.SSHKeyID)
		if err != nil {
			return nil, fmt.Errorf("specified SSH key not found: %w", err)
		}
	} else {
		sshKey, err = db.GetDefaultSSHKey()
	}
	if err != nil {
		// No key yet (user skipped setup step 3) — auto-generate one.
		privKey, pubKey, kerr := sshpkg.GenerateRSAKeypair()
		if kerr != nil {
			return nil, fmt.Errorf("failed to generate SSH keypair: %w", kerr)
		}
		sshKey = &db.SSHKey{
			ID:         shortToken(),
			Name:       "Auto-generated",
			PublicKey:  pubKey,
			PrivateKey: privKey,
			IsDefault:  true,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}
		if kerr := db.CreateSSHKey(sshKey); kerr != nil {
			return nil, fmt.Errorf("failed to save SSH keypair: %w", kerr)
		}
		if kerr := addToAuthorizedKeys(sshKey.PublicKey); kerr != nil {
			fmt.Printf("WARN: could not add auto-generated key to authorized_keys: %v\n", kerr)
		}
	}

	var tunnelPort int
	if bt.TunnelPort != 0 {
		exists, portErr := db.CheckRatholePortExists(bt.TunnelPort)
		if portErr != nil {
			return nil, fmt.Errorf("failed to check port availability: %w", portErr)
		}
		if exists {
			return nil, fmt.Errorf("port %d is already in use by another tunnel", bt.TunnelPort)
		}
		tunnelPort = bt.TunnelPort
	} else {
		tunnelPort, err = db.NextRatholePort()
		if err != nil {
			return nil, fmt.Errorf("failed to allocate tunnel port: %w", err)
		}
	}

	ratholeToken := shortToken()

	// Allocate the agent back-channel up front. Pass tunnelPort to exclude
	// it from consideration: the SSH tunnel port we just picked isn't in
	// the DB yet (we haven't created the Machine row), so without the
	// exclude both calls would return the same port and rathole-server
	// would try to bind two services to the same address.
	//
	// Even if the bootstrap script fails to install the agent (older script,
	// network glitch), we keep the fields populated so the existing-machine
	// migration tool can complete it.
	agentRemotePort, err := db.NextRatholePort(tunnelPort)
	if err != nil {
		return nil, fmt.Errorf("failed to allocate agent port: %w", err)
	}
	agentToken := shortToken()         // bearer token for HTTP auth
	agentRatholeToken := shortToken()  // rathole-tunnel auth (separate)

	machine := &db.Machine{
		ID:                shortToken(),
		Name:              req.Name,
		Username:          req.Username,
		TunnelPort:        tunnelPort,
		RatholeSSHToken:   ratholeToken,
		SSHKeyID:          sshKey.ID,
		PublicSSH:         bt.PublicSSH,
		Status:            "pending",
		AgentToken:        agentToken,
		AgentLocalPort:    agentLocalPortDefault,
		AgentRemotePort:   agentRemotePort,
		AgentRatholeToken: agentRatholeToken,
		AgentInstalled:    false,
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}
	if err := db.CreateMachine(machine); err != nil {
		return nil, fmt.Errorf("failed to create machine: %w", err)
	}
	db.LogEvent("machine_registered", machine.ID, machine.Name)
	if err := db.MarkTokenUsed(bt.ID, machine.ID); err != nil {
		return nil, fmt.Errorf("failed to mark token used: %w", err)
	}

	// Add rathole service entry so the tunnel port opens immediately.
	if err := s.local.AddMachineSSHTunnel(machine); err != nil {
		fmt.Printf("WARN: failed to add rathole tunnel for machine %s: %v\n", machine.ID, err)
	}
	// Open (or restrict) the SSH tunnel port in the firewall.
	// No-op when firewall mode is not "gopher"; safe to call unconditionally.
	ApplyTunnelPort(machine.TunnelPort, "tcp", !machine.PublicSSH)

	// Derive rathole server address from the request host (strip port if present).
	ratholeHost := serverHost
	if h, _, err := net.SplitHostPort(serverHost); err == nil {
		ratholeHost = h
	}

	ratholeConfig := config.GenerateMachineSSHClientConfig(ratholeHost, machine)

	// Async: wait for tunnel then verify SSH connectivity.
	go s.awaitSSHHealth(machine, sshKey.PrivateKey)
	// Async: poll the agent's back-channel until it answers, so the bootstrap
	// modal can flip "machine registered" → "agent ready" within seconds of
	// the agent service starting on the client (vs. waiting up to a minute
	// for the next health-poll cycle).
	go s.awaitAgentReady(machine)

	return &BootstrapResponse{
		TunnelPort:      tunnelPort,
		RatholeToken:    ratholeToken,
		VPSPublicKey:    sshKey.PublicKey,
		RatholeConfig:   ratholeConfig,
		VPSHost:         ratholeHost,
		AgentToken:      agentToken,
		AgentLocalPort:  agentLocalPortDefault,
		AgentRemotePort: agentRemotePort,
	}, nil
}

const bootstrapSSHHealthTimeout = 4 * time.Minute

// awaitSSHHealth polls localhost:tunnelPort for initial bootstrap SSH readiness.
// Some machines take longer than a minute on first bootstraps (package installs,
// systemd startup), so timeout keeps status as "pending" instead of hard-failing.
//
// Uses SetMachineStatus (column-level UPDATE) rather than UpdateMachine
// (DB.Save full-row replace) to avoid clobbering AgentInstalled. This
// goroutine runs concurrently with awaitAgentReady — both hold the same
// in-memory *db.Machine struct that Register handed them, with its stale
// AgentInstalled=false. Saving that struct after awaitAgentReady has already
// flipped agent_installed=true in the DB would race-revert the flag, making
// the dashboard's agent badge oscillate "Install Agent" → "v0.x" → "Install
// Agent" until the next health-poll cycle re-promoted it.
func (s *BootstrapService) awaitSSHHealth(machine *db.Machine, privateKey string) {
	deadline := time.Now().Add(bootstrapSSHHealthTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		c, err := sshpkg.NewClient(TunnelDialHost(machine), machine.TunnelPort, machine.Username, privateKey)
		if err != nil {
			continue
		}
		c.Close()
		now := time.Now()
		_ = db.SetMachineStatus(machine.ID, "connected", &now)
		return
	}
	// Keep machine in pending state; monitor loop can flip to connected once it
	// observes successful SSH after slower bootstrap completions.
	_ = db.SetMachineStatus(machine.ID, "pending", nil)
}

const (
	bootstrapAgentReadyTimeout = 5 * time.Minute
	bootstrapAgentReadyPoll    = 3 * time.Second
)

// awaitAgentReady polls the agent's /status endpoint via the rathole back-channel
// until it answers, then marks Machine.AgentInstalled=true. The first poll fires
// 3s after the call so the bootstrap script has a head start.
//
// Without this, agent_installed stays false until the next 60s HealthService
// cycle catches it, which makes the bootstrap UX feel like the agent is
// hanging when it's actually already up.
//
// The probe re-fetches the machine each iteration: the bootstrap script writes
// a fresh AgentToken via /api/bootstrap before the agent comes online, but the
// in-memory copy here is from before the script ran — re-reading keeps us in
// sync if anything else (the migration tool, manual edits) updates the row.
func (s *BootstrapService) awaitAgentReady(machine *db.Machine) {
	if machine.AgentRemotePort == 0 || machine.AgentToken == "" {
		return // older bootstrap path with no agent fields — nothing to wait for
	}
	deadline := time.Now().Add(bootstrapAgentReadyTimeout)
	time.Sleep(bootstrapAgentReadyPoll)
	for time.Now().Before(deadline) {
		current, err := db.GetMachine(machine.ID)
		if err != nil {
			return // machine deleted (or DB unreachable) — stop polling
		}
		if current.AgentInstalled {
			return // health service or another path already flipped it
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		client := NewAgentClient(current)
		status, err := client.Status(ctx)
		cancel()
		if err == nil {
			now := time.Now()
			_ = db.SetMachineAgentSeen(current.ID, status.AgentVersion, now)
			db.LogEvent("agent_ready", current.ID, current.Name)
			return
		}
		time.Sleep(bootstrapAgentReadyPoll)
	}
}

// shortToken returns 16 random hex characters (8 bytes of entropy).
// Shorter and easier to read/copy than a UUID while still being unguessable.
func shortToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
