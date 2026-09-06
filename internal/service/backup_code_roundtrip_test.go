package service

import (
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/smalex-z/gopher/internal/db"
)

// Isolates the exact question behind an ACM-prod report: after regenerating
// backup codes, do the plaintext codes handed to the user actually verify
// against what's stored? If this passes, the regenerate+verify logic is sound
// and a "codes don't work" report is a login-flow issue (the pending-token
// burn), not a code-generation bug.
func TestBackupCodes_RegenerateThenVerifyRoundTrip(t *testing.T) {
	initTestDB(t)
	svc := NewAuthService()

	// Enroll a device (enables 2FA + mints the first backup-code set).
	secret, _, err := svc.TOTPEnroll()
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	code, _ := totp.GenerateCode(secret, time.Now())
	if _, err := svc.TOTPConfirm(code, "phone", "203.0.113.7"); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	// Regenerate — this is the exact call the operator used.
	code2, _ := totp.GenerateCode(secret, time.Now())
	plain, err := svc.TOTPRegenerateBackupCodes(code2, "203.0.113.7")
	if err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	if len(plain) == 0 {
		t.Fatal("regenerate returned no codes")
	}

	settings, err := db.GetSettings()
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}

	// Every plaintext code the user was shown must verify against storage —
	// exactly as entered (with dash) and normalized (no dash, any case).
	for i, pc := range plain {
		matched, _, verr := verifyAndConsumeBackupCode(settings.TOTPBackupCodes, pc)
		if verr != nil {
			t.Fatalf("code %d (%q): verify error: %v", i, pc, verr)
		}
		if !matched {
			t.Fatalf("code %d (%q) did NOT verify against stored hashes — regeneration produces codes that can't be used", i, pc)
		}
	}
}
