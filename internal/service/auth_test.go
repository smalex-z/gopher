package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---- AuthService: Setup and Login -------------------------------------------

func TestAuthService_SetupAndLogin(t *testing.T) {
	initTestDB(t)
	svc := NewAuthService()

	setup, _ := svc.IsSetup()
	if setup {
		t.Fatal("should not be set up on fresh DB")
	}

	if err := svc.Setup("correctpassword"); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	setup, _ = svc.IsSetup()
	if !setup {
		t.Fatal("should be set up after Setup()")
	}

	result, err := svc.Login("correctpassword", "127.0.0.1")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.Token == "" {
		t.Error("expected non-empty session token")
	}
	if result.NeedsTOTP {
		t.Error("should not need TOTP when not enrolled")
	}
}

func TestAuthService_SetupRejectsShortPassword(t *testing.T) {
	initTestDB(t)
	svc := NewAuthService()
	if err := svc.Setup("short"); err == nil {
		t.Error("expected error for password < 8 chars")
	}
}

func TestAuthService_SetupIdempotentRejection(t *testing.T) {
	initTestDB(t)
	svc := NewAuthService()
	_ = svc.Setup("firstpassword")
	if err := svc.Setup("secondpassword"); err == nil {
		t.Error("expected error when Setup called twice")
	}
}

func TestAuthService_LoginWrongPassword(t *testing.T) {
	initTestDB(t)
	svc := NewAuthService()
	_ = svc.Setup("correctpassword")

	_, err := svc.Login("wrongpassword", "127.0.0.1")
	if err == nil {
		t.Error("expected error for wrong password")
	}
}

func TestAuthService_LoginBeforeSetup(t *testing.T) {
	initTestDB(t)
	svc := NewAuthService()
	_, err := svc.Login("anypassword", "127.0.0.1")
	if err == nil {
		t.Error("expected error when logging in before setup")
	}
}

// ---- Session management -----------------------------------------------------

func TestAuthService_ValidateSession(t *testing.T) {
	initTestDB(t)
	svc := NewAuthService()
	_ = svc.Setup("password123")

	result, _ := svc.Login("password123", "127.0.0.1")
	token := result.Token

	if !svc.ValidateSession(token) {
		t.Error("fresh session should be valid")
	}
}

func TestAuthService_ValidateSession_InvalidToken(t *testing.T) {
	initTestDB(t)
	svc := NewAuthService()
	if svc.ValidateSession("not-a-real-token") {
		t.Error("random token should be invalid")
	}
	if svc.ValidateSession("") {
		t.Error("empty token should be invalid")
	}
}

func TestAuthService_Logout(t *testing.T) {
	initTestDB(t)
	svc := NewAuthService()
	_ = svc.Setup("password123")

	result, _ := svc.Login("password123", "127.0.0.1")
	token := result.Token

	svc.Logout(token)
	if svc.ValidateSession(token) {
		t.Error("token should be invalid after logout")
	}
}

func TestAuthService_LogoutNonexistentToken(t *testing.T) {
	initTestDB(t)
	svc := NewAuthService()
	// Should not panic
	svc.Logout("non-existent-token")
}

// ---- Rate limiter -----------------------------------------------------------

func TestLoginRateLimiter_AllowsUnderLimit(t *testing.T) {
	rl := newLoginRateLimiter()
	for i := 0; i < loginRateLimit; i++ {
		if !rl.record("1.2.3.4") {
			t.Errorf("attempt %d should be allowed", i+1)
		}
	}
}

func TestLoginRateLimiter_BlocksAtLimit(t *testing.T) {
	rl := newLoginRateLimiter()
	for i := 0; i < loginRateLimit; i++ {
		rl.record("1.2.3.4")
	}
	if rl.record("1.2.3.4") {
		t.Error("attempt beyond limit should be blocked")
	}
}

func TestLoginRateLimiter_IsolatesIPs(t *testing.T) {
	rl := newLoginRateLimiter()
	for i := 0; i < loginRateLimit; i++ {
		rl.record("1.2.3.4")
	}
	// Different IP should still be allowed
	if !rl.record("5.6.7.8") {
		t.Error("different IP should not be rate limited")
	}
}

func TestLoginRateLimiter_ResetClearsAttempts(t *testing.T) {
	rl := newLoginRateLimiter()
	for i := 0; i < loginRateLimit; i++ {
		rl.record("1.2.3.4")
	}
	rl.Reset("1.2.3.4")
	if !rl.record("1.2.3.4") {
		t.Error("after reset, IP should be allowed again")
	}
}

// ---- ClientIP ---------------------------------------------------------------

func TestClientIP_FromRemoteAddr(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "192.168.1.1:12345"
	if got := ClientIP(r); got != "192.168.1.1" {
		t.Errorf("ClientIP = %q, want %q", got, "192.168.1.1")
	}
}

func TestClientIP_XForwardedFor(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-For", "10.0.0.1, 10.0.0.2")
	r.RemoteAddr = "127.0.0.1:9999"
	if got := ClientIP(r); got != "10.0.0.1" {
		t.Errorf("ClientIP = %q, want %q (first XFF entry)", got, "10.0.0.1")
	}
}

func TestClientIP_XForwardedForSingle(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.5")
	if got := ClientIP(r); got != "203.0.113.5" {
		t.Errorf("ClientIP = %q, want %q", got, "203.0.113.5")
	}
}

// ---- AuditLog via AuthService -----------------------------------------------

func TestAuthService_AuditLogRecordsEvents(t *testing.T) {
	initTestDB(t)
	svc := NewAuthService()
	_ = svc.Setup("password123")

	_, _ = svc.Login("wrongpassword", "10.0.0.1")
	_, _ = svc.Login("password123", "10.0.0.2")

	log := svc.AuditLog()
	if len(log) < 2 {
		t.Fatalf("expected at least 2 audit events, got %d", len(log))
	}

	events := make([]string, len(log))
	for i, e := range log {
		events[i] = e.Event
	}
	joined := strings.Join(events, ",")

	if !strings.Contains(joined, "LOGIN_FAILED") {
		t.Error("expected LOGIN_FAILED event in audit log")
	}
	if !strings.Contains(joined, "LOGIN_SUCCESS") {
		t.Error("expected LOGIN_SUCCESS event in audit log")
	}
}

// ---- Audit event timing -----------------------------------------------------

func TestAuthService_AuditEventHasTimestampAndIP(t *testing.T) {
	initTestDB(t)
	svc := NewAuthService()
	_ = svc.Setup("password123")

	before := time.Now().Add(-time.Second)
	_, _ = svc.Login("wrongpassword", "1.2.3.4")
	after := time.Now().Add(time.Second)

	log := svc.AuditLog()
	if len(log) == 0 {
		t.Fatal("expected at least one event")
	}
	first := log[0]
	if first.Time.Before(before) || first.Time.After(after) {
		t.Errorf("event time %v not in [%v, %v]", first.Time, before, after)
	}
	if first.IP != "1.2.3.4" {
		t.Errorf("IP = %q, want 1.2.3.4", first.IP)
	}
}

// AuditLog used to be in-memory and lost on restart. With the unified events
// table it now persists. A second AuthService against the same DB must see
// events the first one wrote.
func TestAuthService_AuditLogPersistsAcrossInstances(t *testing.T) {
	initTestDB(t)
	first := NewAuthService()
	_ = first.Setup("password123")
	_, _ = first.Login("wrongpassword", "10.0.0.1")

	// Simulate process restart: throw away the AuthService, build a new one
	// against the same DB.
	second := NewAuthService()
	log := second.AuditLog()
	if len(log) == 0 {
		t.Fatal("expected events to survive restart, got 0")
	}
}
