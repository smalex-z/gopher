package main

// Dial-home config recovery.
//
// The watchdog (migrate.go) can restart a down unit and de-duplicate a corrupt
// client.toml, but a config that is MISSING or unloadable beyond repair is the
// one local failure it can't fix — and every inbound repair channel (gRPC
// config push, SSH) rides the tunnel that file is the credentials for, so the
// server can't reach in either. The edge, however, is always public, and the
// agent still holds its own identity (GOPHER_AGENT_TOKEN in config.env). So
// the agent dials out: POST <edge>/api/agent/recover-config with the bearer
// token, and the edge regenerates the managed client.toml from its DB — the
// source of truth. Custom user entries in the lost file are not in the DB and
// are not resurrected.
//
// The edge URL is learned three ways, most recent wins:
//   - GOPHER_EDGE_URL in config.env (written at bootstrap for 0.2.6+ installs)
//   - x-gopher-edge-url metadata the server attaches to every gRPC call
//   - base_url of a self-update request
// The latter two persist back to config.env, so a machine bootstrapped before
// GOPHER_EDGE_URL existed becomes recovery-capable on first server contact.

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/smalex-z/gopher/internal/paths"
)

const maxRecoveredConfigBytes = 256 << 10 // generous cap; real configs are a few KiB

var edgeURLVal atomic.Value // string — the edge base URL, e.g. https://router.example.com

func currentEdgeURL() string {
	v, _ := edgeURLVal.Load().(string)
	return v
}

// rememberEdgeURL records a newly-learned edge URL and persists it to
// config.env so it survives agent restarts. No-op when unchanged.
func rememberEdgeURL(u string) {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	if u == "" || u == currentEdgeURL() {
		return
	}
	edgeURLVal.Store(u)
	persistEdgeURL(u)
}

var persistEdgeURLMu sync.Mutex

// persistEdgeURL rewrites config.env with the GOPHER_EDGE_URL line replaced.
// config.env is root:gopher 640, so the write goes through a private temp file
// + `sudo install` — never through a shell line that would put the token (the
// file's other content) into a visible argv.
func persistEdgeURL(url string) {
	persistEdgeURLMu.Lock()
	defer persistEdgeURLMu.Unlock()

	path := paths.AgentConfigEnv
	var lines []string
	if data, err := os.ReadFile(path); err == nil { // #nosec G304 — fixed config path
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "GOPHER_EDGE_URL=") {
				continue
			}
			lines = append(lines, line)
		}
	}
	lines = append(lines, "GOPHER_EDGE_URL="+url)

	tmp, err := os.CreateTemp("", "gopher-agent-env-*")
	if err != nil {
		log.Printf("edge URL persist: temp file: %v", err)
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.WriteString(strings.Join(lines, "\n") + "\n")
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		log.Printf("edge URL persist: write temp: %v", err)
		return
	}
	if err := sudo("install", "-m", "640", "-o", "root", "-g", "gopher", tmpPath, path); err != nil {
		log.Printf("edge URL persist: install %s: %v", path, err)
		return
	}
	log.Printf("edge URL persisted: %s", url)
}

// recoverConfigFromEdge fetches the authoritative managed client.toml from the
// edge and writes it to destPath. TLS IS verified here, unlike the self-update
// binary download: this request carries the machine's identity token and the
// response is tunnel credentials, so both directions need an authenticated
// channel — the sha256-integrity model that excuses the insecure binary fetch
// doesn't apply.
func recoverConfigFromEdge(token, destPath string) error {
	base := currentEdgeURL()
	if base == "" {
		return fmt.Errorf("edge URL unknown — set GOPHER_EDGE_URL in config.env, or it will be learned on next server contact")
	}
	if token == "" {
		return fmt.Errorf("no agent token")
	}

	req, err := http.NewRequest(http.MethodPost, base+"/api/agent/recover-config", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("dial edge: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRecoveredConfigBytes))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("edge returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if !strings.Contains(string(body), "[client]") {
		return fmt.Errorf("response does not look like a client.toml (%d bytes)", len(body))
	}
	return writeRecoveredConfig(destPath, body)
}

// writeRecoveredConfig lands the fetched config at path. The plain write
// covers the normal case (agent owns the file/directory from bootstrap); the
// sudo fallback covers a deleted or root-owned parent directory.
//
// Mode MUST be 0644, matching bootstrap.sh: rathole-client.service runs as
// the bootstrap user (User=<username>), not as gopher, so a group-restricted
// 0640 gopher:gopher file leaves rathole crash-looping on "Permission denied"
// right after a successful restore — which is exactly what happened on the
// first field test of this feature.
func writeRecoveredConfig(path string, body []byte) error {
	if fileExists(path) {
		return writeFilePreservingMode(path, body)
	}
	if err := os.WriteFile(path, body, 0o644); err == nil { // #nosec G306 — must be world-readable, see above
		return nil
	}
	tmp, err := os.CreateTemp("", "gopher-client-toml-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(body)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := sudo("mkdir", "-p", filepath.Dir(path)); err != nil {
		return fmt.Errorf("recreate %s: %w", filepath.Dir(path), err)
	}
	if err := sudo("install", "-m", "644", "-o", "gopher", "-g", "gopher", tmpPath, path); err != nil {
		return fmt.Errorf("install %s: %w", path, err)
	}
	return nil
}
