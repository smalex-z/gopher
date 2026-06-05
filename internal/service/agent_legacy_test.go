package service

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/smalex-z/gopher/internal/db"
)

// A v0.1.0 agent answers the JSON /status endpoint. The new server must parse
// that legacy shape so old agents stay monitored until they're upgraded. The
// wire shape must match AgentStatus's json tags exactly.
func TestStatusViaHTTP_ParsesLegacyAgentShape(t *testing.T) {
	const body = `{"agent_version":"0.1.0","agent_uptime_seconds":42,"restarts_served":1,` +
		`"rathole":{"active":true,"state":"active","substate":"running"},` +
		`"system":{"load_avg_1":0.5,"load_avg_5":0.4,"load_avg_15":0.3,` +
		`"mem_total_kb":1000,"mem_avail_kb":250,"disk_free_bytes":10,"disk_total_bytes":100,` +
		`"hostname":"legacy","kernel":"6.x"},"now":"2026-01-01T00:00:00Z"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer t" {
			t.Errorf("missing/wrong bearer: %q", got)
		}
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	_, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)

	st, err := NewAgentClient(&db.Machine{AgentRemotePort: port, AgentToken: "t"}).statusViaHTTP(context.Background())
	if err != nil {
		t.Fatalf("statusViaHTTP: %v", err)
	}
	if st.AgentVersion != "0.1.0" {
		t.Errorf("agent_version: got %q", st.AgentVersion)
	}
	if !st.Rathole.Active || st.Rathole.State != "active" {
		t.Errorf("rathole not parsed: %+v", st.Rathole)
	}
	if st.System.LoadAvg1 != 0.5 || st.System.MemTotalKB != 1000 || st.System.Hostname != "legacy" {
		t.Errorf("system not parsed: %+v", st.System)
	}
}
