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
	"io"
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
	mux.HandleFunc("/rathole-config", srv.requireToken(srv.ratholeConfig))
	mux.HandleFunc("/uninstall", srv.requireToken(srv.uninstall))

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

// POST /restart-rathole — recovers a stopped/failed rathole-client unit.
//
// We deliberately use `systemctl start`, not `restart`. start is a no-op on a
// healthy unit and will resurrect a stopped or failed one — `restart` would
// drop every active tunnel on the machine, which is the exact behavior we're
// avoiding everywhere else (see CLAUDE.md: rathole reloads via inotify).
//
// The endpoint name stays "restart-rathole" for API stability with already-
// deployed VPS-side callers.
func (s *server) restartRathole(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// reset-failed clears systemd's failure counter so start can succeed
	// after the unit hit its restart-burst limit. Best-effort; we ignore
	// errors here because the start below is the source of truth.
	_, _ = exec.CommandContext(ctx, "sudo", "-n", "systemctl", "reset-failed", s.cfg.UnitName).CombinedOutput() // #nosec G204

	out, err := exec.CommandContext(ctx, "sudo", "-n", "systemctl", "start", s.cfg.UnitName).CombinedOutput() // #nosec G204
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

// ─── rathole-config push ─────────────────────────────────────────────────────
//
// GET returns the current /etc/rathole/client.toml so the VPS can read-merge-write
// without an SSH session. POST writes a new config in place; rathole's notify
// watcher picks up the change via inotify and reloads without restart.
//
// The agent runs as the SSH user (set in bootstrap), and bootstrap chowns
// /etc/rathole/client.toml to that user, so direct file I/O works without sudo.
// We deliberately do not support a $HOME/.config/rathole/client.toml fallback:
// the bootstrap script always installs system-wide and aborts on sudo failure,
// so a machine running the agent always has the system-wide path.

const (
	clientTomlPath        = "/etc/rathole/client.toml"
	maxRatholeConfigBytes = 1 << 20 // 1 MiB — generous but bounded
)

func (s *server) ratholeConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		data, err := os.ReadFile(clientTomlPath) // #nosec G304 — fixed path
		if err != nil {
			if os.IsNotExist(err) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "client.toml not present at " + clientTomlPath})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(data)
	case http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, maxRatholeConfigBytes+1))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body: " + err.Error()})
			return
		}
		if len(body) > maxRatholeConfigBytes {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "config exceeds 1MiB"})
			return
		}
		if len(body) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty body"})
			return
		}
		if err := writeFilePreservingMode(clientTomlPath, body); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"written": true,
			"bytes":   len(body),
		})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST required"})
	}
}

// POST /uninstall — kicks off a detached worker that runs the on-disk
// /usr/local/bin/gopher-uninstall script and returns 202 immediately.
//
// The worker is in its own session (setsid) so it survives:
//   - the HTTP request finishing
//   - the agent's own death when gopher-uninstall stops gopher-agent
//   - the rathole tunnel collapsing when the VPS reconciles server.toml
//
// We sleep briefly before running the uninstall so the 202 response has time
// to flush back through the tunnel before rathole-client gets stopped. Once
// the uninstall script is running, the VPS doesn't need to be reachable —
// every step is local.
func (s *server) uninstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}

	const uninstallScript = "/usr/local/bin/gopher-uninstall"
	if _, err := os.Stat(uninstallScript); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "uninstall script missing at " + uninstallScript + " — machine may need manual cleanup",
		})
		return
	}

	// Spawn a detached child via setsid so it survives this process being
	// killed by the uninstall script itself. The child sleeps a few seconds
	// to let the 202 response flush, then runs the canonical on-disk
	// uninstall flow with output captured for post-mortem.
	cmd := exec.Command("setsid", "sh", "-c", // #nosec G204 — fixed argv
		"sleep 3; sudo -n "+uninstallScript+" >/tmp/.gopher-uninstall.log 2>&1")
	if err := cmd.Start(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to spawn detached uninstall worker: " + err.Error(),
		})
		return
	}
	// Don't Wait — the child outlives this process. Release the goroutine
	// holding the OS handle so the kernel reaps the child when it eventually
	// exits (after we're already dead, but that's fine: PID 1 inherits it).
	go func() { _ = cmd.Process.Release() }()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"queued":     true,
		"script":     uninstallScript,
		"log":        "/tmp/.gopher-uninstall.log",
		"started_at": time.Now().UTC(),
	})
}

// writeFilePreservingMode overwrites a file's contents while keeping its mode
// and ownership. Uses truncate-write rather than rename-into-place because the
// agent owns the file but not the parent directory (/etc/rathole is
// root-owned), which would block atomic rename.
func writeFilePreservingMode(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o644) // #nosec G304 — caller resolved path
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(content); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", path, err)
	}
	return nil
}
