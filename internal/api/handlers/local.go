package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	chi "github.com/go-chi/chi/v5"
	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/db"
	"github.com/smalex-z/gopher/internal/service"
)

type LocalHandler struct {
	svc *service.LocalSetupService
}

func NewLocalHandler(svc *service.LocalSetupService) *LocalHandler {
	return &LocalHandler{svc: svc}
}

// GET /api/local/status
func (h *LocalHandler) Status(w http.ResponseWriter, r *http.Request) {
	status, err := h.svc.Status()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, status)
}

// POST /api/local/install
func (h *LocalHandler) Install(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Domain     string `json:"domain"`
		ServerHost string `json:"server_host"`
		SkipCaddy  bool   `json:"skip_caddy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	if !body.SkipCaddy && body.Domain == "" {
		response.BadRequest(w, "domain is required")
		return
	}
	if body.SkipCaddy && body.ServerHost == "" {
		response.BadRequest(w, "server_host is required when skipping Caddy")
		return
	}
	h.svc.Install(body.Domain, body.ServerHost, body.SkipCaddy)
	response.Success(w, map[string]string{"message": "install started"})
}

// POST /api/local/setup-fail2ban
func (h *LocalHandler) SetupFail2ban(w http.ResponseWriter, r *http.Request) {
	h.svc.SetupFail2ban()
	response.Success(w, map[string]string{"message": "fail2ban setup started"})
}

// POST /api/local/skip
func (h *LocalHandler) Skip(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Domain string `json:"domain"`
	}
	// Ignore decode errors — domain is optional
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := h.svc.Skip(body.Domain); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"message": "skipped"})
}

// POST /api/local/reconcile — rebuild server.toml from DB
func (h *LocalHandler) Reconcile(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.ReconcileServerConfig(); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"message": "server config reconciled"})
}

type sshKeyWithStats struct {
	db.SSHKey
	MachineCount int64 `json:"machine_count"`
}

// GET /api/local/ssh-keys — list all key records with machine counts (no private keys)
func (h *LocalHandler) ListSSHKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.svc.ListSSHKeys()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	result := make([]sshKeyWithStats, len(keys))
	for i, k := range keys {
		count, _ := db.CountMachinesUsingKey(k.ID)
		result[i] = sshKeyWithStats{SSHKey: k, MachineCount: count}
	}
	response.Success(w, result)
}

// POST /api/local/ssh-keys/generate — generate a new key pair
func (h *LocalHandler) GenerateSSHKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"name"`
		SetDefault bool   `json:"set_default"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Name == "" {
		req.Name = "Unnamed key"
	}
	key, err := h.svc.GenerateSSHKey(req.Name, req.SetDefault)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, key)
}

// POST /api/local/ssh-keys/upload — store an uploaded key pair
func (h *LocalHandler) UploadSSHKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"name"`
		PrivateKey string `json:"private_key"`
		PublicKey  string `json:"public_key"`
		SetDefault bool   `json:"set_default"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	if req.PrivateKey == "" || req.PublicKey == "" {
		response.BadRequest(w, "private_key and public_key are required")
		return
	}
	if req.Name == "" {
		req.Name = "Uploaded key"
	}
	key, err := h.svc.AddSSHKey(req.Name, req.PrivateKey, req.PublicKey, req.SetDefault)
	if err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	response.Success(w, key)
}

// DELETE /api/local/ssh-keys/{id} — delete a key
func (h *LocalHandler) DeleteSSHKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.DeleteSSHKey(id); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"message": "key deleted"})
}

// PUT /api/local/ssh-keys/{id}/default — set as default
func (h *LocalHandler) SetDefaultSSHKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.SetDefaultSSHKey(id); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"message": "default key updated"})
}

// GET /api/local/ssh-keys/{id}/download — download private key
func (h *LocalHandler) DownloadSSHKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	key, err := h.svc.DownloadSSHKey(id)
	if err != nil {
		response.NotFound(w, "key not found")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="gopher_id_rsa"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(key))
}

// GET /api/local/resolve-ip?host=X  — resolves a hostname to its first A record.
// Works for plain IPs too (returns them as-is). Used by the network map to show
// the VPS's real public IP when the stored host is a hostname, not a raw IP.
func (h *LocalHandler) ResolveIP(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimSpace(r.URL.Query().Get("host"))
	if host == "" {
		response.BadRequest(w, "host is required")
		return
	}
	if ip := net.ParseIP(host); ip != nil {
		response.Success(w, map[string]string{"ip": ip.String()})
		return
	}
	if len(host) > 253 || !validDomain.MatchString(host) {
		response.BadRequest(w, "invalid host")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil || len(ips) == 0 {
		response.Success(w, map[string]string{"ip": ""})
		return
	}
	response.Success(w, map[string]string{"ip": ips[0]})
}

// GET /api/local/check-dns?domain=example.com
// Public endpoint — called during setup wizard before auth is established.
// Resolves router.DOMAIN to verify the wildcard DNS record is in place.
func (h *LocalHandler) CheckDNS(w http.ResponseWriter, r *http.Request) {
	domain := strings.TrimSpace(r.URL.Query().Get("domain"))
	if domain == "" {
		response.BadRequest(w, "domain is required")
		return
	}

	// Validate domain: only allow valid hostname characters and reasonable length.
	if len(domain) > 253 || !validDomain.MatchString(domain) {
		response.BadRequest(w, "invalid domain")
		return
	}

	host := fmt.Sprintf("router.%s", domain)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil || len(ips) == 0 {
		msg := fmt.Sprintf("DNS lookup for %s returned no results", host)
		if err != nil {
			msg = err.Error()
		}
		response.Success(w, map[string]interface{}{
			"ok":      false,
			"message": msg,
		})
		return
	}

	response.Success(w, map[string]interface{}{
		"ok":          true,
		"resolved_to": ips[0],
		"host":        host,
	})
}

// GET /api/local/detect-ip
// Public — called during setup to auto-populate the VPS hostname field.
// Tries a sequence of well-known IP echo services and returns the first success.
func (h *LocalHandler) DetectIP(w http.ResponseWriter, r *http.Request) {
	services := []string{
		"https://api.ipify.org",
		"https://checkip.amazonaws.com",
		"https://ifconfig.me/ip",
	}
	httpClient := &http.Client{Timeout: 4 * time.Second}
	for _, svc := range services {
		resp, err := httpClient.Get(svc) // #nosec G107 — URL is a hardcoded constant
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			continue
		}
		ip := strings.TrimSpace(string(body))
		if net.ParseIP(ip) != nil {
			response.Success(w, map[string]string{"ip": ip})
			return
		}
	}
	response.InternalError(w, "could not detect public IP")
}

// GET /api/local/firewall/detect
// Public — called from the wizard before auth may be established.
func (h *LocalHandler) DetectFirewall(w http.ResponseWriter, r *http.Request) {
	status := h.svc.FirewallDetect()
	response.Success(w, status)
}

// POST /api/local/firewall/configure
// Body: {"mode": "gopher"|"manual"|"none"}
// For "gopher" mode the takeover is async and streams logs to the log WebSocket.
// For "manual" and "none" the mode is saved synchronously.
func (h *LocalHandler) ConfigureFirewall(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	switch body.Mode {
	case "gopher", "manual", "none":
		// valid
	default:
		response.BadRequest(w, "mode must be one of: gopher, manual, none")
		return
	}
	h.svc.FirewallConfigure(body.Mode)
	response.Success(w, map[string]string{"message": "firewall configuration started"})
}

// PUT /api/local/server-ports — toggle dashboard port visibility
func (h *LocalHandler) SetServerPorts(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DashboardPrivate bool `json:"dashboard_private"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	if err := h.svc.SetDashboardPrivate(body.DashboardPrivate); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]bool{"dashboard_private": body.DashboardPrivate})
}

// validDomain matches a reasonable FQDN: labels of alphanumeric + hyphens separated by dots.
var validDomain = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*$`)
