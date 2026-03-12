package handlers

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/smalex-z/gopher/internal/api/response"
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
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	if body.Domain == "" {
		response.BadRequest(w, "domain is required")
		return
	}
	h.svc.Install(body.Domain)
	response.Success(w, map[string]string{"message": "install started"})
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

// POST /api/local/generate-ssh-key — generate a new RSA keypair and store it
func (h *LocalHandler) GenerateSSHKey(w http.ResponseWriter, r *http.Request) {
	pubKey, err := h.svc.GenerateSSHKey()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"public_key": strings.TrimSpace(pubKey)})
}

// PUT /api/local/ssh-key — validate and store an uploaded key pair
func (h *LocalHandler) UploadSSHKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PrivateKey string `json:"private_key"`
		PublicKey  string `json:"public_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	if body.PrivateKey == "" || body.PublicKey == "" {
		response.BadRequest(w, "private_key and public_key are required")
		return
	}
	if err := h.svc.SetSSHKey(body.PrivateKey, body.PublicKey); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	pubKey := strings.TrimSpace(body.PublicKey)
	response.Success(w, map[string]string{"message": "SSH key pair saved", "public_key": pubKey})
}

// GET /api/local/ssh-key — download the VPS SSH private key
func (h *LocalHandler) DownloadSSHKey(w http.ResponseWriter, r *http.Request) {
	key, err := h.svc.GetSSHPrivateKey()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if key == "" {
		response.NotFound(w, "no SSH key generated yet; bootstrap at least one machine first")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="gopher_id_rsa"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(key))
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

	host := fmt.Sprintf("router.%s", domain)
	ips, err := net.LookupHost(host)
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
