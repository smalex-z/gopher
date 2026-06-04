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
	"google.golang.org/grpc/reflection"

	agentpb "github.com/smalex-z/gopher/internal/agentpb"
)

const (
	// agentVersion is the agent build version. It is bumped manually and is
	// intentionally independent of the server's tag-injected version.
	agentVersion = "0.2.0"

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
}

func loadConfig() config {
	c := config{
		Port:     4322,
		Token:    os.Getenv("GOPHER_AGENT_TOKEN"),
		UnitName: "rathole-client.service",
	}
	if p, err := strconv.Atoi(os.Getenv("GOPHER_AGENT_PORT")); err == nil && p > 0 {
		c.Port = p
	}
	if u := os.Getenv("GOPHER_AGENT_UNIT"); u != "" {
		c.UnitName = u
	}
	// Optional config file at /etc/gopher-agent/config.env (KEY=value lines).
	// Useful when systemd EnvironmentFile is preferred over inline Environment=.
	if data, err := os.ReadFile("/etc/gopher-agent/config.env"); err == nil {
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

	cfg := loadConfig()
	if cfg.Token == "" {
		log.Fatal("GOPHER_AGENT_TOKEN is required (env var or /etc/gopher-agent/config.env)")
	}

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
	)
	agentpb.RegisterAgentControlServer(grpcSrv, srv)
	// Reflection lets `grpcurl` introspect the service for debugging. The auth
	// interceptors apply to reflection too, so it still requires the token.
	reflection.Register(grpcSrv)

	httpSrv := &http.Server{
		Handler:           srv.httpHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
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

func ratholeStatus(unit string) ratholeInfo {
	state := runProp(unit, "ActiveState")
	substate := runProp(unit, "SubState")
	return ratholeInfo{
		Active:   state == "active",
		State:    state,
		Substate: substate,
	}
}

func unitActive(unit string) (bool, string) {
	state := runProp(unit, "ActiveState")
	substate := runProp(unit, "SubState")
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
// The agent runs as the SSH user (set in bootstrap), and bootstrap chowns
// /etc/rathole/client.toml to that user, so direct file I/O works without sudo.

const (
	clientTomlPath        = "/etc/rathole/client.toml"
	maxRatholeConfigBytes = 1 << 20 // 1 MiB — generous but bounded
)

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
