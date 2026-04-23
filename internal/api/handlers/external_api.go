package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
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
}

func NewExternalAPIHandler(
	bootstrapSvc *service.BootstrapService,
	tunnelSvc *service.TunnelService,
	machineSvc *service.MachineService,
) *ExternalAPIHandler {
	return &ExternalAPIHandler{
		bootstrapSvc: bootstrapSvc,
		tunnelSvc:    tunnelSvc,
		machineSvc:   machineSvc,
	}
}

type createExternalTunnelRequest struct {
	Subdomain  string `json:"subdomain"`
	TargetIP   string `json:"target_ip"`
	TargetPort int    `json:"target_port"`
}

type externalTunnelResponse struct {
	ID           string    `json:"id"`
	Status       string    `json:"status"`
	Subdomain    string    `json:"subdomain"`
	TargetIP     string    `json:"target_ip"`
	BootstrapURL string    `json:"bootstrap_url,omitempty"`
	TunnelURL    string    `json:"tunnel_url,omitempty"`
	Error        string    `json:"error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// externalTunnelToResponse converts a DB record to the API response shape.
// If the tunnel is "pending" but its bootstrap token has already expired,
// the status is reported as "failed" so callers don't poll indefinitely.
func externalTunnelToResponse(ext *db.ExternalTunnel) externalTunnelResponse {
	status := ext.Status
	errMsg := ext.ErrorMsg

	if status == "pending" {
		if bt, err := db.GetBootstrapTokenByID(ext.TokenID); err == nil {
			if bt.UsedAt == nil && time.Now().After(bt.ExpiresAt) {
				status = "failed"
				errMsg = "bootstrap token expired before the machine registered"
			}
		}
	}

	r := externalTunnelResponse{
		ID:        ext.ID,
		Status:    status,
		Subdomain: ext.Subdomain,
		TargetIP:  ext.TargetIP,
		Error:     errMsg,
		CreatedAt: ext.CreatedAt,
	}
	if status == "active" {
		r.TunnelURL = ext.TunnelURL
	}
	return r
}

// POST /api/v1/tunnels
func (h *ExternalAPIHandler) CreateTunnel(w http.ResponseWriter, r *http.Request) {
	var req createExternalTunnelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	if req.Subdomain == "" || req.TargetPort == 0 {
		response.BadRequest(w, "subdomain and target_port are required")
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

	if err := config.ValidateSubdomain(req.Subdomain); err != nil {
		response.BadRequest(w, fmt.Sprintf("invalid subdomain: %v", err))
		return
	}

	// Check both the live Tunnel table and any pending ExternalTunnel records.
	if taken, err := db.CheckSubdomainExists(req.Subdomain); err != nil {
		response.InternalError(w, "failed to check subdomain")
		return
	} else if taken {
		response.Conflict(w, "subdomain already in use")
		return
	}
	if taken, err := db.CheckExternalTunnelSubdomainExists(req.Subdomain); err != nil {
		response.InternalError(w, "failed to check subdomain")
		return
	} else if taken {
		response.Conflict(w, "subdomain already in use")
		return
	}

	sshKey, err := db.GetDefaultSSHKey()
	if err != nil {
		response.BadRequest(w, "no SSH key configured; add a key in Settings before using the external API")
		return
	}

	bt, err := h.bootstrapSvc.GenerateToken(0, sshKey.ID, false)
	if err != nil {
		response.InternalError(w, fmt.Sprintf("failed to generate bootstrap token: %v", err))
		return
	}

	ext := &db.ExternalTunnel{
		ID:         randHex(),
		Subdomain:  req.Subdomain,
		TargetIP:   req.TargetIP,
		TargetPort: req.TargetPort,
		TokenID:    bt.ID,
		Status:     "pending",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := db.CreateExternalTunnel(ext); err != nil {
		response.InternalError(w, fmt.Sprintf("failed to save tunnel record: %v", err))
		return
	}

	base := hostURL(r)
	resp := externalTunnelToResponse(ext)
	resp.BootstrapURL = fmt.Sprintf("%s/bootstrap/%s", base, bt.Token)
	response.Created(w, resp)
}

// GET /api/v1/tunnels
// Supports ?limit=N&offset=N (default limit 50, max 200).
func (h *ExternalAPIHandler) ListTunnels(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)
	if limit > 200 {
		limit = 200
	}
	if limit < 0 {
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

// GET /api/v1/tunnels/{id}
func (h *ExternalAPIHandler) GetTunnel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ext, err := db.GetExternalTunnel(id)
	if err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "tunnel not found")
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, externalTunnelToResponse(ext))
}

// DELETE /api/v1/tunnels/{id}
// Performs a full teardown: SSHes into the client machine to run the uninstall
// script, removes service tunnel Caddy config, reconciles rathole, and deletes
// all DB records. Safe to call even if the VM is already destroyed — SSH errors
// are best-effort and non-fatal.
func (h *ExternalAPIHandler) DeleteTunnel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ext, err := db.GetExternalTunnel(id)
	if err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "tunnel not found")
			return
		}
		response.InternalError(w, err.Error())
		return
	}

	// Full machine teardown: uninstalls rathole-client on the VM (best-effort SSH),
	// removes all associated service tunnels + Caddy config, reconciles rathole
	// server config, then removes the machine DB record.
	// If no machine was ever created (bootstrap never completed), fall back to
	// deleting only the service tunnel record if one exists.
	if ext.MachineID != nil {
		if delErr := h.machineSvc.Delete(*ext.MachineID); delErr != nil {
			if _, ok := delErr.(*apperrors.NotFoundError); !ok {
				response.InternalError(w, fmt.Sprintf("failed to delete machine: %v", delErr))
				return
			}
		}
	} else if ext.TunnelID != nil {
		// Machine record was never created but a tunnel somehow exists — clean it up.
		if delErr := h.tunnelSvc.Delete(*ext.TunnelID); delErr != nil {
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
