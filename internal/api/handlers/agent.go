package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/smalex-z/gopher/internal/api/response"
	apperrors "github.com/smalex-z/gopher/internal/errors"
	"github.com/smalex-z/gopher/internal/db"
	"github.com/smalex-z/gopher/internal/service"
)

// AgentHandler exposes endpoints for the gopher-agent rollout (install on
// existing machines, surface health data, manual "test now") and wraps the
// HealthService + AgentInstaller services.
type AgentHandler struct {
	installer *service.AgentInstaller
	health    *service.HealthService
}

func NewAgentHandler(installer *service.AgentInstaller, health *service.HealthService) *AgentHandler {
	return &AgentHandler{installer: installer, health: health}
}

// POST /api/machines/{id}/install-agent — returns the operator-paste command
// that installs the agent on the target machine. The dashboard cannot install
// the agent remotely (needs root on the target, which we don't have via SSH
// for already-bootstrapped machines), so the response is the curl-bash
// one-liner the operator runs once on the box. After the agent is up, it
// registers automatically via the rathole back-channel and HealthService
// flips Machine.AgentInstalled=true.
func (h *AgentHandler) InstallAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		response.BadRequest(w, "machine id required")
		return
	}
	instr, err := h.installer.Install(id)
	if err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	response.Success(w, instr)
}

// GET /api/machines/agent/pending — machines that don't yet have the agent
// installed. Used by the dashboard banner.
func (h *AgentHandler) PendingMigrations(w http.ResponseWriter, r *http.Request) {
	rows, err := db.MachinesWithoutAgent()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, rows)
}

// GET /api/machines/{id}/health — returns the per-machine HealthSummary
// (uptime %, latest, recent rows for sparkline). 24-hour window.
func (h *AgentHandler) MachineHealth(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		response.BadRequest(w, "machine id required")
		return
	}
	summary, err := db.GetHealthSummary("machine:"+id, time.Time{}, 30)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, summary)
}

// GET /api/machines/{id}/agent-status — proxies the agent's /status endpoint
// through the rathole back-channel. Returns live system metrics (load, memory,
// disk, uptime) the dashboard renders in the per-machine metrics card.
//
// On-demand only: doesn't write to the DB, doesn't trigger health-recording
// side effects. The health service's 60s poll loop handles that path; this is
// for the UI when the operator expands a machine row.
func (h *AgentHandler) AgentStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		response.BadRequest(w, "machine id required")
		return
	}
	machine, err := db.GetMachine(id)
	if err != nil {
		response.NotFound(w, "machine not found")
		return
	}
	if !machine.AgentInstalled || machine.AgentRemotePort == 0 {
		response.BadRequest(w, "agent not installed on this machine")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	status, err := service.NewAgentClient(machine).Status(ctx)
	if err != nil {
		response.InternalError(w, "agent unreachable: "+err.Error())
		return
	}
	response.Success(w, status)
}

// POST /api/machines/{id}/health/check — runs a one-off check on demand and
// returns the result. The "Test now" button.
func (h *AgentHandler) RunCheck(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		response.BadRequest(w, "machine id required")
		return
	}
	check, err := h.health.CheckMachineNow(id)
	if err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "machine not found")
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]any{
		"check": check,
		"now":   time.Now(),
	})
}
