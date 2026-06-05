package service

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	agentpb "github.com/smalex-z/gopher/internal/agentpb"
	"github.com/smalex-z/gopher/internal/db"
)

// AgentClient is the VPS-side gRPC client for the gopher-agent on a machine.
// It reaches the agent via the rathole back-channel — the bind_addr the rathole
// server holds open at 127.0.0.1:<machine.AgentRemotePort> forwards to the
// agent listening on 127.0.0.1:<machine.AgentLocalPort> on the client.
//
// The transport is cleartext HTTP/2 (gRPC insecure): the tunnel hop is already
// encrypted by rathole's Noise transport. The per-machine bearer token is
// attached to every call as "authorization" metadata via PerRPCCredentials.
//
// Unary methods dial-and-close per call — control traffic is infrequent
// (status polls, config pushes) so a short-lived ClientConn avoids holding open
// connections for machines that may have gone away. WatchStatus holds the conn
// open for the lifetime of the stream.
//
// Network failures, timeouts, and RPC errors are returned as plain errors —
// callers (HealthService, migration UI) decide what to do.
type AgentClient struct {
	machine *db.Machine
}

func NewAgentClient(machine *db.Machine) *AgentClient {
	return &AgentClient{machine: machine}
}

// bearerToken implements credentials.PerRPCCredentials, attaching the
// per-machine token to every RPC. RequireTransportSecurity is false because the
// transport is insecure-over-Noise (see type doc).
type bearerToken struct{ token string }

func (b bearerToken) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + b.token}, nil
}
func (bearerToken) RequireTransportSecurity() bool { return false }

func (c *AgentClient) dial() (*grpc.ClientConn, error) {
	target := fmt.Sprintf("127.0.0.1:%d", c.machine.AgentRemotePort)
	return grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(bearerToken{token: c.machine.AgentToken}),
		// Detect a silently-dropped WatchStatus stream: ping the agent and error
		// out if it doesn't respond, so a dead origin surfaces in ~30s instead of
		// the Recv blocking forever.
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                20 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	)
}

// AgentStatus is the VPS-side view of a status snapshot. Field shape is
// preserved from the previous HTTP/JSON client so existing callers and the
// dashboard are unchanged.
type AgentStatus struct {
	AgentVersion   string `json:"agent_version"`
	AgentUptime    int64  `json:"agent_uptime_seconds"`
	RestartsServed int64  `json:"restarts_served"`
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

func statusFromPB(p *agentpb.StatusInfo) *AgentStatus {
	s := &AgentStatus{
		AgentVersion:   p.GetVersion(),
		AgentUptime:    p.GetAgentUptimeSeconds(),
		RestartsServed: p.GetRestartsServed(),
		Now:            time.Unix(p.GetNowUnix(), 0).UTC(),
	}
	if r := p.GetRathole(); r != nil {
		s.Rathole.Active = r.GetActive()
		s.Rathole.State = r.GetState()
		s.Rathole.Substate = r.GetSubstate()
	}
	if sys := p.GetSystem(); sys != nil {
		s.System.LoadAvg1 = sys.GetLoadAvg_1()
		s.System.LoadAvg5 = sys.GetLoadAvg_5()
		s.System.LoadAvg15 = sys.GetLoadAvg_15()
		s.System.MemTotalKB = sys.GetMemTotalKb()
		s.System.MemAvailKB = sys.GetMemAvailKb()
		s.System.DiskFreeBytes = sys.GetDiskFreeBytes()
		s.System.DiskTotalBytes = sys.GetDiskTotalBytes()
		s.System.Hostname = sys.GetHostname()
		s.System.Kernel = sys.GetKernel()
	}
	return s
}

// Status fetches a one-shot status snapshot from the agent.
func (c *AgentClient) Status(ctx context.Context) (*AgentStatus, error) {
	conn, err := c.dial()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	resp, err := agentpb.NewAgentControlClient(conn).GetStatus(ctx, &agentpb.GetStatusRequest{})
	if err != nil {
		return nil, fmt.Errorf("agent GetStatus: %w", err)
	}
	return statusFromPB(resp), nil
}

// WatchStatus opens a server-streaming status subscription and invokes onUpdate
// for each snapshot until the stream ends (ctx cancellation, agent death, or
// network failure), at which point the terminating error is returned. The
// stream dropping is the signal that the agent/origin is gone.
//
// heartbeat is the requested cadence between snapshots; the agent clamps it to
// a sane range. Pass 0 for the agent default.
func (c *AgentClient) WatchStatus(ctx context.Context, heartbeat time.Duration, onUpdate func(*AgentStatus)) error {
	conn, err := c.dial()
	if err != nil {
		return err
	}
	defer conn.Close()
	stream, err := agentpb.NewAgentControlClient(conn).WatchStatus(ctx, &agentpb.WatchStatusRequest{
		HeartbeatSeconds: uint32(heartbeat / time.Second), // #nosec G115 — clamped agent-side
	})
	if err != nil {
		return fmt.Errorf("agent WatchStatus: %w", err)
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err // includes io.EOF on clean close and the cause on drop
		}
		onUpdate(statusFromPB(msg))
	}
}

// RestartRathole asks the agent to run `systemctl start rathole-client`.
func (c *AgentClient) RestartRathole(ctx context.Context) error {
	conn, err := c.dial()
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := agentpb.NewAgentControlClient(conn).RestartRathole(ctx, &agentpb.RestartRatholeRequest{}); err != nil {
		return fmt.Errorf("agent RestartRathole: %w", err)
	}
	return nil
}

// Version returns the agent build version. Useful for detecting when an agent
// install has completed and is reachable.
func (c *AgentClient) Version(ctx context.Context) (string, error) {
	conn, err := c.dial()
	if err != nil {
		return "", err
	}
	defer conn.Close()
	resp, err := agentpb.NewAgentControlClient(conn).GetVersion(ctx, &agentpb.GetVersionRequest{})
	if err != nil {
		return "", fmt.Errorf("agent GetVersion: %w", err)
	}
	return resp.GetVersion(), nil
}

// ProtocolVersion returns the agent's wire-protocol version — the value the
// server should gate compatibility on (vs. the human-facing semver string).
func (c *AgentClient) ProtocolVersion(ctx context.Context) (uint32, error) {
	conn, err := c.dial()
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	resp, err := agentpb.NewAgentControlClient(conn).GetVersion(ctx, &agentpb.GetVersionRequest{})
	if err != nil {
		return 0, fmt.Errorf("agent GetVersion: %w", err)
	}
	return resp.GetProtocolVersion(), nil
}

// GetRatholeConfig fetches the current /etc/rathole/client.toml from the machine
// via the agent. Replaces an SSH `cat` round-trip.
func (c *AgentClient) GetRatholeConfig(ctx context.Context) (string, error) {
	conn, err := c.dial()
	if err != nil {
		return "", err
	}
	defer conn.Close()
	resp, err := agentpb.NewAgentControlClient(conn).GetRatholeConfig(ctx, &agentpb.GetRatholeConfigRequest{})
	if err != nil {
		return "", fmt.Errorf("agent GetRatholeConfig: %w", err)
	}
	return resp.GetToml(), nil
}

// PutRatholeConfig pushes a new client.toml to the machine. The agent writes it
// in place; rathole's notify watcher reloads on inotify. Replaces an SSH SFTP
// upload + start round-trip.
func (c *AgentClient) PutRatholeConfig(ctx context.Context, content string) error {
	conn, err := c.dial()
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := agentpb.NewAgentControlClient(conn).PutRatholeConfig(ctx, &agentpb.RatholeConfig{Toml: content}); err != nil {
		return fmt.Errorf("agent PutRatholeConfig: %w", err)
	}
	return nil
}

// Uninstall asks the agent to run /usr/local/bin/gopher-uninstall in a detached
// worker. The agent returns as soon as the worker is started — the actual
// cleanup runs after this call completes.
func (c *AgentClient) Uninstall(ctx context.Context) error {
	conn, err := c.dial()
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := agentpb.NewAgentControlClient(conn).Uninstall(ctx, &agentpb.UninstallRequest{}); err != nil {
		return fmt.Errorf("agent Uninstall: %w", err)
	}
	return nil
}
