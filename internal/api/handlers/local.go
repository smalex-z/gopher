package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/service"
)

type LocalHandler struct {
	svc *service.LocalSetupService
}

func NewLocalHandler(svc *service.LocalSetupService) *LocalHandler {
	return &LocalHandler{svc: svc}
}

// GET /api/local/status
func (h *LocalHandler) Status(w http.ResponseWriter, r *http.Request) {
	status, err := h.svc.Status()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, status)
}

// POST /api/local/install
func (h *LocalHandler) Install(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	if body.Domain == "" {
		response.BadRequest(w, "domain is required")
		return
	}
	h.svc.Install(body.Domain)
	response.Success(w, map[string]string{"message": "install started"})
}

// POST /api/local/skip
func (h *LocalHandler) Skip(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Skip(); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"message": "skipped"})
}
