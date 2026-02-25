package service

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/smalex-z/gopher/internal/config"
	"github.com/smalex-z/gopher/internal/db"
)

type BootstrapService struct {
	deploy *DeployService
}

func NewBootstrapService(deploy *DeployService) *BootstrapService {
	return &BootstrapService{deploy: deploy}
}

// GenerateToken creates a one-time bootstrap token valid for 1 hour.
func (s *BootstrapService) GenerateToken() (*db.BootstrapToken, error) {
	bt := &db.BootstrapToken{
		ID:        uuid.New().String(),
		Token:     uuid.New().String(),
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

// Register validates token, creates machine record, returns rathole config.
func (s *BootstrapService) Register(req BootstrapRequest) (*BootstrapResponse, error) {
	bt, err := db.GetBootstrapToken(req.Token)
	if err != nil {
		return nil, fmt.Errorf("invalid token")
	}
	if bt.UsedAt != nil {
		return nil, fmt.Errorf("token already used")
	}
	if time.Now().After(bt.ExpiresAt) {
		return nil, fmt.Errorf("token expired")
	}

	vps, err := db.GetVPS()
	if err != nil {
		return nil, fmt.Errorf("VPS not configured: %w", err)
	}

	tunnelPort, err := db.NextSSHTunnelPort()
	if err != nil {
		return nil, fmt.Errorf("failed to allocate tunnel port: %w", err)
	}

	ratholeToken := uuid.New().String()

	machine := &db.Machine{
		ID:              uuid.New().String(),
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

	ratholeConfig := config.GenerateMachineSSHClientConfig(vps.Host, machine)

	return &BootstrapResponse{
		TunnelPort:    tunnelPort,
		RatholeToken:  ratholeToken,
		VPSPublicKey:  vps.SSHPublicKey,
		RatholeConfig: ratholeConfig,
		VPSHost:       vps.Host,
	}, nil
}
