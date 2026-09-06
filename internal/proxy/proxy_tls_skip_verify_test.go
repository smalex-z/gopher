package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/smalex-z/gopher/internal/db"
	"github.com/smalex-z/gopher/internal/proxy"
)

// Regression for a real report: a Proxmox-style tunnel (self-signed HTTPS
// backend, TLSSkipVerify enabled) with password protection ALSO enabled hit
// "too many redirects" right after solving the password gate.
//
// Root cause: gated tunnels (bot and/or password) route through this
// package's forward(), not Caddy's own reverse_proxy directive — which is
// where a TLSSkipVerify tunnel normally gets its tls_insecure_skip_verify
// transport (see local_caddyfile.go's buildTunnelCaddyBlock). forward()
// always spoke plain HTTP to the upstream regardless of TLSSkipVerify, so a
// TLS-only backend (Proxmox's pveproxy, which issues its own http->https
// redirect on a bare HTTP hit) looped forever once the gate finally let the
// request through.
func TestPasswordGateForwardsToSelfSignedUpstream(t *testing.T) {
	initDB(t)

	backendHit := false
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	ensureTestMachine(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	tun := &db.Tunnel{
		ID: "tun-tls-auth", MachineID: testMachineID, Name: "TLS Auth",
		Subdomain: "pve", LocalPort: 8006, RatholePort: extractPort(t, backend.URL),
		RatholeToken: "tok-tls-auth", Protocol: "tcp", Transport: "tcp",
		Private: true, AuthEnabled: true, AuthPasswordHash: string(hash),
		TLSSkipVerify: true,
		Status:        "active", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := db.CreateTunnel(tun); err != nil {
		t.Fatalf("create tunnel: %v", err)
	}

	mw, _ := proxy.NewMiddleware()
	h := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) // "next" — not expected once gated
	}))
	const host = "pve.example.com"

	// Solve the password gate.
	form := url.Values{"password": {"s3cret"}, "redirect": {"/"}}
	req := httptest.NewRequest("POST", "/auth-verify", strings.NewReader(form.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("password post: expected 303, got %d:\n%s", rec.Code, rec.Body.String())
	}
	var authCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "gopher_auth" {
			authCookie = c
		}
	}
	if authCookie == nil {
		t.Fatal("no gopher_auth cookie issued")
	}

	// The request right after solving the gate — this is exactly where the
	// bug manifested: without the fix, forward() dials the TLS-only backend
	// over plain HTTP and gets nothing sane back.
	req = httptest.NewRequest("GET", "/", nil)
	req.Host = host
	req.AddCookie(authCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !backendHit {
		t.Fatalf("self-signed upstream was not reached after the password gate (code %d):\n%s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected the backend's 200 to pass through, got %d:\n%s", rec.Code, rec.Body.String())
	}
}
