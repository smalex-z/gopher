package service

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/google/uuid"
	"github.com/smalex-z/gopher/internal/api/dto"
	"github.com/smalex-z/gopher/internal/db"
	sshpkg "github.com/smalex-z/gopher/internal/ssh"
)

type MachineService struct {
	deploy *DeployService
	local  *LocalSetupService
}

func NewMachineService(deploy *DeployService, local *LocalSetupService) *MachineService {
	return &MachineService{deploy: deploy, local: local}
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
	machine, err := db.GetMachine(id)
	if err != nil {
		return err
	}

	// Best-effort: SSH into the client machine and remove all gopher configs.
	_ = s.local.RemoveMachineClient(machine)

	// Remove Caddy blocks for every tunnel that belongs to this machine.
	if tunnels, _ := db.GetTunnelsByMachine(id); len(tunnels) > 0 {
		if settings, _ := db.GetSettings(); settings != nil && settings.Domain != "" {
			const caddyFile = "/etc/caddy/Caddyfile"
			if cc, readErr := os.ReadFile(caddyFile); readErr == nil {
				content := string(cc)
				changed := false
				for _, t := range tunnels {
					if t.Subdomain != "" {
						updated := removeCaddyBlock(content, fmt.Sprintf("%s.%s", t.Subdomain, settings.Domain))
						if updated != content {
							content = updated
							changed = true
						}
					}
				}
				if changed {
					_ = writeLocalFile(caddyFile, content)
					_ = exec.Command("sudo", "systemctl", "reload", "caddy").Run() // #nosec G204
				}
			}
		}
		for _, t := range tunnels {
			_ = db.DeleteTunnel(t.ID)
		}
	}

	if err := db.DeleteMachine(id); err != nil {
		return err
	}
	// Reconcile server.toml now that machine + tunnels are gone from DB.
	_ = s.local.ReconcileServerConfig()
	return nil
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
