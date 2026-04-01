package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/smalex-z/gopher/internal/api/dto"
	"github.com/smalex-z/gopher/internal/api/response"
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
	if err := h.svc.Delete(id); err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "machine not found")
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	response.NoContent(w)
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
