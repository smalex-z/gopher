package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/db"
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

// SetChannel updates the update channel preference (stable / beta / alpha).
func (h *UpdateHandler) SetChannel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Channel string `json:"channel"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	switch body.Channel {
	case "stable", "beta", "alpha":
	default:
		response.BadRequest(w, "channel must be one of: stable, beta, alpha")
		return
	}
	if err := db.MutateSettings(func(s *db.AppSettings) error {
		s.UpdateChannel = body.Channel
		return nil
	}); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"channel": body.Channel})
}
