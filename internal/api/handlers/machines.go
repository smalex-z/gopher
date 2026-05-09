package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/smalex-z/gopher/internal/api/dto"
	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/db"
	apperrors "github.com/smalex-z/gopher/internal/errors"
	"github.com/smalex-z/gopher/internal/service"
)

type MachineHandler struct {
	svc *service.MachineService
}

func NewMachineHandler(svc *service.MachineService) *MachineHandler {
	return &MachineHandler{svc: svc}
}

func (h *MachineHandler) List(w http.ResponseWriter, r *http.Request) {
	machines, err := h.svc.List()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, machines)
}

func (h *MachineHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req dto.CreateMachineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	machine, err := h.svc.Create(req)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Created(w, machine)
}

func (h *MachineHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	machine, err := h.svc.Get(id)
	if err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "machine not found")
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, machine)
}

func (h *MachineHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req dto.UpdateMachineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	machine, err := h.svc.Update(id, req)
	if err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "machine not found")
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, machine)
}

func (h *MachineHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	res, err := h.svc.Delete(id)
	if err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "machine not found")
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	// Two-shape response: 204 when the delete was fully clean (preserves the
	// historical caller contract — including tests/critical-path.sh — that
	// /api/machines/{id} DELETE returns no content on success). 200 with a
	// DeleteResult body only when client-side cleanup actually failed, so
	// the dashboard can surface "deleted on server but the box is still
	// dirty" via toast. The frontend uses optional chaining on resp.data so
	// the 204 path harmlessly falls into the plain "Machine deleted." toast.
	if res == nil || res.ClientCleanupOK {
		response.NoContent(w)
		return
	}
	response.Success(w, res)
}

// SelfDelete is called by gopher-uninstall on the client when an operator
// runs it locally (e.g. `sudo gopher-uninstall`). The client authenticates
// with its per-machine agent bearer token — same token the agent uses for
// its API back-channel, so anyone with sudo on the box already has it.
//
// The dashboard's normal delete path runs the SAME teardown via the agent
// (POST /uninstall) and then deletes the DB record; this endpoint just
// inverts the trigger direction so the server stays in sync when cleanup
// starts on the client.
//
// Public route — no session cookie required, since the caller is the dying
// machine, not a logged-in operator.
func (h *MachineHandler) SelfDelete(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		response.BadRequest(w, "missing bearer token")
		return
	}
	token := strings.TrimSpace(auth[len(prefix):])

	machine, err := db.GetMachineByAgentToken(token)
	if err != nil {
		// Don't leak whether the token is wrong vs. unknown — both look
		// the same to the caller. 404 keeps the response shape consistent
		// with other "not found" cases on this handler.
		response.NotFound(w, "machine not found")
		return
	}

	if _, err := h.svc.DeleteFromClient(machine.ID); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"deleted": machine.ID})
}

func (h *MachineHandler) Deploy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.Deploy(id); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"status": "deploy started"})
}

// PUT /api/machines/{id}/ssh-key — push a new SSH key to the machine and update its record
func (h *MachineHandler) ReassignSSHKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		SSHKeyID string `json:"ssh_key_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SSHKeyID == "" {
		response.BadRequest(w, "ssh_key_id is required")
		return
	}
	if err := h.svc.ReassignSSHKey(id, body.SSHKeyID); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"message": "SSH key updated"})
}

func (h *MachineHandler) NetworkInfo(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	info, err := h.svc.RefreshNetworkInfo(id)
	if err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "machine not found")
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, info)
}

func (h *MachineHandler) Status(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	status, err := h.svc.Status(id)
	if err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "machine not found")
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, status)
}
