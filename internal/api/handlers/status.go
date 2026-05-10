package handlers

import (
	"net/http"

	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/build"
	"github.com/smalex-z/gopher/internal/db"
)

func StatusHandler(w http.ResponseWriter, r *http.Request) {
	machines, _ := db.GetMachines()
	tunnels, _ := db.GetTunnels()
	vps, _ := db.GetVPS()

	data := map[string]interface{}{
		"machines": len(machines),
		"tunnels":  len(tunnels),
		"vps":      vps != nil,
	}
	response.Success(w, data)
}

// HealthzHandler is an unauthenticated liveness probe for external monitors.
// Returns 200 + minimal JSON ({"status":"ok","version":"..."}). Deliberately
// scoped to "process is up and the DB pool is reachable" — does NOT leak
// machine/tunnel counts, install state, or any operator-relevant data, so
// it's safe to expose without auth. Pair with a separate authed endpoint
// (e.g., /api/local/status) for richer probes.
func HealthzHandler(w http.ResponseWriter, r *http.Request) {
	// One-byte SELECT to confirm the DB connection pool answers; if it
	// doesn't, the gopher process is up but useless to monitors.
	if db.DB == nil {
		response.Error(w, http.StatusServiceUnavailable, "db not initialised")
		return
	}
	if err := db.DB.Exec("SELECT 1").Error; err != nil {
		response.Error(w, http.StatusServiceUnavailable, "db unreachable")
		return
	}
	response.Success(w, map[string]string{
		"status":  "ok",
		"version": build.Version,
	})
}
