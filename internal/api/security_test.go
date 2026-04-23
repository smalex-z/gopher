package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/smalex-z/gopher/internal/db"
	"github.com/smalex-z/gopher/internal/service"
)

func initTestDB(t *testing.T) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	if err := db.Initialize(dsn); err != nil {
		t.Fatalf("initTestDB: %v", err)
	}
}

// newTestRouter builds a minimal router wired to an in-memory DB and real auth service.
func newTestRouter(t *testing.T) (http.Handler, *service.AuthService) {
	t.Helper()
	initTestDB(t)

	authSvc := service.NewAuthService()
	deploySvc := service.NewDeployService()
	vpsSvc := service.NewVPSService(deploySvc)
	machineSvc := service.NewMachineService(deploySvc, nil)
	tunnelSvc := service.NewTunnelService(nil)
	localSvc := service.NewLocalSetupService(deploySvc.Hub)
	bootstrapSvc := service.NewBootstrapService(localSvc)
	updateSvc := service.NewUpdateService()
	secSvc := service.NewSecurityService()

	router := NewRouter(vpsSvc, machineSvc, tunnelSvc, deploySvc, bootstrapSvc, authSvc, localSvc, updateSvc, secSvc)
	return router, authSvc
}

// authenticatedCookie returns a valid session cookie for a freshly set-up auth service.
func authenticatedCookie(t *testing.T, svc *service.AuthService) *http.Cookie {
	t.Helper()
	if err := svc.Setup("testpassword"); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	result, err := svc.Login("testpassword", "127.0.0.1")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	return &http.Cookie{Name: "gopher_session", Value: result.Token}
}

// ---- Unauthenticated access -------------------------------------------------

func TestUnauthenticated_MachinesReturns401(t *testing.T) {
	router, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/machines", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/machines without auth = %d, want 401", w.Code)
	}
}

func TestUnauthenticated_TunnelsReturns401(t *testing.T) {
	router, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/tunnels", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/tunnels without auth = %d, want 401", w.Code)
	}
}

func TestUnauthenticated_VPSReturns401(t *testing.T) {
	router, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/vps", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/vps without auth = %d, want 401", w.Code)
	}
}

func TestUnauthenticated_SecurityReturns401(t *testing.T) {
	router, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/security/logs", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/security/logs without auth = %d, want 401", w.Code)
	}
}

func TestUnauthenticated_UpdateReturns401(t *testing.T) {
	router, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/update/check", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/update/check without auth = %d, want 401", w.Code)
	}
}

func TestUnauthenticated_DebugReturns401(t *testing.T) {
	router, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/debug/caddyfile", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/debug/caddyfile without auth = %d, want 401", w.Code)
	}
}

// ---- Public routes remain accessible ----------------------------------------

func TestPublic_AuthStatusReachable(t *testing.T) {
	router, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code == http.StatusUnauthorized {
		t.Error("GET /api/auth/status should be public (no auth required)")
	}
}

func TestPublic_StatusReachable(t *testing.T) {
	router, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code == http.StatusUnauthorized {
		t.Error("GET /api/status should be public")
	}
}

func TestPublic_LocalStatusReachable(t *testing.T) {
	router, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/local/status", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code == http.StatusUnauthorized {
		t.Error("GET /api/local/status should be public")
	}
}

// ---- Invalid session token is rejected --------------------------------------

func TestInvalidSession_Returns401(t *testing.T) {
	router, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/machines", nil)
	req.AddCookie(&http.Cookie{Name: "gopher_session", Value: "fake-invalid-token"})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("fake session token = %d, want 401", w.Code)
	}
}

// ---- Authenticated access works --------------------------------------------

func TestAuthenticated_MachinesReturns200(t *testing.T) {
	router, authSvc := newTestRouter(t)
	cookie := authenticatedCookie(t, authSvc)

	req := httptest.NewRequest(http.MethodGet, "/api/machines", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("authenticated GET /api/machines = %d, want 200", w.Code)
	}
}

func TestAuthenticated_TunnelsReturns200(t *testing.T) {
	router, authSvc := newTestRouter(t)
	cookie := authenticatedCookie(t, authSvc)

	req := httptest.NewRequest(http.MethodGet, "/api/tunnels", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("authenticated GET /api/tunnels = %d, want 200", w.Code)
	}
}

// ---- CORS headers -----------------------------------------------------------

func TestCORS_OptionsPreflightReturns200(t *testing.T) {
	router, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodOptions, "/api/machines", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", "GET")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK && w.Code != http.StatusNoContent {
		t.Errorf("OPTIONS preflight = %d, want 200/204", w.Code)
	}
}

func TestCORS_AllowedOriginHeader(t *testing.T) {
	router, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	// With AllowedOrigins: ["*"], the header should be present
	if w.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Error("expected Access-Control-Allow-Origin header in response")
	}
}

// ---- Response format --------------------------------------------------------

func TestResponse_UnauthorizedIsJSON(t *testing.T) {
	router, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/machines", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "unauthorized") {
		t.Errorf("body = %q, expected 'unauthorized' message", body)
	}
}

// ---- Logout invalidates session --------------------------------------------

func TestLogout_InvalidatesSession(t *testing.T) {
	router, authSvc := newTestRouter(t)
	cookie := authenticatedCookie(t, authSvc)

	// Logout
	logoutReq := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	logoutReq.AddCookie(cookie)
	lw := httptest.NewRecorder()
	router.ServeHTTP(lw, logoutReq)

	// Now try to use the same token
	req := httptest.NewRequest(http.MethodGet, "/api/machines", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("after logout, session should be invalid: got %d", w.Code)
	}
}
