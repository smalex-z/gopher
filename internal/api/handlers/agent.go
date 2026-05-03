package handlers

import (
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

// POST /api/machines/{id}/install-agent — runs the migration installer for
// existing machines. Response surfaces the machine's updated agent fields so
// the UI can flip the badge without a refetch.
func (h *AgentHandler) InstallAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		response.BadRequest(w, "machine id required")
		return
	}
	if err := h.installer.Install(id); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	m, err := db.GetMachine(id)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, m)
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

// GET /api/machines/{id}/health — return the latest health check + recent
// history for a machine.
func (h *AgentHandler) MachineHealth(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		response.BadRequest(w, "machine id required")
		return
	}
	subject := "machine:" + id
	latest, err := db.LatestHealthCheck(subject)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	recent, err := db.GetRecentHealthChecks(subject, 30)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]any{
		"latest": latest,
		"recent": recent,
	})
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
