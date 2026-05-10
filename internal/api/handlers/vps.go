package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/smalex-z/gopher/internal/api/dto"
	"github.com/smalex-z/gopher/internal/api/response"
	apperrors "github.com/smalex-z/gopher/internal/errors"
	"github.com/smalex-z/gopher/internal/service"
)

type VPSHandler struct {
	svc *service.VPSService
}

func NewVPSHandler(svc *service.VPSService) *VPSHandler {
	return &VPSHandler{svc: svc}
}

func (h *VPSHandler) Get(w http.ResponseWriter, r *http.Request) {
	vps, err := h.svc.Get()
	if err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "VPS not configured")
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, vps)
}

func (h *VPSHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req dto.CreateVPSRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}

	vps, err := h.svc.Create(req)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Created(w, vps)
}

func (h *VPSHandler) Update(w http.ResponseWriter, r *http.Request) {
	vps, err := h.svc.Get()
	if err != nil {
		response.NotFound(w, "VPS not configured")
		return
	}

	var req dto.UpdateVPSRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}

	updated, err := h.svc.Update(vps.ID, req)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, updated)
}

func (h *VPSHandler) Delete(w http.ResponseWriter, r *http.Request) {
	vps, err := h.svc.Get()
	if err != nil {
		response.NotFound(w, "VPS not configured")
		return
	}
	if err := h.svc.Delete(vps.ID); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.NoContent(w)
}

func (h *VPSHandler) Bootstrap(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Bootstrap(); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"status": "bootstrap started"})
}

func (h *VPSHandler) Status(w http.ResponseWriter, r *http.Request) {
	status, err := h.svc.Status()
	if err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "VPS not configured")
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, status)
}
