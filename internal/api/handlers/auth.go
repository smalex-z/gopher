package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/service"
)

const sessionCookie = "gopher_session"

type AuthHandler struct {
	authSvc *service.AuthService
}

func NewAuthHandler(authSvc *service.AuthService) *AuthHandler {
	return &AuthHandler{authSvc: authSvc}
}

// GET /api/auth/status
func (h *AuthHandler) Status(w http.ResponseWriter, r *http.Request) {
	isSetup, err := h.authSvc.IsSetup()
	if err != nil {
		response.InternalError(w, "failed to check setup status")
		return
	}

	isAuthenticated := false
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		isAuthenticated = h.authSvc.ValidateSession(cookie.Value)
	}

	totpEnabled, _, _, _ := h.authSvc.TOTPStatus()

	response.Success(w, map[string]interface{}{
		"setup":         isSetup,
		"authenticated": isAuthenticated,
		"totp_enabled":  totpEnabled,
	})
}

// POST /api/auth/setup
func (h *AuthHandler) Setup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	if err := h.authSvc.Setup(body.Password); err != nil {
		if err.Error() == "already configured" {
			response.Error(w, http.StatusConflict, "already configured")
			return
		}
		response.BadRequest(w, err.Error())
		return
	}

	// Log in immediately after setup (no 2FA yet at this point).
	result, err := h.authSvc.Login(body.Password, service.ClientIP(r))
	if err != nil || result.NeedsTOTP {
		response.InternalError(w, "setup succeeded but login failed")
		return
	}
	setSessionCookie(w, r, result.Token)
	response.Success(w, map[string]string{"message": "setup complete"})
}

// POST /api/auth/login
// Body: {"password": "...", "totp_code": "..."}
// When 2FA is enabled and totp_code is omitted, returns HTTP 200 with
// {"needs_2fa": true, "pending_token": "..."} and no session cookie.
// The client must then POST /api/auth/login/2fa with the pending_token and code.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}

	ip := service.ClientIP(r)
	result, err := h.authSvc.Login(body.Password, ip)
	if err != nil {
		if err.Error() == "too many attempts" {
			w.Header().Set("Retry-After", fmt.Sprintf("%.0f", h.authSvc.RateLimiter().RetryAfter(ip).Seconds()))
			response.Error(w, http.StatusTooManyRequests, "too many login attempts — try again later")
			return
		}
		response.Error(w, http.StatusUnauthorized, "invalid password")
		return
	}

	if result.NeedsTOTP {
		response.Success(w, map[string]interface{}{
			"needs_2fa":     true,
			"pending_token": result.PendingToken,
		})
		return
	}

	setSessionCookie(w, r, result.Token)
	response.Success(w, map[string]string{"message": "logged in"})
}

// POST /api/auth/login/2fa
// Body: {"pending_token": "...", "code": "..."}
func (h *AuthHandler) LoginTOTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PendingToken string `json:"pending_token"`
		Code         string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	if body.PendingToken == "" || body.Code == "" {
		response.BadRequest(w, "pending_token and code are required")
		return
	}

	ip := service.ClientIP(r)
	token, err := h.authSvc.LoginTOTP(body.PendingToken, body.Code, ip)
	if err != nil {
		response.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	setSessionCookie(w, r, token)
	response.Success(w, map[string]string{"message": "logged in"})
}

// POST /api/auth/logout
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		h.authSvc.Logout(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	response.Success(w, map[string]string{"message": "logged out"})
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int((24 * 60 * 60)),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}
