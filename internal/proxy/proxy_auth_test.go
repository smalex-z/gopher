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

func makeAuthTunnel(t *testing.T, id, subdomain string, ratholePort int, password string) {
	t.Helper()
	ensureTestMachine(t)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	tun := &db.Tunnel{
		ID: id, MachineID: testMachineID, Name: "Auth " + id,
		Subdomain: subdomain, LocalPort: 3000, RatholePort: ratholePort,
		RatholeToken: "tok-" + id, Protocol: "tcp", Transport: "tcp",
		Private: true, AuthEnabled: true, AuthPasswordHash: string(hash),
		Status: "active", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := db.CreateTunnel(tun); err != nil {
		t.Fatalf("create auth tunnel: %v", err)
	}
}

func TestPasswordGateFlow(t *testing.T) {
	initDB(t)

	backendHit := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	makeAuthTunnel(t, "tun-auth", "gated", extractPort(t, backend.URL), "s3cret")

	mw, _ := proxy.NewMiddleware()
	h := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) // "next" — not expected for gated tunnels
	}))
	const host = "gated.example.com"

	post := func(v url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/auth-verify", strings.NewReader(v.Encode()))
		req.Host = host
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// 1. No cookie → login page (401).
	req := httptest.NewRequest("GET", "/dashboard", nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Password required") {
		t.Fatalf("no cookie: expected 401 login page, got %d:\n%s", rec.Code, rec.Body.String())
	}

	// 2. Wrong password → 401 with error, no cookie.
	rec = post(url.Values{"password": {"nope"}, "redirect": {"/dashboard"}})
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Incorrect password") {
		t.Fatalf("wrong password: expected 401 + error, got %d:\n%s", rec.Code, rec.Body.String())
	}

	// 3. Correct password → 303 redirect + gopher_auth cookie.
	rec = post(url.Values{"password": {"s3cret"}, "redirect": {"/dashboard"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("correct password: expected 303, got %d:\n%s", rec.Code, rec.Body.String())
	}
	var authCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "gopher_auth" {
			authCookie = c
		}
	}
	if authCookie == nil {
		t.Fatal("correct password: no gopher_auth cookie issued")
	}

	// 4. With the cookie → forwarded to the backend.
	req = httptest.NewRequest("GET", "/dashboard", nil)
	req.Host = host
	req.AddCookie(authCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !backendHit {
		t.Fatalf("valid cookie: request was not forwarded (code %d):\n%s", rec.Code, rec.Body.String())
	}

	// 5. Rotate the password → the old cookie is invalidated (its HMAC is bound
	//    to the previous hash), so the same cookie now hits the login page.
	newHash, err := bcrypt.GenerateFromPassword([]byte("rotated"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("rehash: %v", err)
	}
	tun, err := db.GetTunnel("tun-auth")
	if err != nil {
		t.Fatalf("get tunnel: %v", err)
	}
	tun.AuthPasswordHash = string(newHash)
	if err := db.UpdateTunnel(tun); err != nil {
		t.Fatalf("rotate password: %v", err)
	}
	backendHit = false
	req = httptest.NewRequest("GET", "/dashboard", nil)
	req.Host = host
	req.AddCookie(authCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if backendHit {
		t.Fatal("stale cookie after password change: request was forwarded but should have been gated")
	}
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Password required") {
		t.Fatalf("stale cookie after password change: expected 401 login page, got %d:\n%s", rec.Code, rec.Body.String())
	}
}
