package service

import (
	"fmt"
	"log"
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
	// Private tunnels have no public URL, so subdomain is meaningless
	if req.Private {
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

	tunnel := &db.Tunnel{
		ID:           shortToken(),
		MachineID:    req.MachineID,
		Name:         req.Name,
		Subdomain:    req.Subdomain,
		LocalPort:    req.LocalPort,
		RatholePort:  ratholePort,
		RatholeToken: shortToken(),
		Protocol:     "tcp",
		Transport:    transport,
		NoTLS:        req.NoTLS,
		Private:      req.Private,
		Status:       "inactive",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
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
	tunnel.Name = req.Name
	tunnel.LocalPort = req.LocalPort
	tunnel.Private = req.Private
	// Private tunnels cannot have a public subdomain URL
	if req.Private {
		tunnel.Subdomain = ""
	}
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

	return tunnel, nil
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
