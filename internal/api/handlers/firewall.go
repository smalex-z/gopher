package handlers

import (
	"encoding/json"
	"net/http"

	chi "github.com/go-chi/chi/v5"
	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/service"
)

type FirewallHandler struct {
	svc *service.LocalSetupService
}

func NewFirewallHandler(svc *service.LocalSetupService) *FirewallHandler {
	return &FirewallHandler{svc: svc}
}

// GET /api/local/firewall/overview
func (h *FirewallHandler) Overview(w http.ResponseWriter, r *http.Request) {
	entries, err := h.svc.FirewallOverview()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, entries)
}

// POST /api/local/firewall/rules
func (h *FirewallHandler) CreateRule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Description string `json:"description"`
		Raw         bool   `json:"raw"`
		RawSpec     string `json:"raw_spec"`
		Protocol    string `json:"protocol"`
		PortRange   string `json:"port_range"`
		Source      string `json:"source"`
		Action      string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}

	var rule interface{}
	var err error
	if body.Raw {
		rule, err = h.svc.CreateRawFirewallRule(body.Description, body.RawSpec)
	} else {
		if body.Protocol == "" {
			body.Protocol = "tcp"
		}
		if body.Source == "" {
			body.Source = "0.0.0.0/0"
		}
		if body.Action == "" {
			body.Action = "ACCEPT"
		}
		rule, err = h.svc.CreateFirewallRule(body.Description, body.Protocol, body.PortRange, body.Source, body.Action)
	}
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, rule)
}

// DELETE /api/local/firewall/rules/{id}
func (h *FirewallHandler) DeleteRule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.DeleteFirewallRule(id); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"message": "rule deleted"})
}

// GET /api/local/firewall/live
func (h *FirewallHandler) LiveRules(w http.ResponseWriter, r *http.Request) {
	chains, err := h.svc.GetLiveRules()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, chains)
}

// POST /api/local/firewall/reload
func (h *FirewallHandler) Reload(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.ReloadFirewall(); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"message": "firewall reloaded"})
}
