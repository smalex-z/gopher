package api

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"
	"github.com/smalex-z/gopher/internal/config"
	"github.com/smalex-z/gopher/internal/db"
	gopherSSH "github.com/smalex-z/gopher/internal/ssh"
)

func RegisterRoutes(r *mux.Router, database *sql.DB) {
	api := r.PathPrefix("/api").Subrouter()

	// VPS
	api.HandleFunc("/vps", makeHandler(database, getVPS)).Methods("GET")
	api.HandleFunc("/vps", makeHandler(database, upsertVPS)).Methods("PUT", "POST")
	api.HandleFunc("/vps/setup", makeHandler(database, setupVPS)).Methods("POST")
	api.HandleFunc("/vps/deploy", makeHandler(database, deployVPS)).Methods("POST")

	// Machines
	api.HandleFunc("/machines", makeHandler(database, listMachines)).Methods("GET")
	api.HandleFunc("/machines", makeHandler(database, createMachine)).Methods("POST")
	api.HandleFunc("/machines/{id}", makeHandler(database, getMachine)).Methods("GET")
	api.HandleFunc("/machines/{id}", makeHandler(database, updateMachine)).Methods("PUT")
	api.HandleFunc("/machines/{id}", makeHandler(database, deleteMachine)).Methods("DELETE")
	api.HandleFunc("/machines/{id}/deploy", makeHandler(database, deployMachine)).Methods("POST")

	// Tunnels
	api.HandleFunc("/tunnels", makeHandler(database, listTunnels)).Methods("GET")
	api.HandleFunc("/tunnels", makeHandler(database, createTunnel)).Methods("POST")
	api.HandleFunc("/tunnels/{id}", makeHandler(database, getTunnel)).Methods("GET")
	api.HandleFunc("/tunnels/{id}", makeHandler(database, updateTunnel)).Methods("PUT")
	api.HandleFunc("/tunnels/{id}", makeHandler(database, deleteTunnel)).Methods("DELETE")
}

type handlerFunc func(db *sql.DB, w http.ResponseWriter, r *http.Request)

func makeHandler(database *sql.DB, fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fn(database, w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func randomToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// --- VPS handlers ---

func getVPS(database *sql.DB, w http.ResponseWriter, r *http.Request) {
	v, err := db.GetVPS(database)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if v == nil {
		writeJSON(w, 404, map[string]string{"error": "not configured"})
		return
	}
	writeJSON(w, 200, v)
}

func upsertVPS(database *sql.DB, w http.ResponseWriter, r *http.Request) {
	var v db.VPSConfig
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if v.Port == 0 {
		v.Port = 22
	}
	if err := db.UpsertVPS(database, &v); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func setupVPS(database *sql.DB, w http.ResponseWriter, r *http.Request) {
	vps, err := db.GetVPS(database)
	if err != nil || vps == nil {
		writeError(w, 400, "VPS not configured")
		return
	}
	tunnels, err := db.ListTunnels(database)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	caddyfile, err := config.GenerateCaddyfile(vps.Domain, tunnels)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	ratholeConfig, err := config.GenerateRatholeServerConfig(tunnels)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	logs, err := gopherSSH.SetupVPS(vps, caddyfile, ratholeConfig)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error(), "logs": logs})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "logs": logs})
}

func deployVPS(database *sql.DB, w http.ResponseWriter, r *http.Request) {
	vps, err := db.GetVPS(database)
	if err != nil || vps == nil {
		writeError(w, 400, "VPS not configured")
		return
	}
	tunnels, err := db.ListTunnels(database)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	caddyfile, err := config.GenerateCaddyfile(vps.Domain, tunnels)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	ratholeConfig, err := config.GenerateRatholeServerConfig(tunnels)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	logs, err := gopherSSH.DeployVPSConfig(vps, caddyfile, ratholeConfig)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error(), "logs": logs})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "logs": logs})
}

// --- Machine handlers ---

func listMachines(database *sql.DB, w http.ResponseWriter, r *http.Request) {
	machines, err := db.ListMachines(database)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if machines == nil {
		machines = []db.Machine{}
	}
	writeJSON(w, 200, machines)
}

func getMachine(database *sql.DB, w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(mux.Vars(r)["id"])
	m, err := db.GetMachine(database, id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if m == nil {
		writeError(w, 404, "not found")
		return
	}
	writeJSON(w, 200, m)
}

func createMachine(database *sql.DB, w http.ResponseWriter, r *http.Request) {
	var m db.Machine
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if m.Port == 0 {
		m.Port = 22
	}
	id, err := db.CreateMachine(database, &m)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	m.ID = int(id)
	writeJSON(w, 201, m)
}

func updateMachine(database *sql.DB, w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(mux.Vars(r)["id"])
	var m db.Machine
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	m.ID = id
	if err := db.UpdateMachine(database, &m); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, m)
}

func deleteMachine(database *sql.DB, w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(mux.Vars(r)["id"])
	if err := db.DeleteMachine(database, id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func deployMachine(database *sql.DB, w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(mux.Vars(r)["id"])
	machine, err := db.GetMachine(database, id)
	if err != nil || machine == nil {
		writeError(w, 404, "machine not found")
		return
	}
	vps, err := db.GetVPS(database)
	if err != nil || vps == nil {
		writeError(w, 400, "VPS not configured")
		return
	}
	tunnels, err := db.ListTunnelsByMachine(database, id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	ratholeClientConfig, err := config.GenerateRatholeClientConfig(vps.Host, tunnels)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	logs, err := gopherSSH.DeployClient(machine, vps.Host, tunnels, ratholeClientConfig)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error(), "logs": logs})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "logs": logs})
}

// --- Tunnel handlers ---

func listTunnels(database *sql.DB, w http.ResponseWriter, r *http.Request) {
	tunnels, err := db.ListTunnels(database)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if tunnels == nil {
		tunnels = []db.Tunnel{}
	}
	writeJSON(w, 200, tunnels)
}

func getTunnel(database *sql.DB, w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(mux.Vars(r)["id"])
	t, err := db.GetTunnel(database, id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if t == nil {
		writeError(w, 404, "not found")
		return
	}
	writeJSON(w, 200, t)
}

func createTunnel(database *sql.DB, w http.ResponseWriter, r *http.Request) {
	var t db.Tunnel
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if t.Token == "" {
		t.Token = randomToken()
	}
	if t.LocalHost == "" {
		t.LocalHost = "127.0.0.1"
	}
	t.Enabled = true
	id, err := db.CreateTunnel(database, &t)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	t.ID = int(id)
	writeJSON(w, 201, t)
}

func updateTunnel(database *sql.DB, w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(mux.Vars(r)["id"])
	var t db.Tunnel
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	t.ID = id
	if err := db.UpdateTunnel(database, &t); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, t)
}

func deleteTunnel(database *sql.DB, w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(mux.Vars(r)["id"])
	if err := db.DeleteTunnel(database, id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
