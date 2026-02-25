package service

import (
"fmt"
"time"

"github.com/google/uuid"
"github.com/smalex-z/gopher/internal/api/dto"
"github.com/smalex-z/gopher/internal/db"
sshpkg "github.com/smalex-z/gopher/internal/ssh"
)

type MachineService struct {
deploy *DeployService
}

func NewMachineService(deploy *DeployService) *MachineService {
return &MachineService{deploy: deploy}
}

func (s *MachineService) List() ([]db.Machine, error) {
return db.GetMachines()
}

func (s *MachineService) Get(id string) (*db.Machine, error) {
return db.GetMachine(id)
}

func (s *MachineService) Create(req dto.CreateMachineRequest) (*db.Machine, error) {
machine := &db.Machine{
ID:         uuid.New().String(),
Name:       req.Name,
Host:       req.Host,
Port:       req.Port,
Username:   req.Username,
PrivateKey: req.PrivateKey,
Status:     "pending",
CreatedAt:  time.Now(),
UpdatedAt:  time.Now(),
}
if machine.Port == 0 {
machine.Port = 22
}

if err := db.CreateMachine(machine); err != nil {
return nil, err
}
return machine, nil
}

func (s *MachineService) Update(id string, req dto.UpdateMachineRequest) (*db.Machine, error) {
machine, err := db.GetMachine(id)
if err != nil {
return nil, err
}

machine.Name = req.Name
machine.Host = req.Host
machine.Port = req.Port
machine.Username = req.Username
if req.PrivateKey != "" {
machine.PrivateKey = req.PrivateKey
}
machine.UpdatedAt = time.Now()

if err := db.UpdateMachine(machine); err != nil {
return nil, err
}
return machine, nil
}

func (s *MachineService) Delete(id string) error {
return db.DeleteMachine(id)
}

func (s *MachineService) Deploy(id string) error {
machine, err := db.GetMachine(id)
if err != nil {
return err
}

vps, err := db.GetVPS()
if err != nil {
return fmt.Errorf("VPS not configured: %w", err)
}

go s.deploy.DeployClient(machine, vps)
return nil
}

func (s *MachineService) Status(id string) (map[string]interface{}, error) {
machine, err := db.GetMachine(id)
if err != nil {
return nil, err
}

vps, _ := db.GetVPS()

var client *sshpkg.SSHClient
if vps != nil && machine.TunnelPort > 0 && vps.SSHPrivateKey != "" {
client, err = sshpkg.NewClientViaJump(vps.Host, vps.Port, vps.Username, vps.PrivateKey,
machine.Username, vps.SSHPrivateKey, machine.TunnelPort)
} else {
client, err = sshpkg.NewClient(machine.Host, machine.Port, machine.Username, machine.PrivateKey)
}
if err != nil {
return map[string]interface{}{
"id":        id,
"connected": false,
"error":     err.Error(),
}, nil
}
defer client.Close()

output, err := client.Execute("systemctl is-active rathole-client 2>&1 || echo 'not installed'")
status := "unknown"
if err == nil {
status = output
}

return map[string]interface{}{
"id":             id,
"connected":      true,
"rathole_status": status,
}, nil
}
