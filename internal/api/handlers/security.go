package handlers

import (
	"encoding/json"
	"net/http"

	chi "github.com/go-chi/chi/v5"
	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/service"
)

type SecurityHandler struct {
	authSvc *service.AuthService
	secSvc  *service.SecurityService
}

func NewSecurityHandler(authSvc *service.AuthService, secSvc *service.SecurityService) *SecurityHandler {
	return &SecurityHandler{authSvc: authSvc, secSvc: secSvc}
}

// GET /api/security/stale-tokens
func (h *SecurityHandler) StaleTokenAttempts(w http.ResponseWriter, r *http.Request) {
	attempts, err := h.secSvc.StaleTokenAttempts()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, attempts)
}

// GET /api/security/logs
func (h *SecurityHandler) AuditLog(w http.ResponseWriter, r *http.Request) {
	response.Success(w, h.authSvc.AuditLog())
}

// GET /api/security/fail2ban
func (h *SecurityHandler) Fail2banStatus(w http.ResponseWriter, r *http.Request) {
	status, err := h.secSvc.Fail2banStatus()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, status)
}

// POST /api/security/fail2ban/unban
// Body: {"jail": "gopher-auth", "ip": "1.2.3.4"}
func (h *SecurityHandler) UnbanIP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Jail string `json:"jail"`
		IP   string `json:"ip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Jail == "" || body.IP == "" {
		response.BadRequest(w, "jail and ip are required")
		return
	}
	if err := h.secSvc.UnbanIP(body.Jail, body.IP); err != nil {
		response.InternalError(w, "unban failed: "+err.Error())
		return
	}
	response.Success(w, map[string]string{"message": "unbanned " + body.IP})
}

// GET /api/security/fail2ban/config
func (h *SecurityHandler) GetFail2banConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.secSvc.GetFail2banConfig()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, cfg)
}

// PUT /api/security/fail2ban/config
// Body: {"max_retry": 5, "find_time": 300, "ban_time": 3600}
func (h *SecurityHandler) SaveFail2banConfig(w http.ResponseWriter, r *http.Request) {
	var body service.Fail2banConfig
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	if body.MaxRetry < 1 || body.FindTime < 10 || body.BanTime < 10 {
		response.BadRequest(w, "invalid values: max_retry ≥ 1, find_time ≥ 10, ban_time ≥ 10")
		return
	}
	if err := h.secSvc.SaveFail2banConfig(&body); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"message": "fail2ban config saved"})
}

// POST /api/security/fail2ban/whitelist
// Body: {"ip": "1.2.3.4"}
func (h *SecurityHandler) AddWhitelistIP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IP string `json:"ip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.IP == "" {
		response.BadRequest(w, "ip is required")
		return
	}
	if err := h.secSvc.AddIgnoreIP(body.IP); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"message": "added to whitelist"})
}

// DELETE /api/security/fail2ban/whitelist/{ip}
func (h *SecurityHandler) RemoveWhitelistIP(w http.ResponseWriter, r *http.Request) {
	ip := chi.URLParam(r, "ip")
	if ip == "" {
		response.BadRequest(w, "ip is required")
		return
	}
	if err := h.secSvc.RemoveIgnoreIP(ip); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"message": "removed from whitelist"})
}
