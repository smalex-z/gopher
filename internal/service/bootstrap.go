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
func (s *BootstrapService) GenerateToken() (*db.BootstrapToken, error) {
	bt := &db.BootstrapToken{
		ID:        shortToken(),
		Token:     shortToken(),
		ExpiresAt: time.Now().Add(time.Hour),
		CreatedAt: time.Now(),
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

	// Ensure the server has an SSH keypair for connecting back to machines.
	settings, err := db.GetSettings()
	if err != nil {
		return nil, fmt.Errorf("settings unavailable: %w", err)
	}
	if settings.SSHPublicKey == "" {
		privKey, pubKey, kerr := sshpkg.GenerateRSAKeypair()
		if kerr != nil {
			return nil, fmt.Errorf("failed to generate SSH keypair: %w", kerr)
		}
		settings.SSHPublicKey = pubKey
		settings.SSHPrivateKey = privKey
		if err := db.SaveSettings(settings); err != nil {
			return nil, fmt.Errorf("failed to save SSH keypair: %w", err)
		}
	}

	tunnelPort, err := db.NextSSHTunnelPort()
	if err != nil {
		return nil, fmt.Errorf("failed to allocate tunnel port: %w", err)
	}

	ratholeToken := shortToken()

	machine := &db.Machine{
		ID:              shortToken(),
		Name:            req.Name,
		Username:        req.Username,
		TunnelPort:      tunnelPort,
		RatholeSSHToken: ratholeToken,
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

	// Derive rathole server address from the request host (strip port if present).
	ratholeHost := serverHost
	if h, _, err := net.SplitHostPort(serverHost); err == nil {
		ratholeHost = h
	}

	ratholeConfig := config.GenerateMachineSSHClientConfig(ratholeHost, machine)

	// Async: wait for tunnel then verify SSH connectivity.
	go s.awaitSSHHealth(machine, settings.SSHPrivateKey)

	return &BootstrapResponse{
		TunnelPort:    tunnelPort,
		RatholeToken:  ratholeToken,
		VPSPublicKey:  settings.SSHPublicKey,
		RatholeConfig: ratholeConfig,
		VPSHost:       ratholeHost,
	}, nil
}

// awaitSSHHealth polls localhost:tunnelPort for up to 60 s, then marks the
// machine status as "connected" or "failed".
func (s *BootstrapService) awaitSSHHealth(machine *db.Machine, privateKey string) {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		c, err := sshpkg.NewClient("localhost", machine.TunnelPort, machine.Username, privateKey)
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
	machine.Status = "failed"
	_ = db.UpdateMachine(machine)
}

// shortToken returns 16 random hex characters (8 bytes of entropy).
// Shorter and easier to read/copy than a UUID while still being unguessable.
func shortToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
