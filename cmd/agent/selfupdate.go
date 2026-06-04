package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const maxAgentBinaryBytes = 64 << 20 // 64 MiB cap on the downloaded binary

type selfUpdateRequest struct {
	BaseURL string `json:"base_url"` // edge URL the origin can reach, e.g. https://router.example.com
	Version string `json:"version"`  // target version — for logging + same-version no-op
}

// handleSelfUpdate is the stable, bearer-authed HTTP control endpoint that rolls
// the agent forward to the binary the edge serves.
//
// It lives on the plaintext HTTP surface (not the gRPC service) deliberately:
// the upgrade trigger must survive across gRPC protocol changes, so a future
// wire-format break is still self-healing. The agent runs as the gopher user
// (NOPASSWD: ALL — see migrate.sh / bootstrap.sh), so it has the privilege to
// install the new binary and restart its own unit. The server's SSH user does
// NOT have that privilege on the origin, which is why self-update — not an
// SSH push — is the correct mechanism.
func (s *agentServer) handleSelfUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	if !s.httpBearerOK(r) {
		httpJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
		return
	}
	var req selfUpdateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		httpJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
		return
	}
	base := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if base == "" {
		httpJSON(w, http.StatusBadRequest, map[string]string{"error": "base_url required"})
		return
	}
	// No-op if we're already the target — prevents a restart loop if the server
	// and agent briefly disagree about what's current.
	if req.Version != "" && req.Version == agentVersion {
		httpJSON(w, http.StatusOK, map[string]any{"updated": false, "reason": "already running " + agentVersion})
		return
	}

	// linux-amd64 / linux-arm64 — matches the filenames scripts/build.sh produces.
	binURL := fmt.Sprintf("%s/static/agents/gopher-agent-linux-%s", base, runtime.GOARCH)

	bin, err := download(binURL, maxAgentBinaryBytes)
	if err != nil {
		httpJSON(w, http.StatusBadGateway, map[string]string{"error": "download binary: " + err.Error()})
		return
	}
	wantSum, err := download(binURL+".sha256", 4096)
	if err != nil {
		httpJSON(w, http.StatusBadGateway, map[string]string{"error": "download checksum: " + err.Error()})
		return
	}
	// The .sha256 is fetched from the same edge over the same channel, so it
	// guards integrity-against-corruption (truncated/half-written downloads —
	// the disk-full class of failure we've hit before), not MITM. The trust
	// root is the edge plus the per-machine bearer token, same model as
	// migrate.sh.
	sum := sha256.Sum256(bin)
	got := hex.EncodeToString(sum[:])
	if want := firstField(string(wantSum)); !strings.EqualFold(want, got) {
		httpJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": fmt.Sprintf("checksum mismatch: got %s want %s", got, want),
		})
		return
	}

	const stagePath = "/tmp/gopher-agent.new"                   // #nosec G101 — not a credential
	if err := os.WriteFile(stagePath, bin, 0o755); err != nil { // #nosec G306 — must be executable
		httpJSON(w, http.StatusInternalServerError, map[string]string{"error": "stage binary: " + err.Error()})
		return
	}

	// Detached worker (setsid) installs the staged binary and restarts the unit.
	// It must outlive this process: `systemctl restart gopher-agent` kills us
	// mid-call. KillMode=process on the unit (see migrate.sh) keeps the detached
	// child alive across the stop. The sleep lets the 202 flush back through the
	// tunnel first. gopher = NOPASSWD: ALL, so sudo -n runs unattended.
	worker := fmt.Sprintf(
		"sleep 2; sudo -n install -m 0755 -o root -g root %s /usr/local/bin/gopher-agent && sudo -n systemctl restart gopher-agent",
		stagePath)
	cmd := exec.Command("setsid", "sh", "-c", worker) // #nosec G204 — fixed argv; stagePath is a constant
	if err := cmd.Start(); err != nil {
		httpJSON(w, http.StatusInternalServerError, map[string]string{"error": "spawn update worker: " + err.Error()})
		return
	}
	go func() { _ = cmd.Process.Release() }()

	httpJSON(w, http.StatusAccepted, map[string]any{"updating": true, "from": agentVersion, "to": req.Version})
}

func (s *agentServer) httpBearerOK(r *http.Request) bool {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.Token)) == 1
}

// download fetches up to max bytes from url. TLS verification is skipped to
// match migrate.sh's curl --insecure (the edge may present an IP/early-boot
// cert); integrity is enforced by the sha256 check on the binary, and the
// trigger itself is bearer-authenticated.
func download(url string, max int64) ([]byte, error) {
	client := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 — see godoc
		},
	}
	resp, err := client.Get(url) // #nosec G107 — url derived from operator-configured edge base URL
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, max))
}

func firstField(s string) string {
	for _, f := range strings.Fields(s) {
		return f
	}
	return ""
}

func httpJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
