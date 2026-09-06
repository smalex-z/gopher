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
	"github.com/smalex-z/gopher/internal/paths"
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
		noteEdgeURL(ctx)
		return handler(ctx, req)
	}
}

func streamAuthInterceptor(token string) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := authFromContext(ss.Context(), token); err != nil {
			return err
		}
		noteEdgeURL(ss.Context())
		return handler(srv, ss)
	}
}

// noteEdgeURL captures the x-gopher-edge-url metadata the server attaches to
// every call — the public address the agent's dial-home recovery needs (see
// recover.go). Only read AFTER auth: the value is persisted to config.env, so
// it must come from the edge, never from an unauthenticated caller. Persisting
// is async — it shells out to sudo and must not sit in the RPC path.
func noteEdgeURL(ctx context.Context) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return
	}
	vals := md.Get("x-gopher-edge-url")
	if len(vals) == 0 {
		return
	}
	go rememberEdgeURL(vals[0])
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
// tunnel on the machine (rathole reloads its config via inotify, never a
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

// GetNetworkInfo discovers the origin's public (WAN) and private (LAN) IPs
// locally — the same lookups the server used to run over SSH. Kept as its own
// on-demand RPC (not in the status stream) because the WAN lookup makes an
// outbound network call.
func (s *agentServer) GetNetworkInfo(ctx context.Context, _ *agentpb.GetNetworkInfoRequest) (*agentpb.NetworkInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// WAN: opendns first (fast, no HTTP), fall back to ipify over HTTPS.
	wan, _ := exec.CommandContext(ctx, "sh", "-c", // #nosec G204 — fixed command
		`dig +short myip.opendns.com @resolver1.opendns.com 2>/dev/null | head -1 || curl -sf --max-time 5 https://api.ipify.org 2>/dev/null`).Output()
	// LAN: first address from the machine's own NICs.
	lan, _ := exec.CommandContext(ctx, "sh", "-c", `hostname -I 2>/dev/null | awk '{print $1}'`).Output() // #nosec G204 — fixed command
	return &agentpb.NetworkInfo{
		WanIp: strings.TrimSpace(string(wan)),
		LanIp: strings.TrimSpace(string(lan)),
	}, nil
}

// SetManagedKey makes public_key the ONE gopher-managed key in the user's
// authorized_keys: it drops every prior gopher-managed line (comment == marker)
// and writes this one, normalized to "type blob gopher-managed". Operator keys
// are untouched; the file can't accumulate. Bearer-token gated.
func (s *agentServer) SetManagedKey(ctx context.Context, in *agentpb.SetManagedKeyRequest) (*agentpb.SetManagedKeyResponse, error) {
	user := strings.TrimSpace(in.GetUsername())
	pubkey := strings.TrimSpace(in.GetPublicKey())
	if user == "" || pubkey == "" {
		return nil, status.Error(codes.InvalidArgument, "username and public_key required")
	}
	if strings.ContainsAny(pubkey, "\n\r") {
		return nil, status.Error(codes.InvalidArgument, "public_key must be a single line")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// Rebuild authorized_keys = (every non-managed line) + (this key, tagged).
	// Atomic via temp+mv so there's no window where the file is missing keys.
	script := `set -e
u="$1"; pk="$2"; marker="$3"
home=$(getent passwd "$u" | cut -d: -f6)
[ -n "$home" ] || { echo "no home dir for user $u" >&2; exit 2; }
mkdir -p "$home/.ssh"; touch "$home/.ssh/authorized_keys"
chmod 700 "$home/.ssh"; chmod 600 "$home/.ssh/authorized_keys"
ak="$home/.ssh/authorized_keys"
line=$(printf '%s\n' "$pk" | awk -v m="$marker" 'NF>=2 {print $1, $2, m; exit}')
[ -n "$line" ] || { echo "invalid public key" >&2; exit 3; }
grep -v " $marker[[:space:]]*\$" "$ak" > "$ak.tmp" 2>/dev/null || true
printf '%s\n' "$line" >> "$ak.tmp"
mv "$ak.tmp" "$ak"
chown -R "$u:$u" "$home/.ssh"`
	out, err := exec.CommandContext(ctx, "sudo", "-n", "sh", "-c", script, "_", user, pubkey, paths.ManagedKeyMarker).CombinedOutput() // #nosec G204 — args are fixed positions
	if err != nil {
		return nil, status.Errorf(codes.Internal, "set managed key for %s: %v: %s", user, err, strings.TrimSpace(string(out)))
	}
	return &agentpb.SetManagedKeyResponse{}, nil
}

// CheckPorts reports, per requested port, whether the origin has a socket bound
// to it — read from /proc/net, so it's read-only and works identically for TCP
// (state LISTEN) and UDP (any bound socket). This is the definitive
// idle-vs-serving signal the server can't get by probing the rathole port from
// the edge.
func (s *agentServer) CheckPorts(_ context.Context, in *agentpb.CheckPortsRequest) (*agentpb.CheckPortsResponse, error) {
	resp := &agentpb.CheckPortsResponse{}
	for _, q := range in.GetPorts() {
		proto := q.GetProto()
		if proto != "udp" {
			proto = "tcp"
		}
		resp.Ports = append(resp.Ports, &agentpb.PortState{
			Port:      q.GetPort(),
			Proto:     proto,
			Listening: portListening(int(q.GetPort()), proto),
		})
	}
	return resp, nil
}

// portListening scans /proc/net for a socket bound to port. For TCP it requires
// state 0A (LISTEN); for UDP any bound socket counts (UDP has no LISTEN state).
// Checks both IPv4 and IPv6 tables since a service may bind :: / ::1.
func portListening(port int, proto string) bool {
	tcp := proto != "udp"
	files := []string{"/proc/net/udp", "/proc/net/udp6"}
	if tcp {
		files = []string{"/proc/net/tcp", "/proc/net/tcp6"}
	}
	target := fmt.Sprintf("%04X", port) // /proc/net encodes the port as uppercase hex
	for _, f := range files {
		data, err := os.ReadFile(f) // #nosec G304 — fixed procfs paths
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			if i == 0 {
				continue // header row
			}
			fields := strings.Fields(line)
			if len(fields) < 4 {
				continue
			}
			// fields[1] = local_address "IPHEX:PORTHEX"; fields[3] = state.
			la := fields[1]
			colon := strings.LastIndex(la, ":")
			if colon < 0 || la[colon+1:] != target {
				continue
			}
			if !tcp || fields[3] == "0A" { // UDP: any bound socket; TCP: LISTEN only
				return true
			}
		}
	}
	return false
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
