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
		TunnelPort int `json:"tunnel_port"`
	}
	// Body is optional; ignore decode errors so a plain POST with no body still works.
	_ = json.NewDecoder(r.Body).Decode(&req)

	bt, err := h.svc.GenerateToken(req.TunnelPort)
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

// GET /static/gopher-uninstall.sh - serve the client uninstall script
func (h *BootstrapHandler) ServeUninstallScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, gopherUninstallScript)
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
