package service

import (
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

go s.deploy.DeployClient(machine)
return nil
}

func (s *MachineService) Status(id string) (map[string]interface{}, error) {
machine, err := db.GetMachine(id)
if err != nil {
return nil, err
}

settings, _ := db.GetSettings()

var client *sshpkg.SSHClient
if settings != nil && machine.TunnelPort > 0 && settings.SSHPrivateKey != "" {
client, err = sshpkg.NewClient("localhost", machine.TunnelPort, machine.Username, settings.SSHPrivateKey)
} else if machine.Host != "" {
client, err = sshpkg.NewClient(machine.Host, machine.Port, machine.Username, machine.PrivateKey)
} else {
return map[string]interface{}{"id": id, "connected": false, "error": "no ssh access method"}, nil
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
