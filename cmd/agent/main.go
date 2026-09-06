// gopher-agent: a tiny daemon running on every Gopher-managed machine.
//
// Listens on 127.0.0.1:<port> (local-only). The Gopher VPS reaches it through
// the same rathole tunnel that already exists for the SSH back-channel — a
// dedicated service entry is added to rathole-client.toml so the VPS can dial
// 127.0.0.1:<remote_port> and reach this agent.
//
// The control surface is gRPC (service agent.v1.AgentControl), served over
// cleartext HTTP/2 (h2c): the rathole hop is already Noise-encrypted, so
// wrapping gRPC in TLS again would be redundant. Every RPC requires a
// per-machine bearer token in the "authorization" metadata header, enforced by
// a server-side interceptor.
//
// A tiny plaintext HTTP/1 surface is multiplexed onto the same port via cmux
// and serves only GET /healthz — an unauthenticated liveness/compat anchor for
// the agent's own systemd healthcheck and for a future server that needs to
// detect an incompatible agent before speaking gRPC to it.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/soheilhy/cmux"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"

	agentpb "github.com/smalex-z/gopher/internal/agentpb"
	"github.com/smalex-z/gopher/internal/build"
	"github.com/smalex-z/gopher/internal/paths"
)

const (
	// agentVersion is the agent build version, aliased to build.AgentVersion —
	// see that constant's doc for why this is a single shared value rather
	// than a second copy that has to be bumped in lockstep by hand.
	//
	// Per-version changelog:
	// 0.2.1: consolidate origin config under /etc/gopher (client.toml +
	// config.env), migrated in place on first boot.
	// 0.2.2: add GetNetworkInfo + SetManagedKey RPCs so the server no longer
	// needs an SSH private key for network-info discovery or operator key
	// rotation. SetManagedKey keeps exactly one gopher-managed key in
	// authorized_keys (tagged `gopher-managed`), so the file can't accumulate
	// stale keys. Additive RPCs — older agents return Unimplemented — so
	// protocolVersion is unchanged.
	// 0.2.3: add CheckPorts RPC — reports whether the origin's local service
	// ports are bound (from /proc/net), giving the server a definitive
	// idle-vs-serving signal it can't get by probing the rathole port (needed
	// for UDP tunnels, cleaner for TCP). Additive — protocolVersion unchanged.
	// 0.2.4: rathole-client watchdog — recoverRatholeOnce now detects "active
	// but wedged" (systemd reports the unit running, but its reconnect loop
	// silently died with zero established connections) via hasEstablishedConnection,
	// not just unitActive(). Purely local behavior change, no RPC surface touched
	// — protocolVersion unchanged.
	// 0.2.5: treat a failed systemd state query as indeterminate, not "unit
	// down". A systemd daemon re-exec (nightly unattended-upgrades patching
	// libpam or systemd) makes systemctl unanswerable for ~a second; that blip
	// used to report rathole-client inactive — false machine_degraded on the
	// dashboard — and made the watchdog log a phantom recovery. ActiveState
	// queries now retry across the re-exec window, and the watchdog skips a
	// tick it can't measure. Purely local — protocolVersion unchanged.
	// 0.2.6: dial-home config recovery — a client.toml that is missing or
	// unloadable beyond repair is fetched fresh from the edge's public
	// /api/agent/recover-config (bearer = agent token, TLS verified), since
	// every inbound repair channel rides the tunnel that file is the
	// credentials for. The edge URL comes from GOPHER_EDGE_URL in config.env
	// (written at bootstrap) and is refreshed from the x-gopher-edge-url gRPC
	// metadata the server now attaches to every call, plus self-update's
	// base_url — both persist back to config.env. Additive outbound HTTP only
	// — protocolVersion unchanged.
	// 0.2.7: restored client.toml is written 0644 (was 0640) — the rathole
	// unit runs as the bootstrap user, not gopher, so 0.2.6's group-restricted
	// restore left rathole crash-looping on EACCES right after a successful
	// dial-home (caught on the feature's first field test). protocolVersion
	// unchanged.
	// 0.2.8: generalize dial-home refetch beyond a missing file — two new
	// triggers treat the config itself as the suspect: the unit refusing to
	// stay up across consecutive starts (rathole rejecting a truncated/garbage
	// file), and repeated wedge restarts with no connection ever established
	// (corrupted remote_addr, deleted transport block, snapshot-stale tokens).
	// Refetch now sends the suspect config to the edge so operator custom
	// sections are carried into the rebuild, and leaves a client.toml.bak for
	// diffing what drifted. Pairs with the edge's 5-minute drift sweep, which
	// heals content drift on machines whose back-channel still works.
	// protocolVersion unchanged.
	// 0.2.9: client.toml.bak falls back to sudo install when the parent dir is
	// root-owned (direct create failed in the field — the agent can rewrite
	// the existing gopher-owned inode but not create beside it). Paired with a
	// server-side fix: dial-home custom-section salvage now extracts only
	// non-managed [client.services.*] sections instead of "whatever survives
	// stripping", which was re-appending corruption debris to the rebuilt
	// config and costing an extra refetch cycle. protocolVersion unchanged.
	// 0.2.10: self-update verifies the downloaded binary against a checksum
	// carried IN the bearer-authed trigger body (sha256_by_arch), which rides
	// the noise-encrypted back-channel — closing the MITM window where both
	// the binary and its .sha256 sidecar came over the same
	// TLS-verification-skipped download channel. Sidecar remains the fallback
	// for edges that don't send hashes yet. protocolVersion unchanged.
	agentVersion = build.AgentVersion

	// protocolVersion is the wire-compatibility contract between server and
	// agent. The server gates compatibility on this integer, NOT on the semver
	// string above. Bump it on any breaking change to the gRPC contract.
	// v1 = initial gRPC AgentControl service.
	protocolVersion = 1
)

type config struct {
	Port     int
	Token    string
	UnitName string // systemd unit to manage (default "rathole-client.service")
	EdgeURL  string // public edge base URL for dial-home recovery (see recover.go)
}

func loadConfig() config {
	c := config{
		Port:     4322,
		Token:    os.Getenv("GOPHER_AGENT_TOKEN"),
		EdgeURL:  os.Getenv("GOPHER_EDGE_URL"),
		UnitName: "rathole-client.service",
	}
	if p, err := strconv.Atoi(os.Getenv("GOPHER_AGENT_PORT")); err == nil && p > 0 {
		c.Port = p
	}
	if u := os.Getenv("GOPHER_AGENT_UNIT"); u != "" {
		c.UnitName = u
	}
	// Optional config file (KEY=value lines). Useful when systemd
	// EnvironmentFile is preferred over inline Environment=. Prefer the
	// consolidated /etc/gopher/agent/config.env, falling back to the legacy
	// /etc/gopher-agent/config.env for origins not yet migrated.
	data, err := os.ReadFile(paths.AgentConfigEnv)
	if err != nil {
		data, err = os.ReadFile(paths.LegacyAgentConfigEnv)
	}
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			v = strings.Trim(v, "\"' ")
			switch strings.TrimSpace(k) {
			case "GOPHER_AGENT_TOKEN":
				if c.Token == "" {
					c.Token = v
				}
			case "GOPHER_AGENT_PORT":
				if p, err := strconv.Atoi(v); err == nil && p > 0 {
					c.Port = p
				}
			case "GOPHER_AGENT_UNIT":
				if c.UnitName == "" {
					c.UnitName = v
				}
			case "GOPHER_EDGE_URL":
				if c.EdgeURL == "" {
					c.EdgeURL = v
				}
			}
		}
	}
	return c
}

func main() {
	flags := flag.NewFlagSet("gopher-agent", flag.ExitOnError)
	versionFlag := flags.Bool("version", false, "print version and exit")
	_ = flags.Parse(os.Args[1:])

	if *versionFlag {
		fmt.Println(agentVersion)
		return
	}

	// One-time relocation of legacy origins onto the /etc/gopher layout. Runs
	// before loadConfig so the env file is read from its final location; a no-op
	// once migrated or on a fresh 0.2.1 bootstrap.
	migrateOriginLayout()

	cfg := loadConfig()
	if cfg.Token == "" {
		log.Fatal("GOPHER_AGENT_TOKEN is required (env var or /etc/gopher-agent/config.env)")
	}

	// Seed the dial-home edge URL from config without re-persisting it —
	// rememberEdgeURL is for values learned at runtime (gRPC metadata,
	// self-update), which do get written back to config.env.
	if u := strings.TrimRight(strings.TrimSpace(cfg.EdgeURL), "/"); u != "" {
		edgeURLVal.Store(u)
	}

	// Local self-healing watchdog: keeps rathole-client alive even when its
	// config is broken and the server can't reach in (the control channel rides
	// the very tunnel that's down). Runs independently of the gRPC surface.
	go ratholeRecoveryLoop(cfg)

	srv := &agentServer{cfg: cfg, startedAt: time.Now()}

	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen %s: %v", addr, err)
	}

	// cmux multiplexes gRPC (HTTP/2) and the plaintext /healthz HTTP/1 surface
	// onto the single port the rathole back-channel forwards to.
	m := cmux.New(lis)
	grpcL := m.MatchWithWriters(cmux.HTTP2MatchHeaderFieldSendSettings("content-type", "application/grpc"))
	httpL := m.Match(cmux.Any())

	grpcSrv := grpc.NewServer(
		grpc.UnaryInterceptor(unaryAuthInterceptor(cfg.Token)),
		grpc.StreamInterceptor(streamAuthInterceptor(cfg.Token)),
		// Permit the server's 20s client keepalive pings (default min is 5min,
		// which would GOAWAY the WatchStatus stream with "too_many_pings").
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	agentpb.RegisterAgentControlServer(grpcSrv, srv)
	// Reflection lets `grpcurl` introspect the service for debugging. The auth
	// interceptors apply to reflection too, so it still requires the token.
	reflection.Register(grpcSrv)

	httpSrv := &http.Server{
		Handler:           srv.httpHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// /self-update downloads the new binary (up to 60s) synchronously before
		// writing its response, so the write window must exceed that — otherwise
		// the VPS sees the connection cut and reports a spurious "upgrade failed"
		// even though the agent did update. This server is loopback-only (reached
		// via the rathole back-channel), so a long write timeout poses no DoS risk.
		WriteTimeout: 80 * time.Second,
	}

	go func() {
		if err := grpcSrv.Serve(grpcL); err != nil && !errIsClosed(err) {
			log.Printf("grpc serve: %v", err)
		}
	}()
	go func() {
		if err := httpSrv.Serve(httpL); err != nil && err != http.ErrServerClosed && !errIsClosed(err) {
			log.Printf("http serve: %v", err)
		}
	}()

	log.Printf("gopher-agent %s (protocol v%d) listening on %s (managing %s)", agentVersion, protocolVersion, addr, cfg.UnitName)
	if err := m.Serve(); err != nil && !errIsClosed(err) {
		log.Fatalf("cmux serve: %v", err)
	}
}

func errIsClosed(err error) bool {
	return err != nil && strings.Contains(err.Error(), "use of closed network connection")
}

// httpHandler serves the stable plaintext HTTP surface multiplexed alongside
// gRPC: an unauthenticated /healthz liveness/compat anchor and the bearer-authed
// /self-update trigger. Kept on HTTP (not gRPC) so both survive across gRPC
// protocol changes.
func (s *agentServer) httpHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"ok":true,"version":%q,"protocol_version":%d}`, agentVersion, protocolVersion)
	})
	mux.HandleFunc("/self-update", s.handleSelfUpdate)
	return mux
}

// agentServer implements agent.v1.AgentControl. Operational logic lives in
// grpc.go; this file owns process wiring and the pure system-inspection
// helpers below.
type agentServer struct {
	agentpb.UnimplementedAgentControlServer
	cfg          config
	startedAt    time.Time
	restartCount atomic.Int64
}

// ─── system-inspection helpers (pure; reused by the gRPC methods) ────────────

type ratholeInfo struct {
	Active   bool
	State    string // "active", "inactive", "failed", etc.
	Substate string // "running", "dead", etc.
	Detail   string
}

type systemInfo struct {
	LoadAvg1       float64
	LoadAvg5       float64
	LoadAvg15      float64
	MemTotalKB     uint64
	MemAvailKB     uint64
	DiskFreeBytes  uint64
	DiskTotalBytes uint64
	Hostname       string
	Kernel         string
}

type diagCheck struct {
	Name   string
	Pass   bool
	Detail string
}

func runDiag(name string, fn func() (bool, string)) diagCheck {
	pass, detail := fn()
	return diagCheck{Name: name, Pass: pass, Detail: detail}
}

// State queries retry while systemctl itself fails to answer: during a
// systemd daemon re-exec — routine when nightly unattended-upgrades patches
// libpam or systemd itself — the manager is unreachable for ~a second, and a
// failed *query* must not read as a stopped *unit*. One such blip flipped a
// healthy machine to degraded on the dashboard and made the watchdog log a
// phantom recovery. Vars, not consts, so tests can shrink the window.
var (
	stateQueryRetries    = 3
	stateQueryRetryDelay = time.Second
)

// runPropRetry is runProp for the load-bearing ActiveState query: it retries
// across a systemd re-exec window before giving up, so "unknown" means
// systemd stayed unanswerable for several seconds straight — a real problem —
// never a blip.
func runPropRetry(unit, prop string) string {
	for attempt := 0; ; attempt++ {
		out, err := runCommand("systemctl", "show", "-p", prop, "--value", unit)
		if err == nil {
			return strings.TrimSpace(out)
		}
		if attempt >= stateQueryRetries {
			return "unknown"
		}
		time.Sleep(stateQueryRetryDelay)
	}
}

func ratholeStatus(unit string) ratholeInfo {
	state := runPropRetry(unit, "ActiveState")
	substate := "unknown"
	if state != "unknown" {
		substate = runProp(unit, "SubState")
	}
	return ratholeInfo{
		Active:   state == "active",
		State:    state,
		Substate: substate,
	}
}

func unitActive(unit string) (bool, string) {
	state := runPropRetry(unit, "ActiveState")
	substate := "unknown"
	if state != "unknown" {
		substate = runProp(unit, "SubState")
	}
	return state == "active", fmt.Sprintf("%s (%s)", state, substate)
}

func runProp(unit, prop string) string {
	out, err := runCommand("systemctl", "show", "-p", prop, "--value", unit)
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(out)
}

func systemStatus() systemInfo {
	info := systemInfo{}
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 {
			info.LoadAvg1, _ = strconv.ParseFloat(fields[0], 64)
			info.LoadAvg5, _ = strconv.ParseFloat(fields[1], 64)
			info.LoadAvg15, _ = strconv.ParseFloat(fields[2], 64)
		}
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			val, _ := strconv.ParseUint(fields[1], 10, 64)
			switch fields[0] {
			case "MemTotal:":
				info.MemTotalKB = val
			case "MemAvailable:":
				info.MemAvailKB = val
			}
		}
	}
	if free, total, err := rootDiskSpace(); err == nil {
		info.DiskFreeBytes = free
		info.DiskTotalBytes = total
	}
	if h, err := os.Hostname(); err == nil {
		info.Hostname = h
	}
	if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		info.Kernel = strings.TrimSpace(string(data))
	}
	return info
}

func rootDiskSpace() (free, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil {
		return 0, 0, err
	}
	bsize := uint64(st.Bsize) // #nosec G115 — Bsize is positive in practice
	return st.Bavail * bsize, st.Blocks * bsize, nil
}

// ─── rathole-config push ─────────────────────────────────────────────────────
//
// The agent runs as the gopher user (set in bootstrap), and bootstrap chowns
// the client.toml to that user, so direct file I/O works without sudo.

// clientTomlPath aliases a paths var (test-redirectable), so it is a var too.
var clientTomlPath = paths.RatholeClientConfig

const maxRatholeConfigBytes = 1 << 20 // 1 MiB — generous but bounded

// availableBytes returns the number of bytes free for non-root writes in dir,
// or (0, false) if the syscall isn't supported. Hoisted into a var so tests
// can stub it without needing privileged access to actually fill the disk.
var availableBytes = func(dir string) (uint64, bool) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, false
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), true
}

// writeFilePreservingMode overwrites a file's contents while keeping its mode
// and ownership. Uses truncate-write rather than rename-into-place because the
// agent owns the file but not the parent directory (/etc/rathole is
// root-owned), which would block atomic rename.
//
// Pre-flight statfs check defends against the corruption mode where O_TRUNC
// destroys the existing content before the write fails with ENOSPC, leaving
// the file at 0 bytes. Hit in the wild during the noise migration on a
// machine with a full disk — the rathole tunnel went down and recovery
// required manually reconstructing the file from server-side state.
func writeFilePreservingMode(path string, content []byte) error {
	dir := filepath.Dir(path)
	if avail, ok := availableBytes(dir); ok {
		// 8 KiB margin covers ext4 metadata writes (inode + indirect block
		// updates) on a worst-case fragmented filesystem.
		needed := uint64(len(content)) + 8192
		if avail < needed {
			return fmt.Errorf("insufficient disk space in %s: %d bytes available, need %d (free disk before retrying)", dir, avail, needed)
		}
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o644) // #nosec G304 — caller resolved path
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(content); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", path, err)
	}
	return nil
}
