package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	chi "github.com/go-chi/chi/v5"
	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/db"
	"github.com/smalex-z/gopher/internal/service"
)

type LocalHandler struct {
	svc  *service.LocalSetupService
	auth *service.AuthService // for re-auth on sensitive ops (key download)
}

func NewLocalHandler(svc *service.LocalSetupService, auth *service.AuthService) *LocalHandler {
	return &LocalHandler{svc: svc, auth: auth}
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

// guardSetupOnly rejects the request with 403 when setup has already
// progressed past the point where the endpoint makes sense. Used to lock
// down the public first-run wizard endpoints once the operator has finished
// them — otherwise anyone on the network could re-trigger the install or
// flip firewall mode after setup completes.
//
// step is one of: "install" (blocks once LocalSetupDone is true) or
// "firewall" (blocks once FirewallMode is non-empty).
func guardSetupOnly(w http.ResponseWriter, step string) bool {
	settings, err := db.GetSettings()
	if err != nil {
		response.InternalError(w, "failed to load settings")
		return false
	}
	switch step {
	case "install":
		if settings.LocalSetupDone {
			response.Error(w, http.StatusForbidden, "setup already complete")
			return false
		}
	case "firewall":
		if settings.FirewallMode != "" {
			response.Error(w, http.StatusForbidden, "firewall already configured")
			return false
		}
	}
	return true
}

// POST /api/local/install
func (h *LocalHandler) Install(w http.ResponseWriter, r *http.Request) {
	if !guardSetupOnly(w, "install") {
		return
	}
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
	if err := h.svc.Install(body.Domain, body.ServerHost, body.SkipCaddy); err != nil {
		if errors.Is(err, service.ErrOpInProgress) {
			response.Error(w, http.StatusConflict, err.Error())
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"message": "install started"})
}

// POST /api/local/setup-fail2ban
func (h *LocalHandler) SetupFail2ban(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.SetupFail2ban(); err != nil {
		if errors.Is(err, service.ErrOpInProgress) {
			response.Error(w, http.StatusConflict, err.Error())
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"message": "fail2ban setup started"})
}

// POST /api/local/skip
func (h *LocalHandler) Skip(w http.ResponseWriter, r *http.Request) {
	if !guardSetupOnly(w, "install") {
		return
	}
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

// POST /api/local/reconcile — rebuild server.toml AND every tunnel's
// Caddy file from DB. The latter recovers from any state where a tunnel
// row in the DB has no matching Caddy file on disk (e.g. file was deleted
// out-of-band, or by a buggy orphan sweep on a previous boot).
func (h *LocalHandler) Reconcile(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.ReconcileServerConfig(); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	h.svc.ReconcileTunnelCaddyFiles()
	if err := h.svc.ReconcileAllTunnelCaddyBlocks(); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"message": "server config + caddy blocks reconciled"})
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

// POST /api/local/ssh-keys/{id}/download — download private key.
//
// Gated behind a step-up challenge: the operator must re-prove identity with
// either a TOTP code (if 2FA is enrolled) or the login password (fallback).
// Stops a stolen session cookie alone from exfiltrating the per-machine keys
// the VPS uses to SSH back into clients.
func (h *LocalHandler) DownloadSSHKey(w http.ResponseWriter, r *http.Request) {
	var req service.SensitiveOpChallenge
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}

	ip := service.ClientIP(r)
	if err := h.auth.VerifySensitiveOp(req, ip); err != nil {
		response.Error(w, http.StatusUnauthorized, err.Error())
		return
	}

	id := chi.URLParam(r, "id")
	key, err := h.svc.DownloadSSHKey(id)
	if err != nil {
		response.NotFound(w, "key not found")
		return
	}

	// Audit-log the successful download so the operator can see exactly which
	// key was pulled and from where, alongside the failed attempts (logged
	// inside VerifySensitiveOp).
	h.auth.LogAuditEvent("SSH_KEY_DOWNLOADED", fmt.Sprintf("%s key=%s", ip, id))

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="gopher_id_rsa"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(key))
}

// GET /api/local/ssh-keys/challenge-info — tells the dashboard which credential
// to prompt for in the re-auth modal. "totp" when 2FA is enrolled, otherwise
// "password". No secrets returned.
func (h *LocalHandler) SSHKeyChallengeInfo(w http.ResponseWriter, r *http.Request) {
	req, err := h.auth.SensitiveOpRequirement()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"requires": req})
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
	if !guardSetupOnly(w, "firewall") {
		return
	}
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
	if err := h.svc.FirewallConfigure(body.Mode); err != nil {
		if errors.Is(err, service.ErrOpInProgress) {
			response.Error(w, http.StatusConflict, err.Error())
			return
		}
		response.InternalError(w, err.Error())
		return
	}
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

// PUT /api/local/bind-ip — set (or clear) the bind IP for all public listeners.
// Immediately reconciles rathole and Caddy; HTTP server requires a restart.
func (h *LocalHandler) SetBindIP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BindIP string `json:"bind_ip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	if err := h.svc.SetBindIP(body.BindIP); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"bind_ip": body.BindIP})
}

// Activity backs the dashboard "recent activity" widget. It returns lifecycle
// + health events for machines and tunnels — auth events have their own
// surface (the security page reads them via AuthService.AuditLog).
func (h *LocalHandler) Activity(w http.ResponseWriter, r *http.Request) {
	machineEvents, err := db.GetEvents(db.EventFilter{Source: "machine", Limit: 25})
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	tunnelEvents, err := db.GetEvents(db.EventFilter{Source: "tunnel", Limit: 25})
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	merged := append(machineEvents, tunnelEvents...)
	// Sort newest-first across the two source streams. Each was already
	// sorted, so a stable merge would be cheaper, but n=50 makes the cost
	// noise.
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].CreatedAt.After(merged[j].CreatedAt)
	})
	if len(merged) > 50 {
		merged = merged[:50]
	}
	response.Success(w, merged)
}


// validDomain matches a reasonable FQDN: labels of alphanumeric + hyphens separated by dots.
var validDomain = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*$`)
