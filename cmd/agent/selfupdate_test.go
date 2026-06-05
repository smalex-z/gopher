package main

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testAgentServer() *agentServer {
	return &agentServer{cfg: config{Token: "secret", UnitName: "rathole-client.service"}, startedAt: time.Now()}
}

func postSelfUpdate(t *testing.T, srv *agentServer, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/self-update", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	srv.handleSelfUpdate(w, req)
	return w
}

func TestSelfUpdate_RejectsGet(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/self-update", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	testAgentServer().handleSelfUpdate(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestSelfUpdate_RequiresToken(t *testing.T) {
	if w := postSelfUpdate(t, testAgentServer(), "", `{"base_url":"http://x"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: expected 401, got %d", w.Code)
	}
	if w := postSelfUpdate(t, testAgentServer(), "wrong", `{"base_url":"http://x"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: expected 401, got %d", w.Code)
	}
}

func TestSelfUpdate_MissingBaseURL(t *testing.T) {
	if w := postSelfUpdate(t, testAgentServer(), "secret", `{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestSelfUpdate_SameVersionIsNoop(t *testing.T) {
	// version == agentVersion returns 200 before any download/spawn.
	body := `{"base_url":"http://unused","version":"` + agentVersion + `"}`
	w := postSelfUpdate(t, testAgentServer(), "secret", body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 no-op, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "already running") {
		t.Errorf("expected no-op reason, got %s", w.Body.String())
	}
}

// Checksum mismatch must abort BEFORE staging/spawning the install worker — this
// is the integrity guard. We serve a binary plus a deliberately-wrong sidecar
// and assert 422 (and therefore that the privileged worker is never reached).
func TestSelfUpdate_ChecksumMismatchAborts(t *testing.T) {
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			_, _ = w.Write([]byte("deadbeef  gopher-agent\n")) // wrong hash
			return
		}
		_, _ = w.Write([]byte("fake-binary-bytes"))
	}))
	defer edge.Close()

	body := `{"base_url":"` + edge.URL + `","version":"9.9.9"}`
	w := postSelfUpdate(t, testAgentServer(), "secret", body)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 on checksum mismatch, got %d body=%s", w.Code, w.Body.String())
	}
	_ = runtime.GOARCH // handler builds the per-arch URL; edge serves any path
}
