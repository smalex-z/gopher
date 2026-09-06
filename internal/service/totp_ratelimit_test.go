package service

import (
	"strings"
	"testing"
)

// Every 2FA-management endpoint that verifies a 6-digit code must debit the
// per-IP login bucket: an attacker holding a stolen session cookie could
// otherwise brute-force TOTP codes against disable/confirm/regenerate.
// The limiter allows 10 attempts per window; the 11th must be rejected
// before any code verification happens.
func TestTOTPManagement_RateLimited(t *testing.T) {
	initTestDB(t)
	svc := NewAuthService()
	t.Cleanup(func() { svc.rl.Stop() })

	ip := "203.0.113.7"
	for i := 0; i < 10; i++ {
		// 2FA isn't enabled on the fresh DB, so these fail with "not
		// enabled" — but each attempt must still debit the bucket.
		if err := svc.TOTPDisable("000000", ip); err == nil {
			t.Fatal("TOTPDisable should fail on fresh DB")
		}
	}
	err := svc.TOTPDisable("000000", ip)
	if err == nil || !strings.Contains(err.Error(), "too many attempts") {
		t.Fatalf("11th attempt = %v, want rate-limit error", err)
	}

	// The bucket is shared across the management endpoints — a limited IP
	// can't just switch endpoints and keep guessing.
	if _, err := svc.TOTPConfirm("000000", "dev", ip); err == nil || !strings.Contains(err.Error(), "too many attempts") {
		t.Fatalf("TOTPConfirm after limit = %v, want rate-limit error", err)
	}
	if err := svc.TOTPRemoveDevice("dev1", "000000", ip); err == nil || !strings.Contains(err.Error(), "too many attempts") {
		t.Fatalf("TOTPRemoveDevice after limit = %v, want rate-limit error", err)
	}
	if _, err := svc.TOTPRegenerateBackupCodes("000000", ip); err == nil || !strings.Contains(err.Error(), "too many attempts") {
		t.Fatalf("TOTPRegenerateBackupCodes after limit = %v, want rate-limit error", err)
	}

	// A different IP still has a fresh bucket.
	if err := svc.TOTPDisable("000000", "203.0.113.8"); err == nil || strings.Contains(err.Error(), "too many attempts") {
		t.Fatalf("fresh IP = %v, want non-rate-limit failure", err)
	}
}

// secretToken must mint 128-bit (32 hex chars) values — credentials on
// public endpoints don't get to be 64-bit shortTokens.
func TestSecretTokenLength(t *testing.T) {
	a, b := secretToken(), secretToken()
	if len(a) != 32 || len(b) != 32 {
		t.Fatalf("secretToken length = %d/%d, want 32", len(a), len(b))
	}
	if a == b {
		t.Fatal("two secretTokens collided")
	}
}
