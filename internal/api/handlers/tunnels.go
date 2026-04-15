package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/smalex-z/gopher/internal/api/dto"
	"github.com/smalex-z/gopher/internal/api/response"
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
	if status != "active" {
		msg := "Tunnel is offline — no client connected"
		if status == "idle" {
			msg = "Tunnel is connected but nothing is listening on port " + itoa(tunnel.LocalPort) + " on the client"
		}
		response.BadRequest(w, msg)
		return
	}
	response.Success(w, map[string]string{"status": status})
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
