package service

import (
	"time"

	"github.com/smalex-z/gopher/internal/api/dto"
	"github.com/smalex-z/gopher/internal/db"
	sshpkg "github.com/smalex-z/gopher/internal/ssh"
)

type MachineService struct {
	deploy *DeployService
	local  localOps
}

func NewMachineService(deploy *DeployService, local localOps) *MachineService {
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
		ID:         shortToken(),
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
	tunnels, err := db.GetTunnelsByMachine(id)
	if err != nil {
		return err
	}

	// Delete each machine tunnel with the same sequence as individual tunnel delete:
	// 1) remove client config over SSH (best-effort), 2) remove DB tunnel,
	// 3) reconcile server config, 4) remove managed Caddy route.
	for i := range tunnels {
		tunnel := &tunnels[i]
		if s.local != nil {
			_ = s.local.RemoveServiceTunnelClient(tunnel, machine)
		}
		if err := db.DeleteTunnel(tunnel.ID); err != nil {
			return err
		}
		if s.local != nil {
			if err := s.local.ReconcileServerConfig(); err != nil {
				return err
			}
			if err := s.local.RemoveServiceTunnelCaddy(tunnel); err != nil {
				return err
			}
		}
	}

	// Best-effort: SSH into the client machine and remove all gopher configs.
	if s.local != nil {
		_ = s.local.RemoveMachineClient(machine)
	}

	if err := db.DeleteMachine(id); err != nil {
		return err
	}
	// Reconcile server.toml now that machine + tunnels are gone from DB.
	if s.local != nil {
		if err := s.local.ReconcileServerConfig(); err != nil {
			return err
		}
	}
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
