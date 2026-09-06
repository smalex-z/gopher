package service

import (
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/smalex-z/gopher/internal/db"
)

// Regression for a production freeze: TOTPConfirm called db.CreateTOTPDevice
// (and CountTOTPDevices) on the GLOBAL connection pool from inside a
// MutateSettings transaction. Because the pool is capped at one connection
// (SetMaxOpenConns(1)), that nested call blocked forever waiting for the very
// connection its enclosing transaction already held — a self-deadlock that
// froze all DB access and the entire server. The test DB uses the same
// single-connection config, so this reproduces the hang; the timeout turns a
// regression into a failure instead of a hung suite.
func TestTOTPConfirm_NoDeadlockUnderSingleConnection(t *testing.T) {
	initTestDB(t)
	svc := NewAuthService()

	secret, _, err := svc.TOTPEnroll()
	if err != nil {
		t.Fatalf("TOTPEnroll: %v", err)
	}
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, cErr := svc.TOTPConfirm(code, "my-phone", "203.0.113.7")
		done <- cErr
	}()

	select {
	case cErr := <-done:
		if cErr != nil {
			t.Fatalf("TOTPConfirm returned error: %v", cErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TOTPConfirm DEADLOCKED: a nested global-pool DB call inside its " +
			"MutateSettings transaction froze the single connection — this is the " +
			"exact bug that took a production server down")
	}

	// It must have actually completed the enrollment, not just returned.
	n, err := db.CountTOTPDevices()
	if err != nil {
		t.Fatalf("CountTOTPDevices: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 enrolled device, got %d", n)
	}
	s, err := db.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if !s.TOTPEnabled || s.TOTPSecret != "" {
		t.Fatalf("enrollment not finalized: TOTPEnabled=%v pendingSecret=%q", s.TOTPEnabled, s.TOTPSecret)
	}
}

// The other three mutation paths share the same shape — verify they also run
// to completion under the single-connection pool (each reads devices and/or
// deletes inside the settings transaction).
func TestTOTPMutations_NoDeadlockUnderSingleConnection(t *testing.T) {
	initTestDB(t)
	svc := NewAuthService()

	// Enroll one device so the disable/remove/regenerate paths have state.
	secret, _, err := svc.TOTPEnroll()
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	code, _ := totp.GenerateCode(secret, time.Now())
	if _, err := svc.TOTPConfirm(code, "phone", "203.0.113.7"); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	// RegenerateBackupCodes — reads devices inside the tx.
	withTimeout(t, "TOTPRegenerateBackupCodes", func() error {
		code, _ := totp.GenerateCode(secret, time.Now())
		_, e := svc.TOTPRegenerateBackupCodes(code, "203.0.113.7")
		return e
	})

	// RemoveDevice — reads + deletes inside the tx. Fetch the device id first.
	devices, _ := db.GetTOTPDevices()
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}
	withTimeout(t, "TOTPRemoveDevice", func() error {
		code, _ := totp.GenerateCode(secret, time.Now())
		return svc.TOTPRemoveDevice(devices[0].ID, code, "203.0.113.7")
	})
}

func withTimeout(t *testing.T, name string, fn func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s returned error: %v", name, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s DEADLOCKED under SetMaxOpenConns(1)", name)
	}
}
