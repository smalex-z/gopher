package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/smalex-z/gopher/internal/api/response"
)

// GET /api/auth/2fa/status
func (h *AuthHandler) TOTPStatus(w http.ResponseWriter, r *http.Request) {
	enabled, remaining, err := h.authSvc.TOTPStatus()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]interface{}{
		"enabled":                enabled,
		"backup_codes_remaining": remaining,
	})
}

// POST /api/auth/2fa/enroll
// Generates a new TOTP secret and QR code. Does not enable 2FA until confirmed.
func (h *AuthHandler) TOTPEnroll(w http.ResponseWriter, r *http.Request) {
	secret, qrDataURL, err := h.authSvc.TOTPEnroll()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{
		"secret":     secret,
		"qr_data_url": qrDataURL,
	})
}

// POST /api/auth/2fa/confirm
// Body: {"code": "123456"}
// Verifies the first TOTP code and enables 2FA. Returns backup codes.
func (h *AuthHandler) TOTPConfirm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Code == "" {
		response.BadRequest(w, "code is required")
		return
	}
	backupCodes, err := h.authSvc.TOTPConfirm(body.Code)
	if err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	response.Success(w, map[string]interface{}{
		"backup_codes": backupCodes,
		"message":      "2FA enabled — save your backup codes now, they won't be shown again",
	})
}

// POST /api/auth/2fa/disable
// Body: {"code": "123456"}
// Disables 2FA after verifying the current TOTP or a backup code.
func (h *AuthHandler) TOTPDisable(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Code == "" {
		response.BadRequest(w, "code is required")
		return
	}
	if err := h.authSvc.TOTPDisable(body.Code); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"message": "2FA disabled"})
}

// POST /api/auth/2fa/backup-codes/regenerate
// Body: {"code": "123456"}
// Replaces all backup codes. Requires a valid TOTP code.
func (h *AuthHandler) TOTPRegenerateBackupCodes(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Code == "" {
		response.BadRequest(w, "code is required")
		return
	}
	codes, err := h.authSvc.TOTPRegenerateBackupCodes(body.Code)
	if err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	response.Success(w, map[string]interface{}{
		"backup_codes": codes,
		"message":      "Backup codes regenerated — save these now, they won't be shown again",
	})
}
