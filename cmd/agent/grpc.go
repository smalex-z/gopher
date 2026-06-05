package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	agentpb "github.com/smalex-z/gopher/internal/agentpb"
)

// runCommand runs a command and returns combined output. Hoisted so the system
// helpers and RPC methods share one exec path.
func runCommand(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput() // #nosec G204 — fixed argv from callers
	return string(out), err
}

// ─── auth interceptors ───────────────────────────────────────────────────────

func authFromContext(ctx context.Context, token string) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing metadata")
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return status.Error(codes.Unauthenticated, "missing bearer token")
	}
	got := strings.TrimPrefix(vals[0], "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
		return status.Error(codes.Unauthenticated, "invalid token")
	}
	return nil
}

func unaryAuthInterceptor(token string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := authFromContext(ctx, token); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

func streamAuthInterceptor(token string) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := authFromContext(ss.Context(), token); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}

// ─── AgentControl RPCs ───────────────────────────────────────────────────────

func (s *agentServer) GetVersion(_ context.Context, _ *agentpb.GetVersionRequest) (*agentpb.VersionInfo, error) {
	return &agentpb.VersionInfo{
		Version:         agentVersion,
		ProtocolVersion: protocolVersion,
		Unit:            s.cfg.UnitName,
		UptimeSeconds:   int64(time.Since(s.startedAt).Seconds()),
		Arch:            runtime.GOARCH,
	}, nil
}

func (s *agentServer) GetStatus(_ context.Context, _ *agentpb.GetStatusRequest) (*agentpb.StatusInfo, error) {
	return s.buildStatus(), nil
}

// WatchStatus streams an initial snapshot immediately, then one on every
// heartbeat tick. The server treats the stream dropping as the signal that the
// agent/origin is gone — no polling required.
func (s *agentServer) WatchStatus(req *agentpb.WatchStatusRequest, stream agentpb.AgentControl_WatchStatusServer) error {
	if err := stream.Send(s.buildStatus()); err != nil {
		return err
	}

	hb := time.Duration(req.GetHeartbeatSeconds()) * time.Second
	switch {
	case hb <= 0:
		hb = 15 * time.Second // default
	case hb < 5*time.Second:
		hb = 5 * time.Second // floor — don't let a client hammer us
	case hb > 5*time.Minute:
		hb = 5 * time.Minute // ceiling
	}

	ticker := time.NewTicker(hb)
	defer ticker.Stop()
	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := stream.Send(s.buildStatus()); err != nil {
				return err
			}
		}
	}
}

// RestartRathole runs `systemctl start` (not restart) on the managed unit.
//
// We deliberately use start, not restart: start is a no-op on a healthy unit
// and resurrects a stopped/failed one, whereas restart would drop every active
// tunnel on the machine (see CLAUDE.md: rathole reloads via inotify, never a
// process bounce).
func (s *agentServer) RestartRathole(ctx context.Context, _ *agentpb.RestartRatholeRequest) (*agentpb.RestartRatholeResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// reset-failed clears systemd's failure counter so start can succeed after
	// the unit hit its restart-burst limit. Best-effort; the start below is the
	// source of truth.
	_, _ = exec.CommandContext(ctx, "sudo", "-n", "systemctl", "reset-failed", s.cfg.UnitName).CombinedOutput() // #nosec G204

	out, err := exec.CommandContext(ctx, "sudo", "-n", "systemctl", "start", s.cfg.UnitName).CombinedOutput() // #nosec G204
	if err != nil {
		return nil, status.Errorf(codes.Internal, "systemctl start %s: %v: %s", s.cfg.UnitName, err, strings.TrimSpace(string(out)))
	}
	s.restartCount.Add(1)
	return &agentpb.RestartRatholeResponse{
		Restarted: true,
		Output:    strings.TrimSpace(string(out)),
	}, nil
}

func (s *agentServer) GetRatholeConfig(_ context.Context, _ *agentpb.GetRatholeConfigRequest) (*agentpb.RatholeConfig, error) {
	data, err := os.ReadFile(clientTomlPath) // #nosec G304 — fixed path
	if err != nil {
		if os.IsNotExist(err) {
			return nil, status.Errorf(codes.NotFound, "client.toml not present at %s", clientTomlPath)
		}
		return nil, status.Errorf(codes.Internal, "read %s: %v", clientTomlPath, err)
	}
	return &agentpb.RatholeConfig{Toml: string(data)}, nil
}

func (s *agentServer) PutRatholeConfig(_ context.Context, in *agentpb.RatholeConfig) (*agentpb.PutRatholeConfigResponse, error) {
	body := []byte(in.GetToml())
	if len(body) == 0 {
		return nil, status.Error(codes.InvalidArgument, "empty config")
	}
	if len(body) > maxRatholeConfigBytes {
		return nil, status.Errorf(codes.InvalidArgument, "config exceeds %d bytes", maxRatholeConfigBytes)
	}
	if err := writeFilePreservingMode(clientTomlPath, body); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &agentpb.PutRatholeConfigResponse{Written: true, Bytes: int64(len(body))}, nil
}

func (s *agentServer) Diagnostics(_ context.Context, _ *agentpb.DiagnosticsRequest) (*agentpb.DiagnosticsResponse, error) {
	checks := []diagCheck{
		runDiag("rathole_unit_active", func() (bool, string) {
			return unitActive(s.cfg.UnitName)
		}),
		runDiag("rathole_config_present", func() (bool, string) {
			if _, err := os.Stat(clientTomlPath); err != nil {
				return false, err.Error()
			}
			return true, clientTomlPath
		}),
		runDiag("disk_space_above_5pct", func() (bool, string) {
			free, total, err := rootDiskSpace()
			if err != nil {
				return false, err.Error()
			}
			pct := float64(free) / float64(total) * 100
			return pct > 5, fmt.Sprintf("%.1f%% free (%d / %d bytes)", pct, free, total)
		}),
	}
	out := &agentpb.DiagnosticsResponse{Checks: make([]*agentpb.DiagCheck, 0, len(checks))}
	for _, c := range checks {
		out.Checks = append(out.Checks, &agentpb.DiagCheck{Name: c.Name, Pass: c.Pass, Detail: c.Detail})
	}
	return out, nil
}

// Uninstall kicks off a detached worker that runs the on-disk gopher-uninstall
// script and returns immediately. The worker is in its own session (setsid) so
// it survives the RPC finishing, the agent's own death when gopher-uninstall
// stops gopher-agent, and the rathole tunnel collapsing when the VPS
// reconciles server.toml.
func (s *agentServer) Uninstall(_ context.Context, _ *agentpb.UninstallRequest) (*agentpb.UninstallResponse, error) {
	const uninstallScript = "/usr/local/bin/gopher-uninstall"
	if _, err := os.Stat(uninstallScript); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition,
			"uninstall script missing at %s — machine may need manual cleanup", uninstallScript)
	}

	// Sleep briefly before running so the RPC response flushes back through the
	// tunnel before rathole-client is stopped. Once running, the VPS doesn't
	// need to be reachable — every step is local.
	cmd := exec.Command("setsid", "sh", "-c", // #nosec G204 — fixed argv
		"sleep 3; sudo -n "+uninstallScript+" >/tmp/.gopher-uninstall.log 2>&1")
	if err := cmd.Start(); err != nil {
		return nil, status.Errorf(codes.Internal, "spawn detached uninstall worker: %v", err)
	}
	// Don't Wait — the child outlives this process. Release the OS handle so the
	// kernel reaps the child when it eventually exits (PID 1 inherits it).
	go func() { _ = cmd.Process.Release() }()

	return &agentpb.UninstallResponse{
		Queued: true,
		Script: uninstallScript,
		Log:    "/tmp/.gopher-uninstall.log",
	}, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func (s *agentServer) buildStatus() *agentpb.StatusInfo {
	r := ratholeStatus(s.cfg.UnitName)
	sys := systemStatus()
	return &agentpb.StatusInfo{
		Version:            agentVersion,
		ProtocolVersion:    protocolVersion,
		AgentUptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
		RestartsServed:     s.restartCount.Load(),
		Rathole: &agentpb.RatholeStatus{
			Active:   r.Active,
			State:    r.State,
			Substate: r.Substate,
			Detail:   r.Detail,
		},
		System: &agentpb.SystemStatus{
			LoadAvg_1:      sys.LoadAvg1,
			LoadAvg_5:      sys.LoadAvg5,
			LoadAvg_15:     sys.LoadAvg15,
			MemTotalKb:     sys.MemTotalKB,
			MemAvailKb:     sys.MemAvailKB,
			DiskFreeBytes:  sys.DiskFreeBytes,
			DiskTotalBytes: sys.DiskTotalBytes,
			Hostname:       sys.Hostname,
			Kernel:         sys.Kernel,
		},
		NowUnix: time.Now().Unix(),
	}
}
