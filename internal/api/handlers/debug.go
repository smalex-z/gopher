package handlers

import (
	"net/http"
	"strings"

	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/config"
	"github.com/smalex-z/gopher/internal/db"
)

// DebugHandler exposes generated config files for inspection.
// All routes are protected by AuthMiddleware.
type DebugHandler struct{}

func NewDebugHandler() *DebugHandler {
	return &DebugHandler{}
}

// GET /api/debug/caddyfile — render and return the Caddyfile that would be deployed.
func (h *DebugHandler) GetCaddyfile(w http.ResponseWriter, r *http.Request) {
	vps, err := db.GetVPS()
	if err != nil {
		// Fall back to the domain stored in app settings (set via POST /api/local/skip or /install).
		settings, sErr := db.GetSettings()
		if sErr != nil || settings.Domain == "" {
			response.NotFound(w, "no VPS or domain configured")
			return
		}
		vps = &db.VPSConfig{Domain: settings.Domain}
	}

	tunnels, err := db.GetAllTunnelsForVPS()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}

	caddyfile, err := config.GenerateCaddyfile(*vps, tunnels)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(caddyfile))
}

// GET /api/debug/rathole-server — render and return the rathole server config that would be deployed.
func (h *DebugHandler) GetRatholeServerConfig(w http.ResponseWriter, r *http.Request) {
	tunnels, err := db.GetAllTunnelsForVPS()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}

	machines, err := db.GetMachines()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}

	bindIP := ""
	noisePriv := ""
	if settings, sErr := db.GetSettings(); sErr == nil && settings != nil {
		bindIP = settings.BindIP
		noisePriv = settings.RatholeNoisePrivKey
	}
	cfg := config.GenerateRatholeServerConfig(machines, tunnels, bindIP, noisePriv)
	cfg = strings.TrimRight(cfg, "\n") + "\n\n# ===== BEGIN CUSTOM CONFIGURATION =====\n# ===== END CUSTOM CONFIGURATION =====\n"

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(cfg))
}
