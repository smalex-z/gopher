package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/config"
	"github.com/smalex-z/gopher/internal/db"
	apperrors "github.com/smalex-z/gopher/internal/errors"
	"github.com/smalex-z/gopher/internal/service"
)

type ExternalAPIHandler struct {
	bootstrapSvc *service.BootstrapService
	tunnelSvc    *service.TunnelService
	machineSvc   *service.MachineService
	localSvc     *service.LocalSetupService
}

func NewExternalAPIHandler(
	bootstrapSvc *service.BootstrapService,
	tunnelSvc *service.TunnelService,
	machineSvc *service.MachineService,
	localSvc *service.LocalSetupService,
) *ExternalAPIHandler {
	return &ExternalAPIHandler{
		bootstrapSvc: bootstrapSvc,
		tunnelSvc:    tunnelSvc,
		machineSvc:   machineSvc,
		localSvc:     localSvc,
	}
}

// ─── Machine responses ────────────────────────────────────────────────────────

type externalMachineResponse struct {
	ID           string    `json:"id"`
	Status       string    `json:"status"` // pending | connected | failed
	BootstrapURL string    `json:"bootstrap_url,omitempty"`
	PublicSSH    bool      `json:"public_ssh"`
	Error        string    `json:"error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// externalMachineToResponse derives live connectivity status from the underlying
// Machine record so the caller doesn't need to know about two separate tables.
func externalMachineToResponse(em *db.ExternalMachine) externalMachineResponse {
	status := "pending"
	errMsg := em.ErrorMsg

	if em.MachineID != nil {
		if m, err := db.GetMachine(*em.MachineID); err == nil {
			switch m.Status {
			case "connected", "active":
				status = "connected"
			case "failed":
				status = "failed"
				if errMsg == "" {
					errMsg = "machine failed to become reachable"
				}
			default:
				status = "pending"
			}
		}
	} else {
		// No machine yet — check if the bootstrap token expired unused.
		if bt, err := db.GetBootstrapTokenByID(em.TokenID); err == nil {
			if bt.UsedAt == nil && time.Now().After(bt.ExpiresAt) {
				status = "failed"
				errMsg = "bootstrap token expired before the machine registered"
			}
		}
	}

	return externalMachineResponse{
		ID:        em.ID,
		Status:    status,
		PublicSSH: em.PublicSSH,
		Error:     errMsg,
		CreatedAt: em.CreatedAt,
	}
}

// POST /api/v1/machines
// Generates a bootstrap token and returns a one-time bootstrap_url.
// Body (all optional): { "public_ssh": false, "ssh_key_id": "" }
func (h *ExternalAPIHandler) CreateMachine(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PublicSSH bool   `json:"public_ssh"`
		SSHKeyID  string `json:"ssh_key_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	// Resolve the SSH key: caller-specified → server default.
	sshKeyID := req.SSHKeyID
	if sshKeyID == "" {
		key, err := db.GetDefaultSSHKey()
		if err != nil {
			response.BadRequest(w, "no SSH key configured; add a key in Settings before using the external API")
			return
		}
		sshKeyID = key.ID
	} else {
		if _, err := db.GetSSHKey(sshKeyID); err != nil {
			response.BadRequest(w, fmt.Sprintf("ssh_key_id %q not found", sshKeyID))
			return
		}
	}

	bt, err := h.bootstrapSvc.GenerateToken(0, sshKeyID, req.PublicSSH)
	if err != nil {
		response.InternalError(w, fmt.Sprintf("failed to generate bootstrap token: %v", err))
		return
	}

	em := &db.ExternalMachine{
		ID:        randHex(),
		TokenID:   bt.ID,
		PublicSSH: req.PublicSSH,
		SSHKeyID:  sshKeyID,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := db.CreateExternalMachine(em); err != nil {
		response.InternalError(w, fmt.Sprintf("failed to save machine record: %v", err))
		return
	}

	base := hostURL(r)
	resp := externalMachineToResponse(em)
	resp.BootstrapURL = fmt.Sprintf("%s/bootstrap/%s", base, bt.Token)
	response.Created(w, resp)
}

// GET /api/v1/machines
func (h *ExternalAPIHandler) ListMachines(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)
	if limit > 200 {
		limit = 200
	}
	if limit < 1 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	machines, total, err := db.GetExternalMachines(limit, offset)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	items := make([]externalMachineResponse, len(machines))
	for i := range machines {
		items[i] = externalMachineToResponse(&machines[i])
	}
	response.Success(w, map[string]interface{}{
		"items":  items,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// GET /api/v1/machines/:id
func (h *ExternalAPIHandler) GetMachine(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	em, err := db.GetExternalMachine(id)
	if err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "machine not found")
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, externalMachineToResponse(em))
}

// DELETE /api/v1/machines/:id
// Full teardown: uninstalls rathole-client on the VM (best-effort SSH), removes
// all associated tunnels, reconciles rathole server config, and deletes all records.
func (h *ExternalAPIHandler) DeleteMachine(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	em, err := db.GetExternalMachine(id)
	if err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "machine not found")
			return
		}
		response.InternalError(w, err.Error())
		return
	}

	if em.MachineID != nil {
		if delErr := h.machineSvc.Delete(*em.MachineID); delErr != nil {
			if _, ok := delErr.(*apperrors.NotFoundError); !ok {
				response.InternalError(w, fmt.Sprintf("failed to delete machine: %v", delErr))
				return
			}
		}
		// Also clean up any ExternalTunnel records for this machine.
		_ = db.DeleteExternalTunnelsByMachineID(*em.MachineID)
	}

	if err := db.DeleteExternalMachine(id); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.NoContent(w)
}

// ─── Tunnel responses ─────────────────────────────────────────────────────────

type externalTunnelResponse struct {
	ID         string    `json:"id"`
	MachineID  string    `json:"machine_id"`
	Status     string    `json:"status"`
	Subdomain  string    `json:"subdomain"`
	TargetIP   string    `json:"target_ip"`
	TargetPort int       `json:"target_port"`
	TunnelURL  string    `json:"tunnel_url,omitempty"`
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

func externalTunnelToResponse(et *db.ExternalTunnel) externalTunnelResponse {
	return externalTunnelResponse{
		ID:         et.ID,
		MachineID:  et.MachineID,
		Status:     et.Status,
		Subdomain:  et.Subdomain,
		TargetIP:   et.TargetIP,
		TargetPort: et.TargetPort,
		TunnelURL:  et.TunnelURL,
		Error:      et.ErrorMsg,
		CreatedAt:  et.CreatedAt,
	}
}

// POST /api/v1/tunnels
// Creates a service tunnel on an already-connected machine. Synchronous.
// Body: { "machine_id": "...", "target_port": 3000, "subdomain": "optional", "target_ip": "127.0.0.1",
//         "private": false, "no_tls": false }
func (h *ExternalAPIHandler) CreateTunnel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MachineID  string `json:"machine_id"`
		Subdomain  string `json:"subdomain"`
		TargetIP   string `json:"target_ip"`
		TargetPort int    `json:"target_port"`
		Private    bool   `json:"private"`
		NoTLS      bool   `json:"no_tls"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	if req.MachineID == "" || req.TargetPort == 0 {
		response.BadRequest(w, "machine_id and target_port are required")
		return
	}

	// Verify the machine exists and is connected.
	machine, err := db.GetMachine(req.MachineID)
	if err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "machine not found")
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	if machine.Status != "connected" && machine.Status != "active" {
		response.BadRequest(w, fmt.Sprintf("machine is not connected (status: %s); wait until status is 'connected' before creating tunnels", machine.Status))
		return
	}

	// Resolve target IP.
	targetIP := req.TargetIP
	if targetIP == "" {
		targetIP = "127.0.0.1"
	}

	// Resolve or auto-generate subdomain.
	subdomain := strings.ToLower(strings.TrimSpace(req.Subdomain))
	if subdomain == "" {
		subdomain = autoSubdomain(machine.Name)
	}
	if err := config.ValidateSubdomain(subdomain); err != nil {
		response.BadRequest(w, fmt.Sprintf("invalid subdomain: %v", err))
		return
	}

	settings, err := db.GetSettings()
	if err != nil {
		response.InternalError(w, "failed to load settings")
		return
	}
	if settings.Domain == "" {
		response.BadRequest(w, "Gopher domain is not configured; cannot create subdomain tunnels")
		return
	}

	if taken, err := db.CheckSubdomainExists(subdomain); err != nil {
		response.InternalError(w, "failed to check subdomain")
		return
	} else if taken {
		response.Conflict(w, fmt.Sprintf("subdomain %q is already in use", subdomain))
		return
	}

	ratholePort, err := db.NextRatholePort()
	if err != nil {
		response.InternalError(w, fmt.Sprintf("failed to allocate rathole port: %v", err))
		return
	}

	tunnel := &db.Tunnel{
		ID:           randHex(),
		MachineID:    machine.ID,
		Name:         subdomain,
		Subdomain:    subdomain,
		LocalPort:    req.TargetPort,
		RatholePort:  ratholePort,
		RatholeToken: randHex(),
		Protocol:     "tcp",
		Transport:    "tcp",
		Private:      req.Private,
		NoTLS:        req.NoTLS,
		Status:       "inactive",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := db.CreateTunnel(tunnel); err != nil {
		response.InternalError(w, fmt.Sprintf("failed to create tunnel record: %v", err))
		return
	}

	service.ApplyTunnelPort(tunnel.RatholePort, tunnel.Transport, tunnel.Private)

	if err := h.localSvc.AddServiceTunnel(tunnel, machine); err != nil {
		tunnel.Status = fmt.Sprintf("config-error: %v", err)
		_ = db.UpdateTunnel(tunnel)
		et := &db.ExternalTunnel{
			ID:         randHex(),
			MachineID:  machine.ID,
			TunnelID:   tunnel.ID,
			Subdomain:  subdomain,
			TargetIP:   targetIP,
			TargetPort: req.TargetPort,
			Status:     "failed",
			ErrorMsg:   fmt.Sprintf("config push failed: %v", err),
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}
		_ = db.CreateExternalTunnel(et)
		response.InternalError(w, fmt.Sprintf("tunnel config push failed: %v", err))
		return
	}

	tunnelURL := fmt.Sprintf("https://%s.%s", subdomain, settings.Domain)
	if req.NoTLS {
		tunnelURL = fmt.Sprintf("http://%s.%s", subdomain, settings.Domain)
	}

	et := &db.ExternalTunnel{
		ID:         randHex(),
		MachineID:  machine.ID,
		TunnelID:   tunnel.ID,
		Subdomain:  subdomain,
		TargetIP:   targetIP,
		TargetPort: req.TargetPort,
		Status:     "active",
		TunnelURL:  tunnelURL,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := db.CreateExternalTunnel(et); err != nil {
		response.InternalError(w, fmt.Sprintf("failed to save tunnel record: %v", err))
		return
	}

	response.Created(w, externalTunnelToResponse(et))
}

// GET /api/v1/tunnels
func (h *ExternalAPIHandler) ListTunnels(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)
	if limit > 200 {
		limit = 200
	}
	if limit < 1 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	tunnels, total, err := db.GetExternalTunnels(limit, offset)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	items := make([]externalTunnelResponse, len(tunnels))
	for i := range tunnels {
		items[i] = externalTunnelToResponse(&tunnels[i])
	}
	response.Success(w, map[string]interface{}{
		"items":  items,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// GET /api/v1/tunnels/:id
func (h *ExternalAPIHandler) GetTunnel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	et, err := db.GetExternalTunnel(id)
	if err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "tunnel not found")
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, externalTunnelToResponse(et))
}

// DELETE /api/v1/tunnels/:id
// Removes the service tunnel only — the machine remains.
func (h *ExternalAPIHandler) DeleteTunnel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	et, err := db.GetExternalTunnel(id)
	if err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "tunnel not found")
			return
		}
		response.InternalError(w, err.Error())
		return
	}

	if et.TunnelID != "" {
		if delErr := h.tunnelSvc.Delete(et.TunnelID); delErr != nil {
			if _, ok := delErr.(*apperrors.NotFoundError); !ok {
				response.InternalError(w, fmt.Sprintf("failed to delete tunnel: %v", delErr))
				return
			}
		}
	}

	if err := db.DeleteExternalTunnel(id); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.NoContent(w)
}

// POST /api/v1/ssh-keys
// Uploads an Ed25519 (or RSA) keypair so the caller can reference it by ID in
// POST /api/v1/machines. Both private_key and public_key are required — Gopher
// uses the private key to SSH back into the VM through the rathole tunnel.
// Body: { "name": "...", "private_key": "...", "public_key": "..." }
func (h *ExternalAPIHandler) UploadSSHKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"name"`
		PrivateKey string `json:"private_key"`
		PublicKey  string `json:"public_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	if req.PrivateKey == "" || req.PublicKey == "" {
		response.BadRequest(w, "private_key and public_key are required")
		return
	}
	if req.Name == "" {
		req.Name = "Uploaded key"
	}
	key, err := h.localSvc.AddSSHKey(req.Name, req.PrivateKey, req.PublicKey, false)
	if err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	response.Created(w, map[string]string{"id": key.ID, "name": key.Name})
}

// GET /api/v1/tunnels/check?subdomain=foo
// Returns {"available": true} if the subdomain is free, false if taken.
// Use this before POST /api/v1/machines to avoid a wasted bootstrap.
func (h *ExternalAPIHandler) CheckSubdomain(w http.ResponseWriter, r *http.Request) {
	subdomain := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("subdomain")))
	if subdomain == "" {
		response.BadRequest(w, "subdomain query parameter is required")
		return
	}
	taken, err := db.CheckSubdomainExists(subdomain)
	if err != nil {
		response.InternalError(w, "failed to check subdomain")
		return
	}
	response.Success(w, map[string]bool{"available": !taken})
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func randHex() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func queryInt(r *http.Request, key string, defaultVal int) int {
	s := r.URL.Query().Get(key)
	if s == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}
	return n
}

// autoSubdomain derives a URL-safe subdomain from the machine name + random suffix.
func autoSubdomain(machineName string) string {
	slug := strings.ToLower(machineName)
	var b strings.Builder
	for _, c := range slug {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			b.WriteRune(c)
		} else if c == ' ' || c == '_' {
			b.WriteRune('-')
		}
	}
	base := strings.Trim(b.String(), "-")
	if base == "" {
		base = "app"
	}
	if len(base) > 40 {
		base = base[:40]
	}
	suffix := make([]byte, 3)
	_, _ = rand.Read(suffix)
	return fmt.Sprintf("%s-%s", base, hex.EncodeToString(suffix))
}
