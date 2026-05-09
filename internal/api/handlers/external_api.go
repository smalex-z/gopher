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
	"github.com/smalex-z/gopher/internal/api/dto"
	"github.com/smalex-z/gopher/internal/api/response"
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
	ID            string    `json:"id"`
	Status        string    `json:"status"` // pending | connected | failed
	BootstrapURL  string    `json:"bootstrap_url,omitempty"`
	PublicSSH     bool      `json:"public_ssh"`
	PublicSSHHost string    `json:"public_ssh_host,omitempty"` // populated once connected + PublicSSH=true
	PublicSSHPort int       `json:"public_ssh_port,omitempty"` // ditto
	Error         string    `json:"error,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// externalMachineToResponse derives live connectivity status from the underlying
// Machine record so the caller doesn't need to know about two separate tables.
// When the machine is connected with PublicSSH=true, the gateway-side
// SSH host:port is included so external callers can build a usable
// connection string without scraping settings.
func externalMachineToResponse(em *db.ExternalMachine) externalMachineResponse {
	status := "pending"
	errMsg := em.ErrorMsg
	var sshHost string
	var sshPort int

	if em.MachineID != nil {
		if m, err := db.GetMachine(*em.MachineID); err == nil {
			switch m.Status {
			case "connected", "active":
				status = "connected"
				if em.PublicSSH && m.TunnelPort > 0 {
					sshPort = m.TunnelPort
					if settings, sErr := db.GetSettings(); sErr == nil {
						switch {
						case settings.ServerHost != "":
							sshHost = settings.ServerHost
						case settings.Domain != "":
							sshHost = settings.Domain
						}
					}
				}
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
		ID:            em.ID,
		Status:        status,
		PublicSSH:     em.PublicSSH,
		PublicSSHHost: sshHost,
		PublicSSHPort: sshPort,
		Error:         errMsg,
		CreatedAt:     em.CreatedAt,
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
		// Discard the per-call DeleteResult — external API callers don't get
		// to see the client-cleanup-warnings field; the dashboard surfaces
		// that, the external delete just needs success/failure.
		if _, delErr := h.machineSvc.Delete(*em.MachineID); delErr != nil {
			if _, ok := delErr.(*apperrors.NotFoundError); !ok {
				response.InternalError(w, fmt.Sprintf("failed to delete machine: %v", delErr))
				return
			}
		}
		// Also clean up any ExternalTunnel records for this machine.
		// MachineID on ExternalTunnel rows holds the external (em.ID) value
		// — that's what callers see and filter by.
		_ = db.DeleteExternalTunnelsByMachineID(em.ID)
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
	Subdomain  string    `json:"subdomain,omitempty"`
	TargetIP   string    `json:"target_ip"`
	TargetPort int       `json:"target_port"`
	// Transport is the L4 protocol the tunnel forwards. "tcp" (default) for
	// HTTP/SSH/etc., "udp" for raw datagram services. UDP tunnels skip
	// Caddy + subdomain routing — they're surfaced on a fixed gateway port.
	Transport string `json:"transport"`
	// Private flips the gateway-side bind to 127.0.0.1 (VPS-local only).
	Private bool `json:"private"`
	// NoTLS asks Caddy to serve plain http:// rather than https://. Ignored
	// for UDP and port-only tunnels.
	NoTLS bool `json:"no_tls"`
	// ServerPort is the port Gopher allocated on the gateway. For subdomain
	// tunnels the user reaches the service via tunnel_url; for port-only
	// (no subdomain) and UDP tunnels they need <gateway>:<server_port>.
	ServerPort int `json:"server_port,omitempty"`
	// Alpha features — bot protection (PoW JS challenge gating HTTP traffic)
	// requires a subdomain and TCP. Acknowledged-and-coerced server-side, so
	// these reflect the actual stored state, not just what the caller asked.
	BotProtectionEnabled bool   `json:"bot_protection_enabled,omitempty"`
	BotProtectionTTL     int    `json:"bot_protection_ttl,omitempty"`
	BotProtectionAllowIP string `json:"bot_protection_allow_ip,omitempty"`
	TLSSkipVerify        bool   `json:"tls_skip_verify,omitempty"`
	TunnelURL            string `json:"tunnel_url,omitempty"`
	Error                string `json:"error,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
}

// externalTunnelToResponse derives the wire-shape from the canonical
// db.Tunnel (looked up via the ExternalTunnel pointer) so callers always see
// the actual stored state — including the server-side coercions tunnelSvc
// applies (e.g., bot protection silently disabled when no subdomain). When
// the underlying Tunnel is missing (race during deletion or a pre-refactor
// row), falls back to whatever the ExternalTunnel record carries.
func externalTunnelToResponse(et *db.ExternalTunnel) externalTunnelResponse {
	resp := externalTunnelResponse{
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
	if et.TunnelID != "" {
		if t, err := db.GetTunnel(et.TunnelID); err == nil {
			resp.Transport = t.Transport
			resp.Private = t.Private
			resp.NoTLS = t.NoTLS
			resp.ServerPort = t.RatholePort
			resp.BotProtectionEnabled = t.BotProtectionEnabled
			resp.BotProtectionTTL = t.BotProtectionTTL
			resp.BotProtectionAllowIP = t.BotProtectionAllowIP
			resp.TLSSkipVerify = t.TLSSkipVerify
		}
	}
	if resp.Transport == "" {
		resp.Transport = "tcp"
	}
	return resp
}

// POST /api/v1/tunnels
// Creates a service tunnel on an already-connected machine. Synchronous.
//
// Body fields:
//
//	machine_id   (required) — connected machine to attach the tunnel to
//	target_port  (required) — port the service listens on inside the VM
//	target_ip    (optional, default "127.0.0.1") — local IP on the VM
//	subdomain    (optional, default = auto from machine name) — public hostname
//	             prefix. Ignored for UDP tunnels (HTTP-only routing). Pass
//	             "" with transport=tcp to expose by port-only.
//	transport    (optional, "tcp"|"udp", default "tcp") — L4 protocol
//	private      (optional) — bind 127.0.0.1 on the VPS instead of 0.0.0.0
//	no_tls       (optional) — Caddy serves http:// instead of https://
//
// Alpha:
//
//	bot_protection_enabled (optional) — PoW JS-challenge page gating HTTP.
//	  Requires a subdomain + TCP. Server silently disables the flag if those
//	  conditions aren't met; the response reflects the stored value.
//	bot_protection_ttl     (optional) — challenge cookie TTL in seconds
//	  (0 = default 86400 / 24h).
//	bot_protection_allow_ip (optional) — JSON array of CIDR/IP strings
//	  whitelisted from the challenge.
//	tls_skip_verify        (optional) — Caddy ignores upstream TLS errors;
//	  required for backends with self-signed certs (e.g. Proxmox itself).
func (h *ExternalAPIHandler) CreateTunnel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MachineID            string `json:"machine_id"`
		Subdomain            string `json:"subdomain"`
		TargetIP             string `json:"target_ip"`
		TargetPort           int    `json:"target_port"`
		Transport            string `json:"transport"`
		Private              bool   `json:"private"`
		NoTLS                bool   `json:"no_tls"`
		BotProtectionEnabled bool   `json:"bot_protection_enabled"`
		BotProtectionTTL     int    `json:"bot_protection_ttl"`
		BotProtectionAllowIP string `json:"bot_protection_allow_ip"`
		TLSSkipVerify        bool   `json:"tls_skip_verify"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	if req.MachineID == "" || req.TargetPort == 0 {
		response.BadRequest(w, "machine_id and target_port are required")
		return
	}

	// External callers know machines by their ExternalMachine.ID (the value
	// returned by POST /api/v1/machines). Resolve that to the underlying
	// Machine record — the dashboard's Tunnel rows reference the inner
	// Machine.ID, not the external one. Falling through to db.GetMachine
	// directly was the old behavior and only happened to work when callers
	// happened to pass the inner ID; on a real ExternalMachine.ID it 404'd.
	em, err := db.GetExternalMachine(req.MachineID)
	if err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "machine not found")
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	if em.MachineID == nil {
		response.BadRequest(w, "machine has not yet bootstrapped — wait for status to flip to 'connected' before creating tunnels")
		return
	}
	machine, err := db.GetMachine(*em.MachineID)
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

	// Default + normalize. Subdomain auto-derives from the machine name when
	// blank and transport is TCP — the internal service won't auto-generate,
	// so we do it here to preserve the prior external-API behavior.
	transport := strings.ToLower(strings.TrimSpace(req.Transport))
	if transport == "" {
		transport = "tcp"
	}
	if transport != "tcp" && transport != "udp" {
		response.BadRequest(w, fmt.Sprintf("transport must be \"tcp\" or \"udp\" (got %q)", req.Transport))
		return
	}
	subdomain := strings.ToLower(strings.TrimSpace(req.Subdomain))
	if subdomain == "" && transport == "tcp" {
		subdomain = autoSubdomain(machine.Name)
	}
	targetIP := strings.TrimSpace(req.TargetIP)
	if targetIP == "" {
		targetIP = "127.0.0.1"
	}

	// Delegate to the canonical service: handles UDP-incompatibility rules
	// (clears subdomain + no_tls), bot-protection coercion (requires
	// subdomain + TCP), port validation, rathole assignment, Caddy/firewall
	// push, etc. We rebuild the response from the resulting db.Tunnel below.
	created, err := h.tunnelSvc.Create(dto.CreateTunnelRequest{
		MachineID:            machine.ID,
		Name:                 subdomain, // dashboard uses Name as the display label
		Subdomain:            subdomain,
		LocalPort:            req.TargetPort,
		Transport:            transport,
		NoTLS:                req.NoTLS,
		Private:              req.Private,
		BotProtectionEnabled: req.BotProtectionEnabled,
		BotProtectionTTL:     req.BotProtectionTTL,
		BotProtectionAllowIP: req.BotProtectionAllowIP,
		TLSSkipVerify:        req.TLSSkipVerify,
	})
	if err != nil {
		switch err.(type) {
		case *apperrors.ValidationError:
			response.BadRequest(w, err.Error())
		case *apperrors.ConflictError:
			response.Conflict(w, err.Error())
		default:
			response.InternalError(w, err.Error())
		}
		return
	}

	// Build the public URL. Subdomain tunnels get the friendly hostname;
	// port-only and UDP tunnels surface as <gateway>:<server_port> so the
	// caller can reach the service without DNS magic.
	settings, _ := db.GetSettings()
	tunnelURL := ""
	switch {
	case created.Subdomain != "" && settings != nil && settings.Domain != "":
		scheme := "https"
		if created.NoTLS {
			scheme = "http"
		}
		tunnelURL = fmt.Sprintf("%s://%s.%s", scheme, created.Subdomain, settings.Domain)
	case settings != nil && (settings.ServerHost != "" || settings.Domain != ""):
		host := settings.ServerHost
		if host == "" {
			host = settings.Domain
		}
		tunnelURL = fmt.Sprintf("%s:%d", host, created.RatholePort)
	}

	et := &db.ExternalTunnel{
		ID: randHex(),
		// Store the external machine ID (what callers passed and what they
		// see in subsequent list/get responses). The link to the inner
		// db.Tunnel record goes through TunnelID, so we don't lose anything
		// by not storing the inner Machine.ID here.
		MachineID:  em.ID,
		TunnelID:   created.ID,
		Subdomain:  created.Subdomain,
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
