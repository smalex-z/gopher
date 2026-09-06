package service

import (
	"encoding/json"
	"strings"
	"testing"
)

// AgentStatus is serialized straight to the dashboard (GET machine status). The
// frontend reads snake_case keys (status.system.load_avg_1, etc.). Dropping a
// json tag compiles fine and passes every other test but white-screens the
// dashboard — which already happened once. This locks the wire keys so a future
// tag drop fails CI instead of the UI.
func TestAgentStatus_JSONKeysAreStable(t *testing.T) {
	b, err := json.Marshal(AgentStatus{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(b)
	for _, key := range []string{
		`"agent_version"`, `"agent_uptime_seconds"`, `"restarts_served"`,
		`"rathole"`, `"active"`, `"state"`, `"substate"`,
		`"system"`, `"load_avg_1"`, `"load_avg_5"`, `"load_avg_15"`,
		`"mem_total_kb"`, `"mem_avail_kb"`, `"disk_free_bytes"`, `"disk_total_bytes"`,
		`"hostname"`, `"kernel"`, `"now"`,
	} {
		if !strings.Contains(js, key) {
			t.Errorf("AgentStatus JSON missing %s — the dashboard depends on it. Full: %s", key, js)
		}
	}
}
