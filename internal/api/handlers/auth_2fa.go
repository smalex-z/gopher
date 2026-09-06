package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/smalex-z/gopher/internal/api/response"
	apperrors "github.com/smalex-z/gopher/internal/errors"
	"github.com/smalex-z/gopher/internal/service"
)

// GET /api/auth/2fa/status
func (h *AuthHandler) TOTPStatus(w http.ResponseWriter, r *http.Request) {
	enabled, devices, remaining, err := h.authSvc.TOTPStatus()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]interface{}{
		"enabled":                enabled,
		"devices":                devices,
		"backup_codes_remaining": remaining,
	})
}

// POST /api/auth/2fa/enroll
// Generates a new TOTP secret + QR for an additional device. Confirm to commit.
func (h *AuthHandler) TOTPEnroll(w http.ResponseWriter, r *http.Request) {
	secret, qrDataURL, err := h.authSvc.TOTPEnroll()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{
		"secret":      secret,
		"qr_data_url": qrDataURL,
	})
}

// POST /api/auth/2fa/confirm
// Body: {"code": "123456", "name": "iPhone"}
// Verifies the code against the pending secret and saves the device.
// Returns backup codes only on FIRST device enrollment.
func (h *AuthHandler) TOTPConfirm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Code == "" {
		response.BadRequest(w, "code is required")
		return
	}
	backupCodes, err := h.authSvc.TOTPConfirm(body.Code, body.Name, service.ClientIP(r))
	if err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	resp := map[string]interface{}{
		"message": "Device enrolled",
	}
	if len(backupCodes) > 0 {
		resp["backup_codes"] = backupCodes
		resp["message"] = "2FA enabled — save your backup codes now, they won't be shown again"
	}
	response.Success(w, resp)
}

// POST /api/auth/2fa/disable
// Body: {"code": "123456"}
// Removes ALL devices and disables 2FA. Verifies via any device or a backup code.
func (h *AuthHandler) TOTPDisable(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Code == "" {
		response.BadRequest(w, "code is required")
		return
	}
	if err := h.authSvc.TOTPDisable(body.Code, service.ClientIP(r)); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"message": "2FA disabled"})
}

// DELETE /api/auth/2fa/devices/{id}
// Body: {"code": "123456"}
// Removes a single device. Verifies via a *different* device or a backup code.
func (h *AuthHandler) TOTPRemoveDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		response.BadRequest(w, "device id is required")
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Code == "" {
		response.BadRequest(w, "code is required")
		return
	}
	if err := h.authSvc.TOTPRemoveDevice(id, body.Code, service.ClientIP(r)); err != nil {
		if _, ok := err.(*apperrors.NotFoundError); ok {
			response.NotFound(w, "device not found")
			return
		}
		response.BadRequest(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"message": "Device removed"})
}

// POST /api/auth/2fa/backup-codes/regenerate
// Body: {"code": "123456"}
func (h *AuthHandler) TOTPRegenerateBackupCodes(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Code == "" {
		response.BadRequest(w, "code is required")
		return
	}
	codes, err := h.authSvc.TOTPRegenerateBackupCodes(body.Code, service.ClientIP(r))
	if err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	response.Success(w, map[string]interface{}{
		"backup_codes": codes,
		"message":      "Backup codes regenerated — save these now, they won't be shown again",
	})
}
