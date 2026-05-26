package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// These tests exercise the agent's HTTP surface (auth, methods, body limits)
// without depending on the real /etc/rathole/client.toml location. The on-disk
// read/write path is intentionally not unit-tested here — it's a single
// os.OpenFile call against a hard-coded path, and integration testing it
// requires a real install. Keeping the unit suite hermetic.

func newTestServer() *server {
	return &server{cfg: config{Token: "secret"}}
}

// Regression for the noise-migration bug that corrupted justin-mc:
// writeFilePreservingMode's open(O_TRUNC) succeeded, then the write hit
// ENOSPC and left the file at 0 bytes. The fix is a statfs pre-flight check
// that returns an error before any destructive op. We can't actually fill
// the disk in CI, but we can ask for an impossibly-large write and confirm
// (a) it errors with the expected message, and (b) the existing file is
// untouched.
func TestWriteFilePreservingMode_RefusesWhenInsufficientSpace(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/client.toml"
	original := []byte("original content that must not be lost")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	// Stub the statfs lookup to simulate a near-full disk. Always returns
	// "100 bytes free" regardless of the directory. Restored after the test.
	prev := availableBytes
	defer func() { availableBytes = prev }()
	availableBytes = func(string) (uint64, bool) { return 100, true }

	err := writeFilePreservingMode(path, []byte("content that needs more than 100 bytes plus 8KiB margin"))
	if err == nil {
		t.Fatal("expected error when statfs reports insufficient space; got nil")
	}
	if !strings.Contains(err.Error(), "insufficient disk space") {
		t.Errorf("expected statfs error, got: %v", err)
	}

	// Confirm original content is intact — this is the load-bearing
	// assertion. Without the precheck this file would now be 0 bytes.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Errorf("file corrupted by failed write; got %q, want %q", got, original)
	}
}

func TestWriteFilePreservingMode_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/client.toml"
	if err := os.WriteFile(path, []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	want := []byte("new content\nwith multiple lines\n")
	if err := writeFilePreservingMode(path, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRatholeConfig_RequiresAuth(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/rathole-config", nil)
	w := httptest.NewRecorder()
	srv.requireToken(srv.ratholeConfig)(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestRatholeConfig_RejectsWrongToken(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/rathole-config", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	srv.requireToken(srv.ratholeConfig)(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestRatholeConfig_PostRejectsEmpty(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodPost, "/rathole-config", bytes.NewReader(nil))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	srv.requireToken(srv.ratholeConfig)(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestRatholeConfig_PostRejectsOversize(t *testing.T) {
	srv := newTestServer()
	big := bytes.Repeat([]byte("a"), maxRatholeConfigBytes+10)
	req := httptest.NewRequest(http.MethodPost, "/rathole-config", bytes.NewReader(big))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	srv.requireToken(srv.ratholeConfig)(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestRatholeConfig_RejectsOtherMethods(t *testing.T) {
	srv := newTestServer()
	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/rathole-config", strings.NewReader("x"))
		req.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		srv.requireToken(srv.ratholeConfig)(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: expected 405, got %d", method, w.Code)
		}
	}
}

func TestUninstall_RequiresAuth(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodPost, "/uninstall", nil)
	w := httptest.NewRecorder()
	srv.requireToken(srv.uninstall)(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestUninstall_RejectsGet(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/uninstall", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	srv.requireToken(srv.uninstall)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestUninstall_MissingScriptReturns500(t *testing.T) {
	// If /usr/local/bin/gopher-uninstall is present on the dev box (rare),
	// the handler will spawn a real worker. Skip rather than risk that.
	if _, err := os.Stat("/usr/local/bin/gopher-uninstall"); err == nil {
		t.Skip("/usr/local/bin/gopher-uninstall present on dev box; skipping to avoid spawning a real uninstall")
	}
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodPost, "/uninstall", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	srv.requireToken(srv.uninstall)(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when script absent, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "uninstall script missing") {
		t.Errorf("expected error to mention missing script: %s", w.Body.String())
	}
}

func TestRatholeConfig_GetMissingFileReturns404(t *testing.T) {
	// If the dev box happens to have /etc/rathole/client.toml, the agent
	// will return its real contents — skip rather than emit a misleading
	// failure.
	if _, err := os.Stat(clientTomlPath); err == nil {
		t.Skip("real /etc/rathole/client.toml present; cannot exercise missing-file branch")
	}
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/rathole-config", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	srv.requireToken(srv.ratholeConfig)(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}
