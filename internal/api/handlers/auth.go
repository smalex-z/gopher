package handlers

import (
	"encoding/json"
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

	response.Success(w, map[string]bool{
		"setup":         isSetup,
		"authenticated": isAuthenticated,
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

	// Log in immediately after setup
	token, err := h.authSvc.Login(body.Password)
	if err != nil {
		response.InternalError(w, "setup succeeded but login failed")
		return
	}
	setSessionCookie(w, r, token)
	response.Success(w, map[string]string{"message": "setup complete"})
}

// POST /api/auth/login
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	token, err := h.authSvc.Login(body.Password)
	if err != nil {
		response.Error(w, http.StatusUnauthorized, "invalid password")
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
		MaxAge:   int((24 * 60 * 60)), // 24 hours
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}
