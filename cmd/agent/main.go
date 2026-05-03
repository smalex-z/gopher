// gopher-agent: a tiny daemon running on every Gopher-managed machine.
//
// Listens on 127.0.0.1:<port> (local-only). The Gopher VPS reaches it through
// the same rathole tunnel that already exists for the SSH back-channel — a
// dedicated service entry is added to rathole-client.toml so VPS can dial
// http://localhost:<remote_port>/... and hit this agent.
//
// All endpoints require a per-machine bearer token (Authorization header).
// The token is generated at install time and known to both sides via the DB.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const agentVersion = "0.1.0"

type config struct {
	Port    int
	Token   string
	UnitName string // systemd unit to manage (default "rathole-client.service")
}

func loadConfig() config {
	c := config{
		Port:     4322,
		Token:    os.Getenv("GOPHER_AGENT_TOKEN"),
		UnitName: "rathole-client.service",
	}
	if p, err := strconv.Atoi(os.Getenv("GOPHER_AGENT_PORT")); err == nil && p > 0 {
		c.Port = p
	}
	if u := os.Getenv("GOPHER_AGENT_UNIT"); u != "" {
		c.UnitName = u
	}
	// Optional config file at /etc/gopher-agent/config.env (KEY=value lines).
	// Useful when systemd EnvironmentFile is preferred over inline Environment=.
	if data, err := os.ReadFile("/etc/gopher-agent/config.env"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			v = strings.Trim(v, "\"' ")
			switch strings.TrimSpace(k) {
			case "GOPHER_AGENT_TOKEN":
				if c.Token == "" {
					c.Token = v
				}
			case "GOPHER_AGENT_PORT":
				if p, err := strconv.Atoi(v); err == nil && p > 0 {
					c.Port = p
				}
			case "GOPHER_AGENT_UNIT":
				if c.UnitName == "" {
					c.UnitName = v
				}
			}
		}
	}
	return c
}

func main() {
	flags := flag.NewFlagSet("gopher-agent", flag.ExitOnError)
	versionFlag := flags.Bool("version", false, "print version and exit")
	_ = flags.Parse(os.Args[1:])

	if *versionFlag {
		fmt.Println(agentVersion)
		return
	}

	cfg := loadConfig()
	if cfg.Token == "" {
		log.Fatal("GOPHER_AGENT_TOKEN is required (env var or /etc/gopher-agent/config.env)")
	}

	srv := &server{cfg: cfg, startedAt: time.Now()}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", srv.healthz) // unauth — for the agent's own systemd healthcheck
	mux.HandleFunc("/status", srv.requireToken(srv.status))
	mux.HandleFunc("/restart-rathole", srv.requireToken(srv.restartRathole))
	mux.HandleFunc("/diagnostics", srv.requireToken(srv.diagnostics))
	mux.HandleFunc("/version", srv.requireToken(srv.version))

	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	log.Printf("gopher-agent %s listening on %s (managing %s)", agentVersion, addr, cfg.UnitName)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server failed: %v", err)
	}
}

type server struct {
	cfg          config
	startedAt    time.Time
	restartCount atomic.Int64
}

func (s *server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing bearer token"})
			return
		}
		got := auth[len(prefix):]
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.Token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
			return
		}
		next(w, r)
	}
}

// GET /healthz — unauth, returns 200 if the agent process is alive.
func (s *server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": agentVersion})
}

// GET /version — bearer-token-protected so VPS can verify it's talking to the right agent.
func (s *server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version": agentVersion,
		"unit":    s.cfg.UnitName,
		"uptime":  int64(time.Since(s.startedAt).Seconds()),
		"arch":    runtime.GOARCH,
	})
}

// GET /status — system + rathole status snapshot.
func (s *server) status(w http.ResponseWriter, _ *http.Request) {
	resp := statusResponse{
		AgentVersion: agentVersion,
		AgentUptime:  int64(time.Since(s.startedAt).Seconds()),
		RestartsServed: s.restartCount.Load(),
		Rathole:      ratholeStatus(s.cfg.UnitName),
		System:       systemStatus(),
		Now:          time.Now().UTC(),
	}
	writeJSON(w, http.StatusOK, resp)
}

// POST /restart-rathole — runs `sudo systemctl restart <unit>`.
func (s *server) restartRathole(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sudo", "-n", "systemctl", "restart", s.cfg.UnitName).CombinedOutput() // #nosec G204
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":  err.Error(),
			"output": string(out),
		})
		return
	}
	s.restartCount.Add(1)
	writeJSON(w, http.StatusOK, map[string]any{
		"restarted": true,
		"output":    strings.TrimSpace(string(out)),
	})
}

// GET /diagnostics — structured pass/fail checks.
func (s *server) diagnostics(w http.ResponseWriter, _ *http.Request) {
	out := []diagCheck{
		runDiag("rathole_unit_active", func() (bool, string) {
			active, detail := unitActive(s.cfg.UnitName)
			return active, detail
		}),
		runDiag("rathole_config_present", func() (bool, string) {
			if _, err := os.Stat("/etc/rathole/client.toml"); err != nil {
				return false, err.Error()
			}
			return true, "/etc/rathole/client.toml"
		}),
		runDiag("disk_space_above_5pct", func() (bool, string) {
			free, total, err := rootDiskSpace()
			if err != nil {
				return false, err.Error()
			}
			pct := float64(free) / float64(total) * 100
			detail := fmt.Sprintf("%.1f%% free (%d / %d bytes)", pct, free, total)
			return pct > 5, detail
		}),
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": out})
}

// ─── status helpers ──────────────────────────────────────────────────────────

type statusResponse struct {
	AgentVersion   string       `json:"agent_version"`
	AgentUptime    int64        `json:"agent_uptime_seconds"`
	RestartsServed int64        `json:"restarts_served"`
	Rathole        ratholeInfo  `json:"rathole"`
	System         systemInfo   `json:"system"`
	Now            time.Time    `json:"now"`
}

type ratholeInfo struct {
	Active   bool   `json:"active"`
	State    string `json:"state"`     // "active", "inactive", "failed", etc.
	Substate string `json:"substate"`  // "running", "dead", etc.
	Detail   string `json:"detail,omitempty"`
}

type systemInfo struct {
	LoadAvg1   float64 `json:"load_avg_1"`
	LoadAvg5   float64 `json:"load_avg_5"`
	LoadAvg15  float64 `json:"load_avg_15"`
	MemTotalKB uint64  `json:"mem_total_kb"`
	MemAvailKB uint64  `json:"mem_avail_kb"`
	DiskFreeBytes  uint64 `json:"disk_free_bytes"`
	DiskTotalBytes uint64 `json:"disk_total_bytes"`
	Hostname   string  `json:"hostname"`
	Kernel     string  `json:"kernel"`
}

type diagCheck struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
}

func runDiag(name string, fn func() (bool, string)) diagCheck {
	pass, detail := fn()
	return diagCheck{Name: name, Pass: pass, Detail: detail}
}

func ratholeStatus(unit string) ratholeInfo {
	state := runProp(unit, "ActiveState")
	substate := runProp(unit, "SubState")
	return ratholeInfo{
		Active:   state == "active",
		State:    state,
		Substate: substate,
	}
}

func unitActive(unit string) (bool, string) {
	state := runProp(unit, "ActiveState")
	substate := runProp(unit, "SubState")
	if state == "active" {
		return true, fmt.Sprintf("%s (%s)", state, substate)
	}
	return false, fmt.Sprintf("%s (%s)", state, substate)
}

func runProp(unit, prop string) string {
	out, err := exec.Command("systemctl", "show", "-p", prop, "--value", unit).Output() // #nosec G204
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func systemStatus() systemInfo {
	info := systemInfo{}
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 {
			info.LoadAvg1, _ = strconv.ParseFloat(fields[0], 64)
			info.LoadAvg5, _ = strconv.ParseFloat(fields[1], 64)
			info.LoadAvg15, _ = strconv.ParseFloat(fields[2], 64)
		}
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			val, _ := strconv.ParseUint(fields[1], 10, 64)
			switch fields[0] {
			case "MemTotal:":
				info.MemTotalKB = val
			case "MemAvailable:":
				info.MemAvailKB = val
			}
		}
	}
	if free, total, err := rootDiskSpace(); err == nil {
		info.DiskFreeBytes = free
		info.DiskTotalBytes = total
	}
	if h, err := os.Hostname(); err == nil {
		info.Hostname = h
	}
	if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		info.Kernel = strings.TrimSpace(string(data))
	}
	return info
}

func rootDiskSpace() (free, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil {
		return 0, 0, err
	}
	bsize := uint64(st.Bsize) // #nosec G115 — Bsize is positive in practice
	return st.Bavail * bsize, st.Blocks * bsize, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
