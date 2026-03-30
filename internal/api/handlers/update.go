package handlers

import (
	"net/http"

	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/service"
)

type UpdateHandler struct {
	svc *service.UpdateService
}

func NewUpdateHandler(svc *service.UpdateService) *UpdateHandler {
	return &UpdateHandler{svc: svc}
}

func (h *UpdateHandler) Check(w http.ResponseWriter, r *http.Request) {
	info, err := h.svc.Check()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, info)
}

// Apply downloads the latest binary, replaces the current one, and schedules
// a service restart. Returns 202 so the client can expect the server to restart.
func (h *UpdateHandler) Apply(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Apply(); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.JSON(w, http.StatusAccepted, map[string]any{
		"success": true,
		"message": "Update applied, server restarting",
	})
}
