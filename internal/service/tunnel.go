package service

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/api/dto"
	"github.com/smalex-z/gopher/internal/config"
	"github.com/smalex-z/gopher/internal/db"
	apperrors "github.com/smalex-z/gopher/internal/errors"
)

type TunnelService struct {
	local localOps
}

func NewTunnelService(local localOps) *TunnelService {
	return &TunnelService{local: local}
}

const (
	machineSSHTunnelPrefix = "machine-"
	machineSSHTunnelSuffix = "-ssh"
)

func machineSSHTunnelID(machineID string) string {
	return machineSSHTunnelPrefix + machineID + machineSSHTunnelSuffix
}

func parseMachineSSHTunnelID(id string) (string, bool) {
	if !strings.HasPrefix(id, machineSSHTunnelPrefix) || !strings.HasSuffix(id, machineSSHTunnelSuffix) {
		return "", false
	}
	machineID := strings.TrimSuffix(strings.TrimPrefix(id, machineSSHTunnelPrefix), machineSSHTunnelSuffix)
	if machineID == "" {
		return "", false
	}
	return machineID, true
}

func machineTunnelStatus(status string) string {
	if status == "connected" {
		return "active"
	}
	return status
}

func (s *TunnelService) List() ([]db.Tunnel, error) {
	tunnels, err := db.GetTunnels()
	if err != nil {
		return nil, err
	}
	machines, err := db.GetMachines()
	if err != nil {
		return nil, err
	}
	for _, machine := range machines {
		if machine.TunnelPort == 0 {
			continue
		}
		tunnels = append(tunnels, db.Tunnel{
			ID:          machineSSHTunnelID(machine.ID),
			MachineID:   machine.ID,
			Name:        machine.Name + " SSH",
			Subdomain:   "",
			LocalPort:   22,
			RatholePort: machine.TunnelPort,
			Protocol:    "tcp",
			Private:     !machine.PublicSSH,
			Status:      machineTunnelStatus(machine.Status),
			Managed:     true,
			Kind:        "machine-ssh",
			CreatedAt:   machine.CreatedAt,
			UpdatedAt:   machine.UpdatedAt,
		})
	}
	return tunnels, nil
}

func (s *TunnelService) ListByMachine(machineID string) ([]db.Tunnel, error) {
	return db.GetTunnelsByMachine(machineID)
}

func (s *TunnelService) Get(id string) (*db.Tunnel, error) {
	return db.GetTunnel(id)
}

// Probe runs a live connectivity check on the tunnel and returns one of
// "active", "idle", or "offline". It uses the same logic as the background
// monitor so the result is consistent with what the dashboard shows.
func (s *TunnelService) Probe(t *db.Tunnel) string {
	return probeTunnel(*t)
}

func (s *TunnelService) NextPort() (int, error) {
	return db.NextRatholePort()
}

func (s *TunnelService) Create(req dto.CreateTunnelRequest) (*db.Tunnel, error) {
	settings, err := db.GetSettings()
	if err != nil {
		return nil, err
	}
	if req.LocalPort == 22 {
		return nil, &apperrors.ValidationError{Field: "local_port", Message: "port 22 is reserved for machine SSH tunnels"}
	}
	transport := req.Transport
	if transport != "udp" {
		transport = "tcp"
	}
	// UDP tunnels cannot have HTTP subdomain routing
	if transport == "udp" {
		req.Subdomain = ""
		req.NoTLS = false
	}
	if req.Subdomain != "" && settings.Domain == "" {
		return nil, &apperrors.ValidationError{Field: "subdomain", Message: "URL routing is disabled; leave subdomain empty"}
	}

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
	if req.RatholePort != 0 {
		if err := config.ValidatePort(req.RatholePort); err != nil {
			return nil, &apperrors.ValidationError{Field: "rathole_port", Message: err.Error()}
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

	// Bot protection requires a subdomain (needs Host-header routing through proxy).
	botProtection := req.BotProtectionEnabled && req.Subdomain != "" && transport != "udp"

	tunnel := &db.Tunnel{
		ID:                   shortToken(),
		MachineID:            req.MachineID,
		Name:                 req.Name,
		Subdomain:            req.Subdomain,
		LocalPort:            req.LocalPort,
		RatholePort:          ratholePort,
		RatholeToken:         shortToken(),
		Protocol:             "tcp",
		Transport:            transport,
		NoTLS:                req.NoTLS,
		Private:              req.Private,
		BotProtectionEnabled: botProtection,
		BotProtectionTTL:     req.BotProtectionTTL,
		BotProtectionAllowIP: req.BotProtectionAllowIP,
		Status:               "inactive",
		CreatedAt:            time.Now(),
		UpdatedAt:            time.Now(),
	}

	if err := db.CreateTunnel(tunnel); err != nil {
		return nil, err
	}

	// Open firewall port if Gopher manages the firewall (non-fatal).
	ApplyTunnelPort(tunnel.RatholePort, tunnel.Transport, tunnel.Private)

	// Push configs to server + client (non-fatal: tunnel is saved even if this fails)
	machine, machErr := db.GetMachine(req.MachineID)
	if machErr == nil && s.local != nil {
		log.Printf("tunnel create: pushing config for tunnel %s to machine %s (port %d)", tunnel.ID, machine.ID, machine.TunnelPort)
		if cfgErr := s.local.AddServiceTunnel(tunnel, machine); cfgErr != nil {
			log.Printf("tunnel create: config push failed for tunnel %s: %v", tunnel.ID, cfgErr)
			// Annotate the tunnel with the error but don't fail the creation
			tunnel.Status = fmt.Sprintf("config-error: %v", cfgErr)
			_ = db.UpdateTunnel(tunnel)
		} else {
			log.Printf("tunnel create: config push succeeded for tunnel %s", tunnel.ID)
		}
	} else if machErr != nil {
		log.Printf("tunnel create: could not load machine %s: %v — skipping config push", req.MachineID, machErr)
	}

	return tunnel, nil
}

func (s *TunnelService) Update(id string, req dto.UpdateTunnelRequest) (*db.Tunnel, error) {
	// Machine SSH tunnels are virtual (not in the tunnels table).
	// Only the Private field can change — it maps to Machine.PublicSSH.
	if machineID, ok := parseMachineSSHTunnelID(id); ok {
		return s.updateMachineSSHPrivacy(machineID, req.Private)
	}

	tunnel, err := db.GetTunnel(id)
	if err != nil {
		return nil, err
	}
	if req.LocalPort == 22 {
		return nil, &apperrors.ValidationError{Field: "local_port", Message: "port 22 is reserved for machine SSH tunnels"}
	}
	settings, err := db.GetSettings()
	if err != nil {
		return nil, err
	}

	if req.Subdomain != tunnel.Subdomain {
		if req.Subdomain != "" && settings.Domain == "" {
			return nil, &apperrors.ValidationError{Field: "subdomain", Message: "URL routing is disabled; leave subdomain empty"}
		}
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

	oldPrivate := tunnel.Private
	oldBotProtection := tunnel.BotProtectionEnabled
	tunnel.Name = req.Name
	tunnel.LocalPort = req.LocalPort
	tunnel.Private = req.Private
	// Private tunnels cannot have a public subdomain URL
	if req.Private {
		tunnel.Subdomain = ""
	}
	// Bot protection requires a subdomain and TCP transport.
	tunnel.BotProtectionEnabled = req.BotProtectionEnabled && tunnel.Subdomain != "" && tunnel.Transport != "udp"
	tunnel.BotProtectionTTL = req.BotProtectionTTL
	tunnel.BotProtectionAllowIP = req.BotProtectionAllowIP
	tunnel.UpdatedAt = time.Now()

	if err := db.UpdateTunnel(tunnel); err != nil {
		return nil, err
	}

	// If privacy setting changed, update rathole bind_addr and firewall.
	if oldPrivate != req.Private && s.local != nil {
		log.Printf("tunnel update: privacy changed for %s (private=%v), reconciling server config", tunnel.ID, req.Private)
		if err := s.local.ReconcileServerConfig(); err != nil {
			log.Printf("tunnel update: reconcile failed: %v", err)
		}
		ApplyTunnelPort(tunnel.RatholePort, tunnel.Transport, tunnel.Private)
	}

	// If bot protection toggled, rewrite the Caddy block so the upstream
	// switches between rathole port (unprotected) and proxy port (protected).
	if oldBotProtection != tunnel.BotProtectionEnabled && tunnel.Subdomain != "" && s.local != nil {
		if svcSettings, svcErr := db.GetSettings(); svcErr == nil && svcSettings.Domain != "" {
			managedPath := managedTunnelCaddyPath(tunnel.ID)
			block := buildTunnelCaddyBlock(tunnel.Subdomain, svcSettings.Domain, tunnel.RatholePort, tunnel.NoTLS, tunnel.BotProtectionEnabled)
			if writeErr := writeLocalFile(managedPath, block); writeErr != nil {
				log.Printf("tunnel update: failed to rewrite Caddy block for %s: %v", tunnel.ID, writeErr)
			} else {
				_ = exec.Command("sudo", "systemctl", "reload", "caddy").Run() // #nosec G204
			}
		}
	}

	return tunnel, nil
}

// updateMachineSSHPrivacy toggles the SSH tunnel visibility for a bootstrapped machine.
// Private=true → bind 127.0.0.1 (jumpbox only); Private=false → bind 0.0.0.0 (public).
func (s *TunnelService) updateMachineSSHPrivacy(machineID string, private bool) (*db.Tunnel, error) {
	machine, err := db.GetMachine(machineID)
	if err != nil {
		return nil, err
	}
	oldPublicSSH := machine.PublicSSH
	machine.PublicSSH = !private
	machine.UpdatedAt = time.Now()
	if err := db.UpdateMachine(machine); err != nil {
		return nil, err
	}
	if oldPublicSSH != machine.PublicSSH && s.local != nil {
		log.Printf("tunnel update: machine %s SSH visibility changed (public=%v), reconciling", machineID, machine.PublicSSH)
		if err := s.local.ReconcileServerConfig(); err != nil {
			log.Printf("tunnel update: reconcile failed: %v", err)
		}
		if machine.PublicSSH {
			ApplyTunnelPort(machine.TunnelPort, "tcp", false)
		} else {
			ApplyTunnelPort(machine.TunnelPort, "tcp", true)
		}
	}
	// Return a synthetic tunnel matching what List() would emit.
	return &db.Tunnel{
		ID:          machineSSHTunnelID(machineID),
		MachineID:   machineID,
		Name:        machine.Name + " SSH",
		LocalPort:   22,
		RatholePort: machine.TunnelPort,
		Protocol:    "tcp",
		Private:     !machine.PublicSSH,
		Status:      machineTunnelStatus(machine.Status),
		Managed:     true,
		Kind:        "machine-ssh",
		CreatedAt:   machine.CreatedAt,
		UpdatedAt:   machine.UpdatedAt,
	}, nil
}

func (s *TunnelService) Delete(id string) error {
	if _, isMachineSSHTunnel := parseMachineSSHTunnelID(id); isMachineSSHTunnel {
		return &apperrors.ValidationError{Field: "id", Message: "cannot delete machine SSH tunnel directly; delete the machine instead"}
	}

	tunnel, err := db.GetTunnel(id)
	if err != nil {
		return err
	}
	if tunnel.LocalPort == 22 {
		return &apperrors.ValidationError{Field: "local_port", Message: "port 22 tunnel cannot be deleted directly; delete the machine instead"}
	}

	machine, machErr := db.GetMachine(tunnel.MachineID)
	if machErr == nil && s.local != nil {
		_ = s.local.RemoveServiceTunnelClient(tunnel, machine)
	}

	if err := db.DeleteTunnel(id); err != nil {
		return err
	}

	// Close firewall port if Gopher manages the firewall (non-fatal).
	RevokeTunnelPort(tunnel.RatholePort, tunnel.Transport)

	if s.local != nil {
		if err := s.local.ReconcileServerConfig(); err != nil {
			return err
		}
		if err := s.local.RemoveServiceTunnelCaddy(tunnel); err != nil {
			return err
		}
	}
	return nil
}
