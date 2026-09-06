package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/smalex-z/gopher/internal/api/dto"
	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/db"
	apperrors "github.com/smalex-z/gopher/internal/errors"
	"github.com/smalex-z/gopher/internal/paths"
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
		if errors.Is(err, service.ErrOpInProgress) {
			response.Error(w, http.StatusConflict, err.Error())
			return
		}
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

// POST /api/machines/{id}/recover — try to repair a stuck machine from the
// server side without operator intervention. Walks the same fallback chain
// the noise migration uses: agent HTTP push first (cheapest, no SSH), then
// SSH-via-tunnel. Returns 200 on success — rathole-client on the box will
// notice the new config via inotify and reconnect within seconds.
//
// 500 with the underlying error means both paths failed: typically the
// rathole tunnel is fully down, so neither the agent nor SSH-via-tunnel can
// reach the box. That's when the operator falls back to the manual script
// (GET /rathole-config?format=script), which is rendered in the UI modal.
func (h *MachineHandler) Recover(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.RecoverMachine(id); err != nil {
		response.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	response.Success(w, map[string]string{"message": "config pushed; rathole-client will reconnect within seconds"})
}

// GET /api/machines/{id}/rathole-config — returns the canonical client.toml
// the machine should be running, plus an optional ?format=script wrapper that
// renders a one-shot recovery shell script. Manual recovery handle for when
// the automated push path can't reach the machine (full disk, offline,
// agent down, etc.). The same content the agent would have written if the
// push had succeeded; safe to copy-paste to /etc/rathole/client.toml or pipe
// through bash on the box.
func (h *MachineHandler) RatholeConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cfg, err := h.svc.CanonicalRatholeConfig(id)
	if err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "machine not found")
			return
		}
		response.InternalError(w, err.Error())
		return
	}

	if r.URL.Query().Get("format") == "script" {
		script := buildRecoveryScript(cfg)
		w.Header().Set("Content-Type", "text/x-shellscript")
		w.Header().Set("Content-Disposition", `attachment; filename="gopher-recover-`+id+`.sh"`)
		_, _ = w.Write([]byte(script))
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="client.toml"`)
	_, _ = w.Write([]byte(cfg))
}

// buildRecoveryScript wraps a client.toml payload in a one-shot recovery
// script. Hands the operator a no-thinking-required recovery: pipe through
// `bash` on the broken machine (curl ... | bash, or download then bash file).
//
// Sudo strategy mirrors bootstrap.sh: $SUDO is empty when already root,
// "sudo" otherwise. The earlier version did `if [ $(id -u) -ne 0 ]; then
// exit 1; fi` — when pasted into a non-root interactive shell under set -e,
// that exit 1 terminated the operator's whole shell session (it logged
// justin out of SSH). Sudo-prefixing every privileged command means the
// script works regardless of how it's invoked.
func buildRecoveryScript(clientToml string) string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	b.WriteString("# Generated by Gopher — restores the rathole client.toml to its canonical state.\n")
	b.WriteString("# Run via: curl -fsSL <this-url> | bash   (or download, then: bash <file>)\n")
	b.WriteString("set -e\n")
	b.WriteString("SUDO=\"\"\n")
	b.WriteString("if [ \"$(id -u)\" -ne 0 ]; then SUDO=\"sudo\"; fi\n")
	// Write to the consolidated /etc/gopher path unless the machine is still on
	// the legacy /etc/rathole layout (no migrated agent yet) — match whatever the
	// running rathole-client expects so recovery doesn't strand it.
	b.WriteString("CFG=" + paths.RatholeClientConfig + "\n")
	b.WriteString("if [ ! -f \"$CFG\" ] && [ -f " + paths.LegacyRatholeClientConfig + " ]; then CFG=" + paths.LegacyRatholeClientConfig + "; fi\n")
	b.WriteString("$SUDO mkdir -p \"$(dirname \"$CFG\")\"\n")
	b.WriteString("$SUDO rm -f \"$(dirname \"$CFG\")/.client.toml.swp\"\n")
	b.WriteString("$SUDO tee \"$CFG\" > /dev/null <<'GOPHER_CONFIG_EOF'\n")
	b.WriteString(clientToml)
	if !strings.HasSuffix(clientToml, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("GOPHER_CONFIG_EOF\n")
	b.WriteString("if id -u gopher >/dev/null 2>&1; then $SUDO chown gopher:gopher \"$CFG\"; fi\n")
	b.WriteString("$SUDO chmod 0644 \"$CFG\"\n")
	b.WriteString("$SUDO systemctl restart rathole-client 2>/dev/null || true\n")
	b.WriteString("echo 'gopher recovery complete'\n")
	return b.String()
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
