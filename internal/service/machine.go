package service

import (
	"fmt"
	"log"
	"strings"
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

// DeleteResult reports the outcome of a machine delete to the API caller.
// Server-side cleanup always runs (machine row, tunnel rows, Caddy, rathole
// reconcile) and any failure there returns an error from Delete. The
// client-cleanup step is best-effort and its result is reported here so the
// dashboard can show a warning toast when the box wasn't actually torn down
// (e.g. tunnel was already dead, agent unreachable, no SSH key on file).
type DeleteResult struct {
	ID                string `json:"id"`
	ClientCleanupOK   bool   `json:"client_cleanup_ok"`
	ClientCleanupPath string `json:"client_cleanup_path,omitempty"` // "agent" | "ssh" | "skipped"
	ClientCleanupErr  string `json:"client_cleanup_error,omitempty"`
}

func (s *MachineService) Delete(id string) (*DeleteResult, error) {
	return s.delete(id, false)
}

// DeleteFromClient is the self-delete entry point: skips the
// remote-uninstall step because the client is already tearing itself
// down (this call comes FROM that teardown). Server-side cleanup —
// tunnels, Caddy, rathole config, machine record — runs as usual.
func (s *MachineService) DeleteFromClient(id string) (*DeleteResult, error) {
	return s.delete(id, true)
}

func (s *MachineService) delete(id string, fromClient bool) (*DeleteResult, error) {
	machine, err := db.GetMachine(id)
	if err != nil {
		return nil, err
	}
	tunnels, err := db.GetTunnelsByMachine(id)
	if err != nil {
		return nil, err
	}

	result := &DeleteResult{ID: id, ClientCleanupOK: true, ClientCleanupPath: "skipped"}

	// Server-driven delete: SSH/agent into the client first — while the
	// rathole back-channel is fully active — and trigger gopher-uninstall.
	// Doing this before ReconcileServerConfig avoids a race where rathole
	// restarts mid-delete and the SSH tunnel briefly disappears.
	//
	// Self-delete: the client is the caller, gopher-uninstall is already
	// running there. Skip the remote step to avoid the duplicate trigger.
	// Non-bootstrapped machines (created directly via /api/machines/ or the
	// external API with no tunnel) have no client-side install to clean —
	// skip the remote teardown entirely instead of reporting it as a
	// failure. ClientCleanupOK stays true and the handler returns 204.
	if s.local != nil && !fromClient && machine.TunnelPort > 0 {
		if machine.AgentInstalled && machine.AgentRemotePort > 0 {
			result.ClientCleanupPath = "agent"
		} else {
			result.ClientCleanupPath = "ssh"
		}
		if err := s.local.RemoveMachineClient(machine); err != nil {
			log.Printf("client cleanup for %s (%s) failed via %s: %v", machine.ID, machine.Name, result.ClientCleanupPath, err)
			result.ClientCleanupOK = false
			result.ClientCleanupErr = err.Error()
		}
	}

	// Delete each tunnel from DB, then do a single reconcile + Caddy cleanup.
	for i := range tunnels {
		tunnel := &tunnels[i]
		if err := db.DeleteTunnel(tunnel.ID); err != nil {
			return nil, err
		}
		if s.local != nil {
			if err := s.local.RemoveServiceTunnelCaddy(tunnel); err != nil {
				return nil, err
			}
		}
	}

	db.LogEvent("machine_deleted", id, machine.Name)
	if err := db.DeleteMachine(id); err != nil {
		return nil, err
	}

	// Single reconcile after all DB deletions — avoids multiple rathole restarts.
	if s.local != nil {
		if err := s.local.ReconcileServerConfig(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *MachineService) Deploy(id string) error {
	machine, err := db.GetMachine(id)
	if err != nil {
		return err
	}

	go s.deploy.DeployClient(machine) //nolint:errcheck
	return nil
}

// CanonicalRatholeConfig returns the client.toml that this machine should
// currently be running, derived from the DB and the current server-side noise
// pubkey. Provides a manual recovery handle for the case where automated
// config push can't land (machine unreachable, disk full, agent broken).
// Equivalent to what mergeClientManagedConfig would produce against an empty
// existing file — the canonical "fresh paste" version.
func (s *MachineService) CanonicalRatholeConfig(id string) (string, error) {
	machine, err := db.GetMachine(id)
	if err != nil {
		return "", err
	}
	settings, err := db.GetSettings()
	if err != nil {
		return "", fmt.Errorf("load settings: %w", err)
	}
	tunnels, err := db.GetTunnelsByMachine(machine.ID)
	if err != nil {
		return "", fmt.Errorf("load tunnels for machine: %w", err)
	}
	cfg, err := mergeClientManagedConfig("", machine, tunnels, ratholeHostFromSettings(settings), settings.RatholeNoisePubKey)
	if err != nil {
		return "", err
	}
	return cfg, nil
}

func (s *MachineService) Status(id string) (map[string]interface{}, error) {
	machine, err := db.GetMachine(id)
	if err != nil {
		return nil, err
	}

	var client *sshpkg.SSHClient
	if machine.TunnelPort > 0 {
		if sshKey, kerr := db.GetSSHKeyForMachine(machine); kerr == nil {
			client, err = sshpkg.NewClient(TunnelDialHost(machine), machine.TunnelPort, machine.Username, sshKey.PrivateKey)
		}
	}
	if client == nil {
		if machine.Host != "" {
			client, err = sshpkg.NewClient(machine.Host, machine.Port, machine.Username, machine.PrivateKey)
		} else {
			return map[string]interface{}{"id": id, "connected": false, "error": "no ssh access method"}, nil
		}
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

// RefreshNetworkInfo SSHes into the machine, discovers its WAN IP and LAN IP,
// stores the public IP on the machine record, and returns the info.
func (s *MachineService) RefreshNetworkInfo(id string) (map[string]interface{}, error) {
	machine, err := db.GetMachine(id)
	if err != nil {
		return nil, err
	}

	var client *sshpkg.SSHClient
	var sshKeyErr error
	if machine.TunnelPort > 0 {
		key, kerr := db.GetSSHKeyForMachine(machine)
		if kerr != nil {
			sshKeyErr = kerr
		} else {
			client, _ = sshpkg.NewClient(TunnelDialHost(machine), machine.TunnelPort, machine.Username, key.PrivateKey)
		}
	}
	if client == nil && machine.Host != "" {
		p := machine.Port
		if p == 0 {
			p = 22
		}
		client, err = sshpkg.NewClient(machine.Host, p, machine.Username, machine.PrivateKey)
		if err != nil {
			return map[string]interface{}{"id": id, "error": err.Error()}, nil
		}
	}
	if client == nil {
		// Surface the SSH-key lookup failure when it was the actual reason
		// the tunnel path didn't take. The previous "no ssh access method"
		// message was misleading — the machine had a tunnel, just no key.
		if sshKeyErr != nil {
			return map[string]interface{}{"id": id, "error": fmt.Sprintf("ssh key lookup failed: %v", sshKeyErr)}, nil
		}
		return map[string]interface{}{"id": id, "error": "no ssh access method"}, nil
	}
	defer client.Close()

	// WAN (public) IP — try opendns first, fall back to ipify.
	wanOut, _ := client.Execute(
		`dig +short myip.opendns.com @resolver1.opendns.com 2>/dev/null | head -1 || curl -sf --max-time 5 https://api.ipify.org 2>/dev/null`,
	)
	publicIP := strings.TrimSpace(wanOut)

	// LAN (private) IP from the machine's own NIC.
	lanOut, _ := client.Execute(`hostname -I 2>/dev/null | awk '{print $1}'`)
	privateIP := strings.TrimSpace(lanOut)
	if privateIP == "" {
		privateIP = machine.Host
	}

	// Persist so subsequent loads don't need an SSH round-trip.
	if publicIP != "" && publicIP != machine.PublicIP {
		machine.PublicIP = publicIP
		machine.UpdatedAt = time.Now()
		_ = db.UpdateMachine(machine)
	}

	isNAT := publicIP != "" && privateIP != "" && publicIP != privateIP

	return map[string]interface{}{
		"id":         id,
		"public_ip":  publicIP,
		"private_ip": privateIP,
		"is_nat":     isNAT,
	}, nil
}

// ReassignSSHKey installs newKeyID's public key on the machine (via its current
// key) and updates the machine record. The old key is left in authorized_keys so
// access is never lost if something goes wrong.
func (s *MachineService) ReassignSSHKey(machineID, newKeyID string) error {
	machine, err := db.GetMachine(machineID)
	if err != nil {
		return err
	}
	newKey, err := db.GetSSHKey(newKeyID)
	if err != nil {
		return err
	}

	// Connect using the current key.
	currentKey, err := db.GetSSHKeyForMachine(machine)
	if err != nil {
		return fmt.Errorf("cannot determine current SSH key: %w", err)
	}
	if machine.TunnelPort == 0 {
		return fmt.Errorf("machine has no active tunnel — cannot push key")
	}
	client, err := sshpkg.NewClient(TunnelDialHost(machine), machine.TunnelPort, machine.Username, currentKey.PrivateKey)
	if err != nil {
		return fmt.Errorf("failed to connect to machine: %w", err)
	}
	defer client.Close()

	// Append the new public key (idempotent).
	appendCmd := fmt.Sprintf(
		`grep -qF %q ~/.ssh/authorized_keys 2>/dev/null || echo %q >> ~/.ssh/authorized_keys`,
		newKey.PublicKey, newKey.PublicKey,
	)
	if _, err := client.Execute(appendCmd); err != nil {
		return fmt.Errorf("failed to install new key on machine: %w", err)
	}

	machine.SSHKeyID = newKeyID
	machine.UpdatedAt = time.Now()
	return db.UpdateMachine(machine)
}
