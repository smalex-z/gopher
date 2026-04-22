package service

import (
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
}

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
		tunnelPort, err = db.NextSSHTunnelPort()
		if err != nil {
			return nil, fmt.Errorf("failed to allocate tunnel port: %w", err)
		}
	}

	ratholeToken := shortToken()

	machine := &db.Machine{
		ID:              shortToken(),
		Name:            req.Name,
		Username:        req.Username,
		TunnelPort:      tunnelPort,
		RatholeSSHToken: ratholeToken,
		SSHKeyID:        sshKey.ID,
		PublicSSH:       bt.PublicSSH,
		Status:          "pending",
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := db.CreateMachine(machine); err != nil {
		return nil, fmt.Errorf("failed to create machine: %w", err)
	}
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

	return &BootstrapResponse{
		TunnelPort:    tunnelPort,
		RatholeToken:  ratholeToken,
		VPSPublicKey:  sshKey.PublicKey,
		RatholeConfig: ratholeConfig,
		VPSHost:       ratholeHost,
	}, nil
}

const bootstrapSSHHealthTimeout = 4 * time.Minute

// awaitSSHHealth polls localhost:tunnelPort for initial bootstrap SSH readiness.
// Some machines take longer than a minute on first bootstraps (package installs,
// systemd startup), so timeout keeps status as "pending" instead of hard-failing.
func (s *BootstrapService) awaitSSHHealth(machine *db.Machine, privateKey string) {
	deadline := time.Now().Add(bootstrapSSHHealthTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		c, err := sshpkg.NewClient(TunnelDialHost(machine), machine.TunnelPort, machine.Username, privateKey)
		if err != nil {
			continue
		}
		c.Close()
		machine.Status = "connected"
		now := time.Now()
		machine.LastSeen = &now
		_ = db.UpdateMachine(machine)
		return
	}
	// Keep machine in pending state; monitor loop can flip to connected once it
	// observes successful SSH after slower bootstrap completions.
	machine.Status = "pending"
	_ = db.UpdateMachine(machine)
}

// shortToken returns 16 random hex characters (8 bytes of entropy).
// Shorter and easier to read/copy than a UUID while still being unguessable.
func shortToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
