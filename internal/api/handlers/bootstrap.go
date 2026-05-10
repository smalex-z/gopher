package handlers

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"text/template"

	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/db"
	"github.com/smalex-z/gopher/internal/service"
)

//go:embed templates/bootstrap.sh
var bootstrapScriptTmpl string

//go:embed templates/gopher-uninstall.sh
var gopherUninstallScript string

//go:embed templates/migrate.sh
var migrateScriptTmpl string

type BootstrapHandler struct {
	svc *service.BootstrapService
}

func NewBootstrapHandler(svc *service.BootstrapService) *BootstrapHandler {
	return &BootstrapHandler{svc: svc}
}

// hostURL builds the canonical base URL from settings + request context.
// It uses the configured domain when available so the URL is always correct
// regardless of which subdomain/IP the request arrived on. The scheme is
// detected from the X-Forwarded-Proto header (set by Caddy) and falls back
// to https when a domain is configured.
func hostURL(r *http.Request) string {
	scheme := "https"
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
		scheme = fwd
	} else if r.TLS != nil {
		scheme = "https"
	} else if r.Host == "localhost" || len(r.Host) > 0 && r.Host[0] == '[' {
		scheme = "http"
	}

	if settings, err := db.GetSettings(); err == nil && settings.Domain != "" {
		return fmt.Sprintf("%s://router.%s", scheme, settings.Domain)
	}
	return fmt.Sprintf("%s://%s", scheme, r.Host)
}

// POST /api/bootstrap/token - generate a one-time bootstrap token
func (h *BootstrapHandler) GenerateToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TunnelPort int    `json:"tunnel_port"`
		SSHKeyID   string `json:"ssh_key_id"`
		PublicSSH  bool   `json:"public_ssh"`
	}
	// Body is optional; ignore decode errors so a plain POST with no body still works.
	_ = json.NewDecoder(r.Body).Decode(&req)

	bt, err := h.svc.GenerateToken(req.TunnelPort, req.SSHKeyID, req.PublicSSH)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}

	base := hostURL(r)
	bootstrapCmd := fmt.Sprintf("curl -fsSL %s/static/bootstrap.sh | bash -s -- %s", base, bt.Token)

	response.Success(w, map[string]string{
		"token":             bt.Token,
		"bootstrap_command": bootstrapCmd,
		"expires_at":        bt.ExpiresAt.Format("2006-01-02T15:04:05Z"),
	})
}

// POST /api/bootstrap - called by machines during self-registration
func (h *BootstrapHandler) Register(w http.ResponseWriter, r *http.Request) {
	if !h.svc.AllowAttempt(service.ClientIP(r)) {
		response.Error(w, http.StatusTooManyRequests, "too many bootstrap attempts; try again later")
		return
	}

	var req service.BootstrapRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	if req.Token == "" || req.Name == "" || req.Username == "" {
		response.BadRequest(w, "token, name, and username are required")
		return
	}

	resp, err := h.svc.Register(req, r.Host)
	if err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	response.Success(w, resp)
}

// GET /static/bootstrap.sh - serve bootstrap script dynamically
func (h *BootstrapHandler) ServeScript(w http.ResponseWriter, r *http.Request) {
	base := hostURL(r)
	script := generateBootstrapScript(base)
	w.Header().Set("Content-Type", "text/x-shellscript")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, script)
}

// GET /static/gopher-uninstall.sh - serve the client uninstall script with
// HostURL templated in so the script can call back to the dashboard's
// /api/machines/self-delete endpoint when an operator runs it locally.
func (h *BootstrapHandler) ServeUninstallScript(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.New("uninstall").Delims("{{", "}}").Parse(gopherUninstallScript)
	if err != nil {
		http.Error(w, "uninstall template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, struct{ HostURL string }{HostURL: hostURL(r)}); err != nil {
		http.Error(w, "uninstall template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, buf.String())
}

// GET /static/migrate.sh - serve the migrate script. The script takes a
// migration token as $1 and calls back to POST /api/migrate to fetch the
// per-machine secrets. Mirrors the bootstrap.sh pattern: a token-bearing
// shell script + an API callback that resolves the token to config.
func (h *BootstrapHandler) ServeMigrateScript(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.New("migrate").Delims("{{", "}}").Parse(migrateScriptTmpl)
	if err != nil {
		http.Error(w, "migrate template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, struct{ HostURL string }{HostURL: hostURL(r)}); err != nil {
		http.Error(w, "migrate template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, buf.String())
}

// POST /api/migrate - called by migrate.sh during agent install. The script
// passes the token it was invoked with; we resolve it to a machine and
// return the per-machine secrets the script needs to lay down config files.
//
// Symmetric with POST /api/bootstrap, which is called by bootstrap.sh.
func (h *BootstrapHandler) Migrate(w http.ResponseWriter, r *http.Request) {
	if !h.svc.AllowAttempt(service.ClientIP(r)) {
		response.Error(w, http.StatusTooManyRequests, "too many migrate attempts; try again later")
		return
	}

	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	if req.Token == "" {
		response.BadRequest(w, "token is required")
		return
	}

	mt, err := db.GetMigrationToken(req.Token)
	if err != nil {
		response.BadRequest(w, "invalid or expired migration token")
		return
	}
	machine, err := db.GetMachine(mt.MachineID)
	if err != nil {
		response.InternalError(w, "machine not found for token")
		return
	}

	response.Success(w, map[string]any{
		"machine_id":     machine.ID,
		"agent_token":    machine.AgentToken,
		"agent_port":     machine.AgentLocalPort,
		"rathole_token":  machine.AgentRatholeToken,
	})
}

func generateBootstrapScript(hostURL string) string {
	tmpl, err := template.New("bootstrap").Delims("{{", "}}").Parse(bootstrapScriptTmpl)
	if err != nil {
		// Template is embedded from a known-good file — this should never happen.
		panic("bootstrap template parse error: " + err.Error())
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, struct{ HostURL string }{HostURL: hostURL}); err != nil {
		panic("bootstrap template execute error: " + err.Error())
	}
	return buf.String()
}
