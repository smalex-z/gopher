package service

import (
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

// Regression for an ACM-prod report: after regenerating backup codes, a user's
// codes "all came up as expired." Root cause was LoginTOTP consuming the
// pending-2FA challenge on ANY attempt, so one wrong/mistyped code burned it
// and every subsequent code — correct or not — failed as "2FA expired." A
// wrong guess must leave the challenge usable for a retry within its window.
func TestLoginTOTP_WrongCodeDoesNotBurnChallenge(t *testing.T) {
	initTestDB(t)
	svc := NewAuthService()

	// Set a password + enroll 2FA.
	if err := svc.Setup("correct-horse-battery"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	secret, _, err := svc.TOTPEnroll()
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	code, _ := totp.GenerateCode(secret, time.Now())
	if _, err := svc.TOTPConfirm(code, "phone", "10.0.0.1"); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	// Password step → pending 2FA challenge.
	res, err := svc.Login("correct-horse-battery", "10.0.0.1")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !res.NeedsTOTP || res.PendingToken == "" {
		t.Fatalf("expected a pending 2FA challenge, got %+v", res)
	}

	// A wrong code fails...
	if _, err := svc.LoginTOTP(res.PendingToken, "000000", "10.0.0.1"); err == nil {
		t.Fatal("expected wrong code to fail")
	}

	// ...but the SAME challenge must still be usable with the correct code.
	// (Old bug: this returned "invalid or expired token" because the wrong
	// attempt above had already deleted the pending entry.)
	good, _ := totp.GenerateCode(secret, time.Now())
	tok, err := svc.LoginTOTP(res.PendingToken, good, "10.0.0.1")
	if err != nil {
		t.Fatalf("correct code after one wrong attempt should succeed, got: %v", err)
	}
	if tok == "" {
		t.Fatal("expected a session token on successful 2FA")
	}

	// And once consumed on success, the challenge is gone (single-use holds).
	if _, err := svc.LoginTOTP(res.PendingToken, good, "10.0.0.1"); err == nil {
		t.Fatal("challenge should be consumed after a successful login")
	}
}
