package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/config"
	"github.com/smalex-z/gopher/internal/db"
	apperrors "github.com/smalex-z/gopher/internal/errors"
	sshpkg "github.com/smalex-z/gopher/internal/ssh"
)

type BootstrapService struct {
	local *LocalSetupService
	// rl throttles the public Register / Migrate endpoints by source IP.
	// Same shape as the auth login limiter — 10 attempts per 5 minutes
	// per IP. Random tokens have 64-bit entropy so brute-force is
	// already impractical, but this caps the rate at which an attacker
	// can trigger DB-touching codepaths from an unauthenticated endpoint.
	rl *loginRateLimiter
}

func NewBootstrapService(local *LocalSetupService) *BootstrapService {
	return &BootstrapService{local: local, rl: newLoginRateLimiter()}
}

// AllowAttempt records a hit from ip and returns false when the rate
// limiter has tripped. Called from the bootstrap handler before any DB
// work — short-circuits at the cost of one map lookup.
func (s *BootstrapService) AllowAttempt(ip string) bool {
	return s.rl.record(ip)
}

// GenerateToken creates a one-time bootstrap token valid for 1 hour.
// tunnelPort optionally pre-assigns the SSH tunnel port (0 = auto-allocate).
// sshKeyID optionally pins the SSH key to install on the machine (empty = use default).
func (s *BootstrapService) GenerateToken(tunnelPort int, sshKeyID string, publicSSH, sshEnabled bool) (*db.BootstrapToken, error) {
	// Validate a pinned port up front so the operator hears about a bad choice
	// at the dashboard, not when the bootstrap script fails on the client an
	// hour later. Range/privilege check plus the same DB + live-OS availability
	// checks the tunnel-create path applies (a port that's DB-free but held by
	// a process — Caddy, sshd, the dashboard — passes the DB check and then
	// silently fails at rathole bind time). Register's claim path re-checks:
	// this is advisory, that one is authoritative.
	if tunnelPort != 0 {
		if err := config.ValidatePort(tunnelPort); err != nil {
			return nil, &apperrors.ValidationError{Field: "tunnel_port", Message: err.Error()}
		}
		if exists, err := db.CheckRatholePortExists(tunnelPort); err == nil && exists {
			return nil, &apperrors.ValidationError{Field: "tunnel_port", Message: fmt.Sprintf("port %d is already assigned to another tunnel or machine", tunnelPort)}
		}
		if !db.PortAvailable(tunnelPort) {
			return nil, &apperrors.ValidationError{Field: "tunnel_port", Message: fmt.Sprintf("port %d is already in use by a process on the server", tunnelPort)}
		}
	}
	bt := &db.BootstrapToken{
		ID:         shortToken(),
		Token:      secretToken(),
		ExpiresAt:  time.Now().Add(time.Hour),
		CreatedAt:  time.Now(),
		TunnelPort: tunnelPort,
		SSHKeyID:   sshKeyID,
		PublicSSH:  publicSSH,
		SSHEnabled: sshEnabled,
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
	// NoSSH lets the client force an agent-only machine (bootstrap.sh --no-ssh),
	// overriding the token even if it was generated with SSH enabled. SSH can
	// only be turned OFF here, never on — enabling requires a key choice, which
	// lives in the token.
	NoSSH bool `json:"no_ssh"`
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
	// ClaimBootstrapToken atomically marks the token used: two parallel
	// Register calls with the same token can't both proceed past this point.
	// The token row's machine_id is populated below once CreateMachine
	// succeeds (BindBootstrapTokenToMachine).
	bt, err := db.ClaimBootstrapToken(req.Token)
	if err != nil {
		return nil, fmt.Errorf("invalid or expired token")
	}

	// SSH is provisioned only when the token asked for it AND the client didn't
	// opt out via --no-ssh. Disabled = agent-only machine: no SSH back-tunnel,
	// no authorized_keys entry, control entirely over the agent channel.
	sshEnabled := bt.SSHEnabled && !req.NoSSH

	// Retrieve the SSH key to install — only when SSH is enabled. Order:
	// token-pinned key → default → auto-generate. Agent-only machines skip this
	// entirely (nil key, no key material touched).
	var sshKey *db.SSHKey
	if sshEnabled {
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
	}

	ratholeToken := secretToken()
	agentToken := secretToken()        // bearer token for HTTP auth
	agentRatholeToken := secretToken() // rathole-tunnel auth (separate)

	// Allocate ports + create the machine row inside a retry loop. Two
	// concurrent bootstrap requests can both pick the same port via
	// NextRatholePort (which doesn't lock); the partial unique indexes on
	// machines.tunnel_port and machines.agent_remote_port catch the second
	// INSERT and we re-pick the next gap.
	machine, err := allocatePortsAndCreateMachine(req, bt, sshKey, ratholeToken, agentToken, agentRatholeToken, sshEnabled)
	if err != nil {
		return nil, err
	}
	db.LogEvent("machine_registered", machine.ID, machine.Name)
	// Token was already marked used atomically by ClaimBootstrapToken; just
	// fill in machine_id so the FK back-reference is set. A failure here is
	// non-fatal: the machine exists, the token is unreusable, the only loss
	// is the token→machine pointer which the dashboard doesn't depend on.
	if err := db.BindBootstrapTokenToMachine(bt.ID, machine.ID); err != nil {
		fmt.Printf("WARN: failed to bind bootstrap token %s to machine %s: %v\n", bt.ID, machine.ID, err)
	}

	// Add rathole service entry so the tunnel port opens immediately.
	if err := s.local.AddMachineSSHTunnel(machine); err != nil {
		fmt.Printf("WARN: failed to add rathole tunnel for machine %s: %v\n", machine.ID, err)
	}
	// Open the SSH tunnel port in the firewall — only for SSH-enabled machines
	// (agent-only machines have no SSH port). Public SSH ports get per-source-IP
	// rate-limiting; jumpbox (private) ports are restricted to loopback.
	// No-op when firewall mode is not "gopher".
	if sshEnabled && machine.TunnelPort > 0 {
		if machine.PublicSSH {
			ApplyPublicSSHPort(machine.TunnelPort)
		} else {
			ApplyTunnelPort(machine.TunnelPort, "tcp", true)
		}
	}

	// Derive rathole server address from the request host (strip port if present).
	ratholeHost := serverHost
	if h, _, err := net.SplitHostPort(serverHost); err == nil {
		ratholeHost = h
	}

	noisePub := ""
	if settings, sErr := db.GetSettings(); sErr == nil && settings != nil {
		noisePub = settings.RatholeNoisePubKey
	}
	ratholeConfig := config.GenerateMachineSSHClientConfig(ratholeHost, machine, noisePub)

	// Only hand back a public key to install in authorized_keys when SSH is on.
	// Agent-only machines get "" → bootstrap.sh skips the authorized_keys step.
	vpsPublicKey := ""
	if sshEnabled && sshKey != nil {
		vpsPublicKey = sshKey.PublicKey
	}

	// Async: wait for the tunnel to come up (TCP probe, no SSH).
	go goSafe("awaitTunnelHealth", func() { s.awaitTunnelHealth(machine) })
	// Async: poll the agent's back-channel until it answers, so the bootstrap
	// modal can flip "machine registered" → "agent ready" within seconds of
	// the agent service starting on the client (vs. waiting up to a minute
	// for the next health-poll cycle).
	go goSafe("awaitAgentReady", func() { s.awaitAgentReady(machine) })

	return &BootstrapResponse{
		TunnelPort:      machine.TunnelPort,
		RatholeToken:    ratholeToken,
		VPSPublicKey:    vpsPublicKey,
		RatholeConfig:   ratholeConfig,
		VPSHost:         ratholeHost,
		AgentToken:      agentToken,
		AgentLocalPort:  agentLocalPortDefault,
		AgentRemotePort: machine.AgentRemotePort,
	}, nil
}

// allocatePortsAndCreateMachine performs the port-pick + INSERT under a retry
// loop bounded by the partial unique indexes on machines.tunnel_port and
// machines.agent_remote_port. NextRatholePort is a non-locking SELECT, so two
// concurrent bootstraps can both pick the same gap; the second INSERT trips
// the unique constraint and we re-scan for a fresh gap. Capped at a few
// attempts so a runaway scan can't busy-loop the DB.
func allocatePortsAndCreateMachine(req BootstrapRequest, bt *db.BootstrapToken, sshKey *db.SSHKey, ratholeToken, agentToken, agentRatholeToken string, sshEnabled bool) (*db.Machine, error) {
	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		// SSH tunnel port is allocated only for SSH-enabled machines. Agent-only
		// machines run with TunnelPort=0 (exempt from the tunnel_port unique
		// index, WHERE tunnel_port > 0) and no SSH service in the config.
		tunnelPort := 0
		if sshEnabled {
			if bt.TunnelPort != 0 {
				// Token pinned a port. We can't re-pick on conflict — fail
				// immediately if it's taken (the operator picked a port that's
				// already in use, that's a config error not a race).
				exists, portErr := db.CheckRatholePortExists(bt.TunnelPort)
				if portErr != nil {
					return nil, fmt.Errorf("failed to check port availability: %w", portErr)
				}
				if exists {
					return nil, fmt.Errorf("port %d is already in use by another tunnel", bt.TunnelPort)
				}
				// DB-free isn't enough — the port must also be free on the box,
				// same as the tunnel-create path. Without this a pinned port
				// held by a live process passes validation and then silently
				// fails at rathole bind time.
				if !db.PortAvailable(bt.TunnelPort) {
					return nil, fmt.Errorf("port %d is already in use by a process on the server", bt.TunnelPort)
				}
				tunnelPort = bt.TunnelPort
			} else {
				var err error
				tunnelPort, err = db.NextRatholePort()
				if err != nil {
					return nil, fmt.Errorf("failed to allocate tunnel port: %w", err)
				}
			}
		}

		// Pass tunnelPort to exclude it from the agent allocation: the SSH
		// tunnel port we just picked isn't in the DB yet, so without the
		// exclude both calls would return the same port and rathole-server
		// would try to bind two services to the same address. (Excluding 0 for
		// agent-only machines is a harmless no-op.)
		agentRemotePort, err := db.NextRatholePort(tunnelPort)
		if err != nil {
			return nil, fmt.Errorf("failed to allocate agent port: %w", err)
		}

		// SSH identity fields only when SSH is on; agent-only machines leave them
		// zero so every SSH gate (TunnelPort>0 / SSHKeyID) short-circuits.
		sshKeyID := ""
		sshToken := ""
		publicSSH := false
		if sshEnabled {
			sshKeyID = sshKey.ID
			sshToken = ratholeToken
			publicSSH = bt.PublicSSH
		}

		machine := &db.Machine{
			ID:                shortToken(),
			Name:              req.Name,
			Username:          req.Username,
			TunnelPort:        tunnelPort,
			RatholeSSHToken:   sshToken,
			SSHKeyID:          sshKeyID,
			PublicSSH:         publicSSH,
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
			lastErr = err
			// SQLite reports "UNIQUE constraint failed: machines.tunnel_port"
			// (or .agent_remote_port) when our partial indexes catch a race.
			// Retry — NextRatholePort will skip the now-claimed port.
			if isUniqueConstraintErr(err) {
				continue
			}
			return nil, fmt.Errorf("failed to create machine: %w", err)
		}
		return machine, nil
	}
	return nil, fmt.Errorf("failed to allocate machine ports after %d attempts: %w", maxAttempts, lastErr)
}

// isUniqueConstraintErr probes the SQLite error string for the unique-violation
// signature. We don't have a typed error from the driver here, so we fall back
// to a stable substring match.
func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "constraint failed: unique")
}

const bootstrapTunnelHealthTimeout = 4 * time.Minute

// awaitTunnelHealth polls the machine's rathole tunnel port for initial
// connectivity — a plain TCP connect, no SSH. A successful dial means the
// rathole-client connected out and the VPS-side port is forwarding, which is
// exactly the "machine is reachable" signal we want, without the server needing
// an SSH private key. Some machines take longer than a minute on first
// bootstraps (package installs, systemd startup), so on timeout we leave the
// status "pending" rather than hard-failing.
//
// Uses SetMachineStatus (column-level UPDATE) rather than UpdateMachine
// (DB.Save full-row replace) to avoid clobbering AgentInstalled. This
// goroutine runs concurrently with awaitAgentReady — both hold the same
// in-memory *db.Machine struct that Register handed them, with its stale
// AgentInstalled=false. Saving that struct after awaitAgentReady has already
// flipped agent_installed=true in the DB would race-revert the flag.
func (s *BootstrapService) awaitTunnelHealth(machine *db.Machine) {
	// Agent-only machines have no SSH tunnel port to probe — dialing :0 would
	// just fail for the whole window and then clobber the status back to
	// "pending", overwriting a "connected" that awaitAgentReady may have set.
	// Their readiness signal is the agent back-channel (awaitAgentReady).
	if machine.TunnelPort == 0 {
		return
	}
	deadline := time.Now().Add(bootstrapTunnelHealthTimeout)
	addr := net.JoinHostPort(TunnelDialHost(machine), fmt.Sprintf("%d", machine.TunnelPort))
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			continue
		}
		_ = conn.Close()
		now := time.Now()
		_ = db.SetMachineStatus(machine.ID, "connected", &now)
		return
	}
	// Keep machine in pending state; the monitor/health loop flips to connected
	// once it observes reachability after slower bootstrap completions.
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
// For record IDs only — anything that authenticates a caller (bootstrap,
// agent, rathole, migration tokens) must use secretToken instead.
//
// Panics on crypto/rand failure — silently returning the zero byte slice
// would mint deterministic "tokens" that anyone could replay. On Linux
// post-boot rand.Read essentially never fails; if it does, the system is
// without entropy and we'd rather crash than issue a fake token.
func shortToken() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failure in shortToken: %v", err))
	}
	return hex.EncodeToString(b)
}

// secretToken returns 32 random hex characters (16 bytes / 128 bits of
// entropy) for credentials: bearer tokens, rathole tokens, bootstrap and
// migration tokens. 64-bit shortToken values are unguessable for IDs but too
// thin for credentials on a public endpoint. Existing machines keep the
// 64-bit tokens they were minted with; rotation is tracked post-release.
func secretToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failure in secretToken: %v", err))
	}
	return hex.EncodeToString(b)
}
