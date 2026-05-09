package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/smalex-z/gopher/internal/api/dto"
	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/db"
	apperrors "github.com/smalex-z/gopher/internal/errors"
	"github.com/smalex-z/gopher/internal/service"
)

type TunnelHandler struct {
	svc *service.TunnelService
}

func NewTunnelHandler(svc *service.TunnelService) *TunnelHandler {
	return &TunnelHandler{svc: svc}
}

func (h *TunnelHandler) List(w http.ResponseWriter, r *http.Request) {
	tunnels, err := h.svc.List()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, tunnels)
}

func (h *TunnelHandler) NextPort(w http.ResponseWriter, r *http.Request) {
	port, err := h.svc.NextPort()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]int{"port": port})
}

func (h *TunnelHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req dto.CreateTunnelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	tunnel, err := h.svc.Create(req)
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
	response.Created(w, tunnel)
}

func (h *TunnelHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tunnel, err := h.svc.Get(id)
	if err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "tunnel not found")
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, tunnel)
}

func (h *TunnelHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req dto.UpdateTunnelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	tunnel, err := h.svc.Update(id, req)
	if err != nil {
		switch err.(type) {
		case *apperrors.NotFoundError:
			response.NotFound(w, "tunnel not found")
		case *apperrors.ValidationError:
			response.BadRequest(w, err.Error())
		case *apperrors.ConflictError:
			response.Conflict(w, err.Error())
		default:
			response.InternalError(w, err.Error())
		}
		return
	}
	response.Success(w, tunnel)
}

func (h *TunnelHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.Delete(id); err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "tunnel not found")
			return
		}
		if _, ok := err.(*apperrors.ValidationError); ok {
			response.BadRequest(w, err.Error())
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	response.NoContent(w)
}

// GET /api/tunnels/{id}/health — uptime % + recent checks (sparkline source).
//
// 24-hour rolling window. Tunnels collect health rows from monitor.go's
// 30s probe loop; for managed entries (machine-ssh / machine-agent) the
// data lives under "machine:<id>" since they share lifecycle with the
// machine itself.
func (h *TunnelHandler) Health(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		response.BadRequest(w, "tunnel id required")
		return
	}
	subject := "tunnel:" + id
	if mid, ok := parseManagedTunnelID(id); ok {
		subject = "machine:" + mid
	}
	summary, err := db.GetHealthSummary(subject, time.Time{}, 30)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, summary)
}

// parseManagedTunnelID extracts the machine ID from synthesized SSH /
// agent tunnel IDs (e.g. "machine-abc123-ssh" or "machine-abc123-agent").
// Returns ("", false) for regular service tunnels.
func parseManagedTunnelID(id string) (string, bool) {
	const prefix = "machine-"
	if !strings.HasPrefix(id, prefix) {
		return "", false
	}
	body := strings.TrimPrefix(id, prefix)
	for _, suffix := range []string{"-ssh", "-agent"} {
		if strings.HasSuffix(body, suffix) {
			machineID := strings.TrimSuffix(body, suffix)
			if machineID != "" {
				return machineID, true
			}
		}
	}
	return "", false
}

func (h *TunnelHandler) Test(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tunnel, err := h.svc.Get(id)
	if err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "tunnel not found")
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	status := h.svc.Probe(tunnel)
	switch status {
	case "active", "connected":
		response.Success(w, map[string]string{"status": status})
	case "idle":
		response.BadRequest(w, "Tunnel is connected but nothing is listening on port "+itoa(tunnel.LocalPort)+" on the client")
	default:
		response.BadRequest(w, "Tunnel is offline — no client connected")
	}
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
