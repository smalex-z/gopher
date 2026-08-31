package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Dial-home recovery: the one local failure the watchdog can't repair in
// place — client.toml missing or unrepairable — is fetched fresh from the
// edge, bearer-authed, and written to the active config path.

func withTestEdge(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	edgeURLVal.Store(srv.URL)
	t.Cleanup(func() { edgeURLVal.Store("") })
	return srv
}

func TestRecoverConfigFromEdge_WritesFetchedConfig(t *testing.T) {
	const want = "[client]\nremote_addr = \"edge.example:2333\"\n"
	withTestEdge(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/agent/recover-config" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok123" {
			t.Errorf("Authorization = %q, want bearer tok123", got)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(want))
	})

	dest := filepath.Join(t.TempDir(), "client.toml")
	if err := recoverConfigFromEdge("tok123", dest); err != nil {
		t.Fatalf("recoverConfigFromEdge: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read recovered config: %v", err)
	}
	if string(got) != want {
		t.Errorf("recovered config = %q, want %q", got, want)
	}
	// 0644 is load-bearing: rathole-client.service runs as the bootstrap user,
	// not gopher, so a group-restricted restore leaves rathole crash-looping
	// on EACCES right after a "successful" recovery (field-tested the hard way).
	if fi, err := os.Stat(dest); err == nil && fi.Mode().Perm() != 0o644 {
		t.Errorf("recovered config mode = %o, want 644 (world-readable for the rathole unit user)", fi.Mode().Perm())
	}
}

func TestRecoverConfigFromEdge_RejectedTokenWritesNothing(t *testing.T) {
	withTestEdge(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid token"}`))
	})

	dest := filepath.Join(t.TempDir(), "client.toml")
	if err := recoverConfigFromEdge("wrong", dest); err == nil {
		t.Fatal("expected an error for a rejected token")
	}
	if fileExists(dest) {
		t.Error("a rejected recovery must not write a config file")
	}
}

// A 200 that isn't a client.toml (proxy error page, captive portal) must never
// be installed as rathole config.
func TestRecoverConfigFromEdge_NonTomlResponseRejected(t *testing.T) {
	withTestEdge(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>welcome to the airport wifi</html>"))
	})

	dest := filepath.Join(t.TempDir(), "client.toml")
	if err := recoverConfigFromEdge("tok123", dest); err == nil {
		t.Fatal("expected an error for a non-toml response")
	}
	if fileExists(dest) {
		t.Error("a non-toml response must not be written")
	}
}

// With no edge URL learned yet, recovery fails fast with a clear error rather
// than dialing anything.
func TestRecoverConfigFromEdge_NoEdgeURLFailsFast(t *testing.T) {
	edgeURLVal.Store("")
	dest := filepath.Join(t.TempDir(), "client.toml")
	if err := recoverConfigFromEdge("tok123", dest); err == nil {
		t.Fatal("expected an error when the edge URL is unknown")
	}
}
