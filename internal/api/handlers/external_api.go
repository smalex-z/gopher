package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
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
}

func NewExternalAPIHandler(bootstrapSvc *service.BootstrapService, tunnelSvc *service.TunnelService) *ExternalAPIHandler {
	return &ExternalAPIHandler{bootstrapSvc: bootstrapSvc, tunnelSvc: tunnelSvc}
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

func externalTunnelToResponse(ext *db.ExternalTunnel) externalTunnelResponse {
	r := externalTunnelResponse{
		ID:        ext.ID,
		Status:    ext.Status,
		Subdomain: ext.Subdomain,
		TargetIP:  ext.TargetIP,
		Error:     ext.ErrorMsg,
		CreatedAt: ext.CreatedAt,
	}
	if ext.Status == "active" {
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
	exists, err := db.CheckSubdomainExists(req.Subdomain)
	if err != nil {
		response.InternalError(w, "failed to check subdomain")
		return
	}
	if exists {
		response.Conflict(w, "subdomain already in use")
		return
	}
	// Also check pending external tunnels — a second request can arrive before
	// the async activation has had a chance to create the Tunnel record.
	extExists, err := db.CheckExternalTunnelSubdomainExists(req.Subdomain)
	if err != nil {
		response.InternalError(w, "failed to check subdomain")
		return
	}
	if extExists {
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
	bootstrapURL := fmt.Sprintf("%s/bootstrap/%s", base, bt.Token)

	resp := externalTunnelToResponse(ext)
	resp.BootstrapURL = bootstrapURL

	response.Created(w, resp)
}

// GET /api/v1/tunnels
func (h *ExternalAPIHandler) ListTunnels(w http.ResponseWriter, r *http.Request) {
	tunnels, err := db.GetExternalTunnels()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	out := make([]externalTunnelResponse, len(tunnels))
	for i, t := range tunnels {
		out[i] = externalTunnelToResponse(&t)
	}
	response.Success(w, out)
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

	// Tear down service tunnel if one was created.
	if ext.TunnelID != nil {
		if delErr := h.tunnelSvc.Delete(*ext.TunnelID); delErr != nil {
			if _, ok := delErr.(*apperrors.NotFoundError); !ok {
				response.InternalError(w, fmt.Sprintf("failed to delete service tunnel: %v", delErr))
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
