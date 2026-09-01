package handlers

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/template"

	"github.com/smalex-z/gopher/internal/agentdist"
	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/build"
	"github.com/smalex-z/gopher/internal/db"
	"github.com/smalex-z/gopher/internal/embedbin"
	apperrors "github.com/smalex-z/gopher/internal/errors"
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
		PublicSSH  *bool  `json:"public_ssh"`
		SSHEnabled *bool  `json:"ssh_enabled"`
	}
	// Body is optional; ignore decode errors so a plain POST with no body still works.
	_ = json.NewDecoder(r.Body).Decode(&req)

	// Defaults when unspecified: SSH enabled, publicly reachable. The
	// security-conscious opt into jumpbox-gating or disable SSH entirely.
	sshEnabled := req.SSHEnabled == nil || *req.SSHEnabled
	publicSSH := req.PublicSSH == nil || *req.PublicSSH

	bt, err := h.svc.GenerateToken(req.TunnelPort, req.SSHKeyID, publicSSH, sshEnabled)
	if err != nil {
		var verr *apperrors.ValidationError
		if errors.As(err, &verr) {
			response.BadRequest(w, err.Error())
			return
		}
		response.InternalError(w, err.Error())
		return
	}

	base := hostURL(r)
	bootstrapCmd := fmt.Sprintf("curl -fsSL %s/static/bootstrap.sh | bash -s -- %s", base, bt.Token)
	// Surface the disable as a visible flag so the copied command is honest and
	// CLI users can reproduce it. Register honors --no-ssh authoritatively.
	if !sshEnabled {
		bootstrapCmd += " --no-ssh"
	}

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

// POST /api/agent/recover-config — the agent's dial-home recovery endpoint.
// Public (pre-auth) route, authenticated by the per-machine agent bearer
// token; returns the machine's regenerated managed client.toml as text/plain.
// The audit event is warn-severity on purpose: legitimate dial-home recovery
// is rare, so every one deserves operator eyeballs — and the IP column shows
// who asked.
func (h *BootstrapHandler) RecoverConfig(w http.ResponseWriter, r *http.Request) {
	ip := service.ClientIP(r)
	if !h.svc.AllowAttempt(ip) {
		response.Error(w, http.StatusTooManyRequests, "too many attempts; try again later")
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" || token == r.Header.Get("Authorization") {
		response.Error(w, http.StatusUnauthorized, "bearer token required")
		return
	}
	// Optional body: the agent's current (suspect) config, so custom sections
	// survive the rebuild. 0.2.6/0.2.7 agents send no body — from-scratch.
	current, _ := io.ReadAll(io.LimitReader(r.Body, 256<<10))
	toml, machine, err := h.svc.RecoverClientConfig(token, r.Host, string(current))
	if err != nil {
		if errors.Is(err, service.ErrUnknownAgentToken) {
			response.Error(w, http.StatusUnauthorized, "invalid token")
			return
		}
		response.InternalError(w, "config generation failed")
		return
	}
	db.RecordEvent(&db.Event{
		Severity:     "warn",
		Source:       "machine",
		Kind:         "agent_config_recovered",
		Actor:        "agent",
		ResourceType: "machine",
		ResourceID:   machine.ID,
		ResourceName: machine.Name,
		IP:           ip,
		Message:      fmt.Sprintf("Machine %s recovered client config via dial-home", machine.Name),
	})
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(toml))
}

// GET /static/bootstrap.sh - serve bootstrap script dynamically
func (h *BootstrapHandler) ServeScript(w http.ResponseWriter, r *http.Request) {
	base := hostURL(r)
	script := build.InjectVersions(generateBootstrapScript(base))
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
	if err := tmpl.Execute(&buf, scriptDataFor(hostURL(r))); err != nil {
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

	mt, err := db.ClaimMigrationToken(req.Token)
	if err != nil {
		// Single-use: a re-run after a failed install needs a fresh token
		// (this response hands out the machine's credentials, so tokens
		// must not be replayable inside their TTL). Say so — "expired" alone
		// sends the operator down the wrong debugging path.
		response.BadRequest(w, "invalid, expired, or already-used migration token — generate a new install command from the dashboard")
		return
	}
	machine, err := db.GetMachine(mt.MachineID)
	if err != nil {
		response.InternalError(w, "machine not found for token")
		return
	}

	// noise_pubkey lets migrate.sh ensure the [client.transport] block is
	// present on the machine before adding the agent service. A pre-noise
	// machine whose plaintext client.toml survived the upgrade migration
	// (because it was offline at the time) gets repaired here on the next
	// agent-install pass — the operator clicking "Install Agent" is the
	// natural recovery handle for those stragglers.
	noisePub := ""
	if settings, sErr := db.GetSettings(); sErr == nil && settings != nil {
		noisePub = settings.RatholeNoisePubKey
	}
	response.Success(w, map[string]any{
		"machine_id":    machine.ID,
		"agent_token":   machine.AgentToken,
		"agent_port":    machine.AgentLocalPort,
		"rathole_token": machine.AgentRatholeToken,
		"noise_pubkey":  noisePub,
	})
}

// scriptTemplateData is the render context for bootstrap.sh / migrate.sh.
// Besides the callback URL it carries the authoritative sha256 of every
// binary the script will download from the edge. The script itself is fetched
// by the operator over verified TLS, so hashes embedded in its text are
// trustworthy even though the script's own download steps keep their
// cert-tolerant fallbacks (--insecure retry for old CA bundles). Empty hash
// fields (dev builds without staged binaries) make the scripts fall back to
// the legacy same-channel .sha256 sidecars.
type scriptTemplateData struct {
	HostURL           string
	AgentSHAAmd64     string
	AgentSHAArm64     string
	AgentSHAArmv7     string
	RatholeSHAX8664   string
	RatholeSHAAarch64 string
	RatholeSHAArmv7   string
}

func scriptDataFor(hostURL string) scriptTemplateData {
	a := agentdist.All()
	r := embedbin.RatholeSHA256ByTarget()
	return scriptTemplateData{
		HostURL:           hostURL,
		AgentSHAAmd64:     a["amd64"],
		AgentSHAArm64:     a["arm64"],
		AgentSHAArmv7:     a["armv7"],
		RatholeSHAX8664:   r["x86_64"],
		RatholeSHAAarch64: r["aarch64"],
		RatholeSHAArmv7:   r["armv7"],
	}
}

func generateBootstrapScript(hostURL string) string {
	tmpl, err := template.New("bootstrap").Delims("{{", "}}").Parse(bootstrapScriptTmpl)
	if err != nil {
		// Template is embedded from a known-good file — this should never happen.
		panic("bootstrap template parse error: " + err.Error())
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, scriptDataFor(hostURL)); err != nil {
		panic("bootstrap template execute error: " + err.Error())
	}
	return buf.String()
}
