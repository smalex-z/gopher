package proxy_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/smalex-z/gopher/internal/db"
	"github.com/smalex-z/gopher/internal/proxy"
)

// initDB creates a fresh temp-file SQLite DB for each test and registers
// cleanup so the file is removed when the test ends.
func initDB(t *testing.T) {
	t.Helper()
	f, err := os.CreateTemp("", "gopher-test-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	if err := db.Initialize(f.Name()); err != nil {
		t.Fatalf("db init: %v", err)
	}
}

const testMachineID = "machine-test"

func ensureTestMachine(t *testing.T) {
	t.Helper()
	// Create a stub machine to satisfy the FK constraint on tunnels.machine_id.
	m := &db.Machine{
		ID:        testMachineID,
		Name:      "Test Machine",
		Username:  "ubuntu",
		Status:    "connected",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	// Ignore duplicate-key errors — machine already exists from a prior call.
	_ = db.CreateMachine(m)
}

func makeTunnel(t *testing.T, id, subdomain string, ratholePort int, botEnabled bool) *db.Tunnel {
	t.Helper()
	ensureTestMachine(t)
	tun := &db.Tunnel{
		ID:                   id,
		MachineID:            testMachineID,
		Name:                 "Test " + id,
		Subdomain:            subdomain,
		LocalPort:            3000,
		RatholePort:          ratholePort,
		RatholeToken:         "tok-" + id,
		Protocol:             "tcp",
		Transport:            "tcp",
		BotProtectionEnabled: botEnabled,
		Status:               "active",
		CreatedAt:            time.Now(),
		UpdatedAt:            time.Now(),
	}
	if err := db.CreateTunnel(tun); err != nil {
		t.Fatalf("create tunnel: %v", err)
	}
	return tun
}

// solvePoW brute-forces a nonce+counter whose SHA-256 starts with `difficulty` zero hex chars.
func solvePoW(nonce string, difficulty int) string {
	prefix := strings.Repeat("0", difficulty)
	for c := 0; ; c++ {
		candidate := fmt.Sprintf("%s:%d", nonce, c)
		sum := sha256.Sum256([]byte(candidate))
		if strings.HasPrefix(hex.EncodeToString(sum[:]), prefix) {
			return fmt.Sprintf("%d", c)
		}
	}
}

// --- tests ---

func TestPassthroughForUnknownHost(t *testing.T) {
	initDB(t)

	mw, err := proxy.NewMiddleware()
	if err != nil {
		t.Fatal(err)
	}

	called := false
	h := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "unknown.example.com"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !called {
		t.Error("next handler should have been called for unknown host")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestPassthroughForUnprotectedTunnel(t *testing.T) {
	initDB(t)
	makeTunnel(t, "tun-open", "open", 20100, false)

	mw, _ := proxy.NewMiddleware()
	called := false
	h := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "open.example.com"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !called {
		t.Error("next handler should have been called for unprotected tunnel")
	}
}

func TestChallengePage_NoCookie(t *testing.T) {
	initDB(t)
	makeTunnel(t, "tun-bot", "protected", 20101, true)

	mw, _ := proxy.NewMiddleware()
	h := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // should not reach here
	}))

	req := httptest.NewRequest("GET", "/dashboard", nil)
	req.Host = "protected.example.com"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 challenge, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "crypto.subtle") {
		t.Error("expected PoW JS in challenge page")
	}
	if !strings.Contains(body, "Verifying your browser") {
		t.Error("expected challenge page heading")
	}
}

func TestAPIClientGets403JSON(t *testing.T) {
	initDB(t)
	makeTunnel(t, "tun-api", "api-protected", 20102, true)

	mw, _ := proxy.NewMiddleware()
	h := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/data", nil)
	req.Host = "api-protected.example.com"
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("expected JSON content-type, got %q", ct)
	}
	if !strings.Contains(rec.Body.String(), `"success":false`) {
		t.Error("expected JSON error body")
	}
}

// Once a browser passes the challenge, its own fetch() calls (which send
// Accept: application/json) must also be forwarded — not blocked.
func TestBrowserFetchWithCookieIsForwarded(t *testing.T) {
	initDB(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	makeTunnel(t, "tun-fetch", "fetchapp", extractPort(t, backend.URL), true)

	mw, _ := proxy.NewMiddleware()
	h := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	// First: get a real cookie by solving PoW.
	req1 := httptest.NewRequest("GET", "/", nil)
	req1.Host = "fetchapp.example.com"
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)
	nonce := extractNonce(t, rec1.Body.String())
	solution := solvePoW(nonce, 5)

	form := url.Values{"nonce": {nonce}, "solution": {solution}, "redirect": {"/"}}
	req2 := httptest.NewRequest("POST", "/bot-verify", strings.NewReader(form.Encode()))
	req2.Host = "fetchapp.example.com"
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	cookies := rec2.Result().Cookies()
	var botCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "gopher_bot_pass" {
			botCookie = c
		}
	}
	if botCookie == nil {
		t.Fatal("no cookie issued")
	}

	// Now make a fetch-style request (Accept: application/json) WITH the cookie.
	req3 := httptest.NewRequest("GET", "/api/items", nil)
	req3.Host = "fetchapp.example.com"
	req3.Header.Set("Accept", "application/json")
	req3.AddCookie(botCookie)
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req3)

	if rec3.Code == http.StatusForbidden {
		t.Error("browser fetch with valid cookie should not be blocked")
	}
}

func TestWebSocketWithoutCookie_Rejected(t *testing.T) {
	initDB(t)
	makeTunnel(t, "tun-ws", "ws-protected", 20103, true)

	mw, _ := proxy.NewMiddleware()
	h := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))

	req := httptest.NewRequest("GET", "/ws", nil)
	req.Host = "ws-protected.example.com"
	req.Header.Set("Upgrade", "websocket")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for websocket without cookie, got %d", rec.Code)
	}
}

func TestIPAllowlist_BypassesChallenge(t *testing.T) {
	initDB(t)
	tun := makeTunnel(t, "tun-allow", "allowlisted", 20104, true)
	tun.BotProtectionAllowIP = `["10.0.0.1"]`
	if err := db.UpdateTunnel(tun); err != nil {
		t.Fatal(err)
	}

	// Start a tiny backend on the rathole port so forward() succeeds.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	backendPort := extractPort(t, backend.URL)
	tun.RatholePort = backendPort
	if err := db.UpdateTunnel(tun); err != nil {
		t.Fatal(err)
	}

	mw, _ := proxy.NewMiddleware()
	h := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) // won't be reached via botmw path
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "allowlisted.example.com"
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Should not get a 403 challenge.
	if rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), "crypto.subtle") {
		t.Error("allowlisted IP should not receive challenge page")
	}
}

func TestFullPoWFlow(t *testing.T) {
	initDB(t)

	// Start a real backend on a dynamic port to act as rathole.
	backendHit := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	tun := makeTunnel(t, "tun-pow", "secured", extractPort(t, backend.URL), true)
	_ = tun

	mw, _ := proxy.NewMiddleware()
	h := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fallback next — not expected to be called for bot-protected tunnels.
		w.WriteHeader(http.StatusTeapot)
	}))

	// --- Step 1: request without cookie → get challenge page ---
	req1 := httptest.NewRequest("GET", "/secret", nil)
	req1.Host = "secured.example.com"
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusForbidden {
		t.Fatalf("step 1: expected 403 challenge, got %d", rec1.Code)
	}

	// Extract the nonce embedded in the page.
	body1 := rec1.Body.String()
	nonce := extractNonce(t, body1)
	t.Logf("nonce from challenge page: %s", nonce)

	// --- Step 2: solve PoW (difficulty 5 = ~1M iterations, fast in Go) ---
	solution := solvePoW(nonce, 5)
	t.Logf("solved: nonce=%s solution=%s", nonce, solution)

	// Verify our solution is actually correct.
	sum := sha256.Sum256([]byte(nonce + ":" + solution))
	hash := hex.EncodeToString(sum[:])
	if !strings.HasPrefix(hash, "00000") {
		t.Fatalf("solvePoW produced wrong answer: hash=%s", hash)
	}

	// --- Step 3: POST /bot-verify ---
	form := url.Values{
		"nonce":    {nonce},
		"solution": {solution},
		"redirect": {"/secret"},
	}
	req2 := httptest.NewRequest("POST", "/bot-verify", strings.NewReader(form.Encode()))
	req2.Host = "secured.example.com"
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusSeeOther {
		t.Fatalf("step 3: expected 303 redirect, got %d\nbody: %s", rec2.Code, rec2.Body.String())
	}
	cookies := rec2.Result().Cookies()
	var botCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "gopher_bot_pass" {
			botCookie = c
			break
		}
	}
	if botCookie == nil {
		t.Fatal("step 3: no gopher_bot_pass cookie in response")
	}
	t.Logf("cookie issued: %s", botCookie.Value[:min(40, len(botCookie.Value))]+"…")

	// --- Step 4: request with valid cookie → forwarded to backend ---
	req3 := httptest.NewRequest("GET", "/secret", nil)
	req3.Host = "secured.example.com"
	req3.AddCookie(botCookie)
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req3)

	// Should NOT be a challenge page.
	if rec3.Code == http.StatusForbidden && strings.Contains(rec3.Body.String(), "crypto.subtle") {
		t.Fatal("step 4: valid cookie still triggered challenge page")
	}
	if !backendHit {
		t.Error("step 4: backend was not called — request was not forwarded")
	}
	t.Logf("step 4: backend response status %d — cookie grants access", rec3.Code)
}

// --- helpers ---

func extractNonce(t *testing.T, html string) string {
	t.Helper()
	const marker = `const nonce="`
	idx := strings.Index(html, marker)
	if idx < 0 {
		t.Fatalf("nonce marker not found in challenge page")
	}
	rest := html[idx+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatal("unterminated nonce in challenge page")
	}
	return rest[:end]
}

func extractPort(t *testing.T, rawURL string) int {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse url %q: %v", rawURL, err)
	}
	var port int
	_, _ = fmt.Sscanf(u.Port(), "%d", &port)
	return port
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
