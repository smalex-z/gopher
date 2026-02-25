package service

import (
"time"

"github.com/google/uuid"
"github.com/smalex-z/gopher/internal/api/dto"
"github.com/smalex-z/gopher/internal/config"
"github.com/smalex-z/gopher/internal/db"
apperrors "github.com/smalex-z/gopher/internal/errors"
)

type TunnelService struct{}

func NewTunnelService() *TunnelService {
return &TunnelService{}
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

func (s *TunnelService) Create(req dto.CreateTunnelRequest) (*db.Tunnel, error) {
if err := config.ValidateSubdomain(req.Subdomain); err != nil {
return nil, &apperrors.ValidationError{Field: "subdomain", Message: err.Error()}
}
if err := config.ValidatePort(req.LocalPort); err != nil {
return nil, &apperrors.ValidationError{Field: "local_port", Message: err.Error()}
}

exists, err := db.CheckSubdomainExists(req.Subdomain)
if err != nil {
return nil, err
}
if exists {
return nil, &apperrors.ConflictError{Message: "subdomain already exists"}
}

ratholePort, err := db.NextRatholePort()
if err != nil {
return nil, err
}

protocol := req.Protocol
if protocol == "" {
protocol = "http"
}

tunnel := &db.Tunnel{
ID:          uuid.New().String(),
MachineID:   req.MachineID,
Name:        req.Name,
Subdomain:   req.Subdomain,
LocalPort:   req.LocalPort,
RatholePort: ratholePort,
Protocol:    protocol,
Status:      "inactive",
CreatedAt:   time.Now(),
UpdatedAt:   time.Now(),
}

if err := db.CreateTunnel(tunnel); err != nil {
return nil, err
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
tunnel.Protocol = req.Protocol
tunnel.UpdatedAt = time.Now()

if err := db.UpdateTunnel(tunnel); err != nil {
return nil, err
}
return tunnel, nil
}

func (s *TunnelService) Delete(id string) error {
return db.DeleteTunnel(id)
}
