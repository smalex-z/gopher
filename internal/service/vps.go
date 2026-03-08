package service

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/smalex-z/gopher/internal/api/dto"
	"github.com/smalex-z/gopher/internal/db"
	sshpkg "github.com/smalex-z/gopher/internal/ssh"
)

type VPSService struct {
deploy *DeployService
}

func NewVPSService(deploy *DeployService) *VPSService {
return &VPSService{deploy: deploy}
}

func (s *VPSService) Get() (*db.VPSConfig, error) {
return db.GetVPS()
}

func (s *VPSService) Create(req dto.CreateVPSRequest) (*db.VPSConfig, error) {
client, err := sshpkg.NewClient(req.Host, req.Port, req.Username, req.PrivateKey)
if err != nil {
return nil, fmt.Errorf("SSH connection test failed: %w", err)
}
client.Close()

privKey, pubKey, err := sshpkg.GenerateRSAKeypair()
if err != nil {
return nil, fmt.Errorf("failed to generate SSH keypair: %w", err)
}

vps := &db.VPSConfig{
ID:            uuid.New().String(),
Host:          req.Host,
Port:          req.Port,
Username:      req.Username,
PrivateKey:    req.PrivateKey,
Domain:        req.Domain,
SSHPublicKey:  pubKey,
SSHPrivateKey: privKey,
CreatedAt:     time.Now(),
UpdatedAt:     time.Now(),
}
if vps.Port == 0 {
vps.Port = 22
}

if err := db.CreateVPS(vps); err != nil {
return nil, err
}
return vps, nil
}

func (s *VPSService) Update(id string, req dto.UpdateVPSRequest) (*db.VPSConfig, error) {
vps, err := db.GetVPS()
if err != nil {
return nil, err
}

vps.Host = req.Host
vps.Port = req.Port
vps.Username = req.Username
if req.PrivateKey != "" {
vps.PrivateKey = req.PrivateKey
}
vps.Domain = req.Domain
vps.UpdatedAt = time.Now()

if err := db.UpdateVPS(vps); err != nil {
return nil, err
}
return vps, nil
}

func (s *VPSService) Delete(id string) error {
return db.DeleteVPS(id)
}

func (s *VPSService) Bootstrap() error {
vps, err := db.GetVPS()
if err != nil {
return err
}
go s.deploy.Bootstrap(vps)
return nil
}

func (s *VPSService) Deploy() error {
vps, err := db.GetVPS()
if err != nil {
return err
}
go s.deploy.DeployVPS(vps)
return nil
}

func (s *VPSService) Status() (map[string]interface{}, error) {
vps, err := db.GetVPS()
if err != nil {
return nil, err
}

client, err := sshpkg.NewClient(vps.Host, vps.Port, vps.Username, vps.PrivateKey)
if err != nil {
return map[string]interface{}{
"connected": false,
"error":     err.Error(),
}, nil
}
defer client.Close()

output, err := client.Execute("docker ps --format '{{.Names}}: {{.Status}}'")
if err != nil {
return map[string]interface{}{
"connected": true,
"docker":    "error: " + err.Error(),
}, nil
}

return map[string]interface{}{
"connected": true,
"services":  output,
}, nil
}
