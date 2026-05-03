package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/db"
)

// AgentClient is the VPS-side counterpart to cmd/agent. It talks to the
// gopher-agent on a machine via the rathole back-channel — the bind_addr the
// rathole server holds open at 127.0.0.1:<machine.AgentRemotePort> forwards to
// the agent listening on 127.0.0.1:<machine.AgentLocalPort> on the client.
//
// All methods are bounded by a per-call context. Network failures, timeouts,
// and non-2xx responses are returned as plain errors — callers (HealthService,
// migration UI) decide what to do.
type AgentClient struct {
	machine *db.Machine
	http    *http.Client
}

func NewAgentClient(machine *db.Machine) *AgentClient {
	return &AgentClient{
		machine: machine,
		http: &http.Client{
			Timeout: 8 * time.Second,
		},
	}
}

func (c *AgentClient) baseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", c.machine.AgentRemotePort)
}

// AgentStatus is the parsed shape of GET /status from the agent.
type AgentStatus struct {
	AgentVersion   string    `json:"agent_version"`
	AgentUptime    int64     `json:"agent_uptime_seconds"`
	RestartsServed int64     `json:"restarts_served"`
	Rathole        struct {
		Active   bool   `json:"active"`
		State    string `json:"state"`
		Substate string `json:"substate"`
	} `json:"rathole"`
	System struct {
		LoadAvg1       float64 `json:"load_avg_1"`
		LoadAvg5       float64 `json:"load_avg_5"`
		LoadAvg15      float64 `json:"load_avg_15"`
		MemTotalKB     uint64  `json:"mem_total_kb"`
		MemAvailKB     uint64  `json:"mem_avail_kb"`
		DiskFreeBytes  uint64  `json:"disk_free_bytes"`
		DiskTotalBytes uint64  `json:"disk_total_bytes"`
		Hostname       string  `json:"hostname"`
		Kernel         string  `json:"kernel"`
	} `json:"system"`
	Now time.Time `json:"now"`
}

func (c *AgentClient) Status(ctx context.Context) (*AgentStatus, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL()+"/status", nil)
	c.authHeader(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("agent status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var s AgentStatus
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, fmt.Errorf("decode status: %w", err)
	}
	return &s, nil
}

// RestartRathole asks the agent to run `systemctl restart rathole-client`.
// Returns nil on success; an error including the agent's stderr on failure.
func (c *AgentClient) RestartRathole(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/restart-rathole", nil)
	c.authHeader(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent restart-rathole %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// AgentVersion returns the version string reported by the agent. Useful for
// detecting when an agent install has completed and is reachable.
func (c *AgentClient) Version(ctx context.Context) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL()+"/version", nil)
	c.authHeader(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("agent version %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", err
	}
	return v.Version, nil
}

func (c *AgentClient) authHeader(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.machine.AgentToken)
}

// GetRatholeConfig fetches the current /etc/rathole/client.toml from the
// machine via the agent's back-channel. Replaces an SSH `cat` round-trip.
func (c *AgentClient) GetRatholeConfig(ctx context.Context) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL()+"/rathole-config", nil)
	c.authHeader(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("agent get rathole-config %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return string(body), nil
}

// PutRatholeConfig pushes a new client.toml to the machine. The agent writes
// it in place; rathole's notify watcher reloads on inotify. Replaces an SSH
// SFTP upload + start round-trip.
func (c *AgentClient) PutRatholeConfig(ctx context.Context, content string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/rathole-config", strings.NewReader(content))
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	c.authHeader(req)
	// Config writes can take a moment if the agent fsyncs; allow a longer
	// timeout than the default 8s status probe.
	httpClient := *c.http
	httpClient.Timeout = 15 * time.Second
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent put rathole-config %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
