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
