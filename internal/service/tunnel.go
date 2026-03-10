package service

import (
	"fmt"
	"time"

	"github.com/smalex-z/gopher/internal/api/dto"
	"github.com/smalex-z/gopher/internal/config"
	"github.com/smalex-z/gopher/internal/db"
	apperrors "github.com/smalex-z/gopher/internal/errors"
)

type TunnelService struct {
	local *LocalSetupService
}

func NewTunnelService(local *LocalSetupService) *TunnelService {
	return &TunnelService{local: local}
}

func (s *TunnelService) List() ([]db.Tunnel, error) {
return db.GetTunnels()
}

func (s *TunnelService) ListByMachine(machineID string) ([]db.Tunnel, error) {
return db.GetTunnelsByMachine(machineID)
}

func (s *TunnelService) Get(id string) (*db.Tunnel, error) {
	return db.GetTunnel(id)
}

func (s *TunnelService) NextPort() (int, error) {
	return db.NextRatholePort()
}

func (s *TunnelService) Create(req dto.CreateTunnelRequest) (*db.Tunnel, error) {
	if req.Subdomain != "" {
		if err := config.ValidateSubdomain(req.Subdomain); err != nil {
			return nil, &apperrors.ValidationError{Field: "subdomain", Message: err.Error()}
		}
		exists, err := db.CheckSubdomainExists(req.Subdomain)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, &apperrors.ConflictError{Message: "subdomain already exists"}
		}
	}
	if err := config.ValidatePort(req.LocalPort); err != nil {
		return nil, &apperrors.ValidationError{Field: "local_port", Message: err.Error()}
	}

	var ratholePort int
	if req.RatholePort >= 20000 {
		if req.RatholePort > 65535 {
			return nil, &apperrors.ValidationError{Field: "rathole_port", Message: "port must be between 20000 and 65535"}
		}
		exists, err := db.CheckRatholePortExists(req.RatholePort)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, &apperrors.ConflictError{Message: fmt.Sprintf("server port %d is already in use by another tunnel", req.RatholePort)}
		}
		ratholePort = req.RatholePort
	} else {
		var err error
		ratholePort, err = db.NextRatholePort()
		if err != nil {
			return nil, err
		}
	}

	tunnel := &db.Tunnel{
		ID:           shortToken(),
		MachineID:    req.MachineID,
		Name:         req.Name,
		Subdomain:    req.Subdomain,
		LocalPort:    req.LocalPort,
		RatholePort:  ratholePort,
		RatholeToken: shortToken(),
		Protocol:     "tcp",
		Status:       "inactive",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	if err := db.CreateTunnel(tunnel); err != nil {
		return nil, err
	}

	// Push configs to server + client (non-fatal: tunnel is saved even if this fails)
	machine, machErr := db.GetMachine(req.MachineID)
	if machErr == nil {
		if cfgErr := s.local.AddServiceTunnel(tunnel, machine); cfgErr != nil {
			// Annotate the tunnel with the error but don't fail the creation
			tunnel.Status = fmt.Sprintf("config-error: %v", cfgErr)
			_ = db.UpdateTunnel(tunnel)
		}
	}

	return tunnel, nil
}

func (s *TunnelService) Update(id string, req dto.UpdateTunnelRequest) (*db.Tunnel, error) {
tunnel, err := db.GetTunnel(id)
if err != nil {
return nil, err
}

if req.Subdomain != tunnel.Subdomain {
if err := config.ValidateSubdomain(req.Subdomain); err != nil {
return nil, &apperrors.ValidationError{Field: "subdomain", Message: err.Error()}
}
exists, err := db.CheckSubdomainExists(req.Subdomain)
if err != nil {
return nil, err
}
if exists {
return nil, &apperrors.ConflictError{Message: "subdomain already exists"}
}
tunnel.Subdomain = req.Subdomain
}

tunnel.Name = req.Name
tunnel.LocalPort = req.LocalPort
tunnel.UpdatedAt = time.Now()

if err := db.UpdateTunnel(tunnel); err != nil {
return nil, err
}
return tunnel, nil
}

func (s *TunnelService) Delete(id string) error {
	tunnel, err := db.GetTunnel(id)
	if err != nil {
		return err
	}
	machine, machErr := db.GetMachine(tunnel.MachineID)
	if machErr == nil {
		s.local.RemoveServiceTunnel(tunnel, machine)
	}
	return db.DeleteTunnel(id)
}
