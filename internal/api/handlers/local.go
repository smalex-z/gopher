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
	var body struct {
		Domain string `json:"domain"`
	}
	// Ignore decode errors — domain is optional
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := h.svc.Skip(body.Domain); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"message": "skipped"})
}

// POST /api/local/reconcile — rebuild server.toml from DB
func (h *LocalHandler) Reconcile(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.ReconcileServerConfig(); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"message": "server config reconciled"})
}

// GET /api/local/ssh-key — download the VPS SSH private key
func (h *LocalHandler) DownloadSSHKey(w http.ResponseWriter, r *http.Request) {
	key, err := h.svc.GetSSHPrivateKey()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if key == "" {
		response.NotFound(w, "no SSH key generated yet; bootstrap at least one machine first")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="gopher_id_rsa"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(key))
}
