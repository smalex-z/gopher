package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/smalex-z/gopher/internal/build"
)

func TestServeAPIVersion(t *testing.T) {
	rr := httptest.NewRecorder()
	ServeAPIVersion(rr, httptest.NewRequest(http.MethodGet, "/api/v1/version", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var body struct {
		Data struct {
			ServerVersion   string `json:"server_version"`
			AgentVersion    string `json:"agent_version"`
			ProtocolVersion int    `json:"protocol_version"`
			APIVersion      string `json:"api_version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.ServerVersion != build.Version {
		t.Errorf("server_version = %q, want %q", body.Data.ServerVersion, build.Version)
	}
	if body.Data.AgentVersion != build.AgentVersion {
		t.Errorf("agent_version = %q, want %q", body.Data.AgentVersion, build.AgentVersion)
	}
	if body.Data.ProtocolVersion != build.AgentProtocolVersion {
		t.Errorf("protocol_version = %d, want %d", body.Data.ProtocolVersion, build.AgentProtocolVersion)
	}
	if body.Data.APIVersion != apiVersion {
		t.Errorf("api_version = %q, want %q", body.Data.APIVersion, apiVersion)
	}
}

// Guard against the const and the hand-maintained spec literal drifting apart.
func TestOpenAPISpecVersionMatchesConst(t *testing.T) {
	if !strings.Contains(openAPISpec, `"version": "`+apiVersion+`"`) {
		t.Fatalf("openAPISpec info.version out of sync with apiVersion const %q", apiVersion)
	}
}
