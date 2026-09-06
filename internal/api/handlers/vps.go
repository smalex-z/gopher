package handlers

import (
	"net/http"

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
