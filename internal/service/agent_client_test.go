package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/smalex-z/gopher/internal/db"
)

// agentTestServer wires an httptest.Server to a mock machine such that
// AgentClient.baseURL() points at the test server. The test server's port
// becomes the machine's AgentRemotePort.
func agentTestServer(t *testing.T, handler http.Handler) (*httptest.Server, *db.Machine) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return srv, &db.Machine{
		ID:              "test",
		AgentInstalled:  true,
		AgentRemotePort: port,
		AgentToken:      "test-token",
	}
}

func TestAgentClient_GetRatholeConfig_Success(t *testing.T) {
	const body = "[client]\nremote_addr = \"router:2333\"\n"
	srv, machine := agentTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rathole-config" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("missing/wrong bearer header: %q", got)
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, body)
	}))
	_ = srv

	got, err := NewAgentClient(machine).GetRatholeConfig(context.Background())
	if err != nil {
		t.Fatalf("GetRatholeConfig: %v", err)
	}
	if got != body {
		t.Fatalf("body mismatch: got %q want %q", got, body)
	}
}

func TestAgentClient_GetRatholeConfig_NonOK(t *testing.T) {
	srv, machine := agentTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"no client.toml"}`)
	}))
	_ = srv

	_, err := NewAgentClient(machine).GetRatholeConfig(context.Background())
	if err == nil {
		t.Fatal("expected error on 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404: %v", err)
	}
}

func TestAgentClient_PutRatholeConfig_SendsBodyAndAuth(t *testing.T) {
	const newConfig = "[client]\nremote_addr = \"router:2333\"\n[client.services.tunnel-x]\n"
	var seen string
	srv, machine := agentTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("missing/wrong bearer header: %q", got)
		}
		b, _ := io.ReadAll(r.Body)
		seen = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"written":true}`)
	}))
	_ = srv

	if err := NewAgentClient(machine).PutRatholeConfig(context.Background(), newConfig); err != nil {
		t.Fatalf("PutRatholeConfig: %v", err)
	}
	if seen != newConfig {
		t.Errorf("server got %q, want %q", seen, newConfig)
	}
}

func TestAgentClient_PutRatholeConfig_NonOK(t *testing.T) {
	srv, machine := agentTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"disk full"}`)
	}))
	_ = srv

	err := NewAgentClient(machine).PutRatholeConfig(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if !strings.Contains(err.Error(), "disk full") {
		t.Errorf("error should surface server detail: %v", err)
	}
}

// updateClientTomlViaAgent runs the read-transform-write loop against a real
// HTTP test server, exercising the agent path end-to-end on the VPS side.
func TestUpdateClientTomlViaAgent_HappyPath(t *testing.T) {
	current := "[client]\nremote_addr = \"router:2333\"\n"
	expected := current + "[client.services.tunnel-new]\n"

	var got string
	srv, machine := agentTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, current)
		case http.MethodPost:
			b, _ := io.ReadAll(r.Body)
			got = string(b)
			_, _ = io.WriteString(w, `{"written":true}`)
		}
	}))
	_ = srv

	s := &LocalSetupService{}
	err := s.updateClientTomlViaAgent(machine, func(existing string) (string, error) {
		if existing != current {
			t.Errorf("transform got existing=%q, want %q", existing, current)
		}
		return expected, nil
	})
	if err != nil {
		t.Fatalf("updateClientTomlViaAgent: %v", err)
	}
	if got != expected {
		t.Errorf("agent received %q, want %q", got, expected)
	}
}

func TestUpdateClientTomlViaAgent_NoOpSkipsWrite(t *testing.T) {
	current := "[client]\nremote_addr = \"router:2333\"\n"

	postCalled := false
	srv, machine := agentTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, current)
		case http.MethodPost:
			postCalled = true
			_, _ = io.WriteString(w, `{"written":true}`)
		}
	}))
	_ = srv

	s := &LocalSetupService{}
	err := s.updateClientTomlViaAgent(machine, func(existing string) (string, error) {
		return existing, nil // no change
	})
	if err != nil {
		t.Fatalf("updateClientTomlViaAgent: %v", err)
	}
	if postCalled {
		t.Errorf("expected no POST when transform returns identical content")
	}
}

func TestAgentClient_Uninstall_Accepts202(t *testing.T) {
	srv, machine := agentTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/uninstall" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("missing/wrong bearer header: %q", got)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"queued":true}`)
	}))
	_ = srv

	if err := NewAgentClient(machine).Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
}

func TestAgentClient_Uninstall_BubblesError(t *testing.T) {
	srv, machine := agentTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"missing /usr/local/bin/gopher-uninstall"}`)
	}))
	_ = srv

	err := NewAgentClient(machine).Uninstall(context.Background())
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error should surface server detail: %v", err)
	}
}

// Ensure context cancellation propagates so a stuck agent doesn't block forever.
func TestAgentClient_GetRatholeConfig_RespectsContext(t *testing.T) {
	srv, machine := agentTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	_ = srv

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := NewAgentClient(machine).GetRatholeConfig(ctx)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
