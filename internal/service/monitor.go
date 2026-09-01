package service

import (
	"context"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/smalex-z/gopher/internal/db"
)

type MonitorService struct {
	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
	// probes tracks the per-machine / per-tunnel fan-out goroutines spawned by
	// each check cycle. Stop() waits on it so shutdown can't return while a
	// status-writer is still mid-flight — closing doneCh only proves the run
	// loop exited, not that the writers it launched have drained.
	probes sync.WaitGroup

	// offlineMu guards offlineStreak. checkTunnel runs concurrently across
	// tunnels (one goroutine per tunnel per cycle), so the debounce counter
	// needs its own lock independent of probes.
	offlineMu     sync.Mutex
	offlineStreak map[string]int // tunnel ID -> consecutive raw "offline" probes
}

func NewMonitorService() *MonitorService {
	return &MonitorService{
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
		offlineStreak: make(map[string]int),
	}
}

// Start launches the polling goroutine. Idempotent — repeat calls are no-ops
// so a misordered startup sequence can't end up with two monitors writing
// the same machine status rows in parallel.
func (s *MonitorService) Start() {
	s.startOnce.Do(func() {
		go goSafe("monitor.run", s.run)
	})
}

// Stop signals the polling goroutine to exit and waits for it — plus the
// fan-out probe goroutines it spawned — to drain. Idempotent; extra calls
// return immediately. Wired to the SIGTERM handler in cmd/server/main.go so a
// `systemctl stop gopher` doesn't kill a goroutine mid-probe and leave a
// half-written status row. The budget exceeds a single probe's worst case
// (~10s: 5s dial + 5s banner read) so the drain can actually complete;
// systemd's SIGTERM grace is 90s, so there's ample headroom.
func (s *MonitorService) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	select {
	case <-s.doneCh:
	case <-time.After(12 * time.Second):
		log.Printf("monitor: stop timeout — goroutine may still be in-flight")
	}
}

// run drives the poll loop. It only closes doneCh after the final check
// cycle's fan-out goroutines have drained, so Stop()'s wait covers the
// status-writers — not just the loop.
func (s *MonitorService) run() {
	defer close(s.doneCh)
	defer s.probes.Wait()
	// Check immediately on start, then every 30 seconds.
	s.checkAll()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.checkAll()
		}
	}
}

func (s *MonitorService) checkAll() {
	s.checkMachines()
	s.checkTunnels()
}

func (s *MonitorService) checkMachines() {
	machines, err := db.GetMachines()
	if err != nil {
		log.Printf("monitor: failed to get machines: %v", err)
		return
	}
	for _, machine := range machines {
		m := machine
		s.probes.Add(1)
		go goSafe("monitor.checkMachine", func() {
			defer s.probes.Done()
			s.checkMachine(m)
		})
	}
}

func (s *MonitorService) checkMachine(machine db.Machine) {
	if machine.TunnelPort == 0 {
		return
	}
	// Skip machines the HealthService owns — any machine with an agent
	// back-channel allocated, installed or not. Gating on AgentInstalled alone
	// left a two-writer conflict for machines whose agent install failed but
	// whose SSH tunnel works: health's WatchStatus stream marked them offline
	// (agent unreachable) while this probe marked them connected (SSH banner
	// OK), flapping the status every 30-60s. Health is the single status
	// writer for every agent-provisioned machine; monitor stays the fallback
	// for pre-agent legacy machines only (AgentRemotePort == 0).
	if machine.AgentInstalled || machine.AgentRemotePort > 0 {
		return
	}
	// Use an SSH banner grab rather than a full SSH handshake.
	//
	// golang.org/x/crypto/ssh's ClientConfig.Timeout only covers the TCP dial —
	// not the SSH handshake. Rathole always accepts the TCP connection (it binds
	// the port regardless of whether a client is connected), so the dial
	// succeeds instantly. The SSH handshake then waits for a banner from the
	// client VM's sshd. If the VM is offline, rathole holds the connection open
	// indefinitely and the handshake never completes — causing NewClient to hang
	// forever and the machine to stay "connected" forever.
	//
	// A banner read with a hard deadline is sufficient: sshd sends its version
	// string immediately on connect, so any data back means the VM is reachable.
	reachable := probeMachineSSH(TunnelDialHost(&machine), machine.TunnelPort)

	if !reachable {
		if err := db.SetMachineStatus(machine.ID, "offline", nil); err != nil {
			log.Printf("monitor: failed to update machine %s: %v", machine.ID, err)
		}
		return
	}

	now := time.Now()
	if err := db.SetMachineStatus(machine.ID, "connected", &now); err != nil {
		log.Printf("monitor: failed to update machine %s: %v", machine.ID, err)
	}
}

// probeMachineSSH connects to the machine's rathole tunnel port and reads the
// SSH banner with a short deadline. Returns true only when the VM's sshd sends
// data back, confirming the tunnel is live end-to-end.
func probeMachineSSH(host string, tunnelPort int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, tunnelPort), 5*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 8)
	n, err := conn.Read(buf)
	return err == nil && n > 0
}

// checkTunnels probes each tunnel for real end-to-end connectivity and updates
// the tunnel's status in the DB.
func (s *MonitorService) checkTunnels() {
	tunnels, err := db.GetTunnels()
	if err != nil {
		log.Printf("monitor: failed to get tunnels: %v", err)
		return
	}
	for _, t := range tunnels {
		tunnel := t
		s.probes.Add(1)
		go goSafe("monitor.checkTunnel", func() {
			defer s.probes.Done()
			s.checkTunnel(tunnel)
		})
	}
}

// checkTunnel probes the tunnel and stores one of four status values:
//
//   - "active"    — rathole client connected, upstream responded with bytes
//   - "connected" — rathole client connected, upstream is silent (e.g. a
//     client-speaks-first TCP service like Minecraft / MySQL that ignores
//     unrecognised data without closing the connection)
//   - "idle"      — rathole client connected, upstream EOF'd quickly
//     (port not bound on the client, app refused the connection)
//   - "offline"   — rathole bind port unreachable, OR probe was ambiguous
//     AND the corresponding machine is reporting offline
//
// The probe alone can't distinguish "no rathole client" from "client
// connected, silent upstream" — both observe as "TCP connect succeeded,
// no bytes flow." We resolve the ambiguity by cross-referencing the
// owning machine's status: if the machine is reachable (agent or SSH),
// the rathole-client is up, so a probe-side timeout means the upstream
// app is silent (= "connected"). If the machine is offline, the rathole
// tunnel really is dead (= "offline").
func (s *MonitorService) checkTunnel(t db.Tunnel) {
	if t.RatholePort == 0 {
		return
	}
	start := time.Now()

	status := s.debounceOffline(t.ID, tunnelStatus(t))
	latency := int(time.Since(start) / time.Millisecond)

	// Provisioning fallback (#93): the create-time verifier normally clears
	// CaddyPending within seconds, but it dies with the process — after a
	// restart or a >90s certificate stall this is what un-sticks the flag.
	if t.CaddyPending {
		if settings, err := db.GetSettings(); err == nil && tunnelHasHTTPRoute(&t, settings.Domain) {
			if caddyRouteServing(t.Subdomain+"."+settings.Domain, t.NoTLS, settings.BindIP) {
				if err := db.SetTunnelCaddyPending(t.ID, false); err != nil {
					log.Printf("monitor: clear caddy-pending for %s: %v", t.ID, err)
				}
			}
		} else if err == nil {
			// Route no longer exists (subdomain cleared, domain removed) —
			// nothing to verify; don't present provisioning forever.
			_ = db.SetTunnelCaddyPending(t.ID, false)
		}
	}

	// Partial Update — see SetTunnelStatus godoc. Avoids the race where
	// the monitor's stale snapshot of the row would otherwise revert a
	// concurrent operator edit on next save.
	if err := db.SetTunnelStatus(t.ID, status); err != nil {
		log.Printf("monitor: failed to update tunnel %s: %v", t.ID, err)
	}

	// Record a health-check row per probe so the dashboard can render
	// per-tunnel uptime % and a sparkline. Anything other than "offline"
	// means the tunnel layer is functional — gopher's responsibility ends
	// at the rathole forwarding path, and whether the upstream app is
	// responsive ("active"), silent ("connected"), or refusing ("idle")
	// is the user's domain. Treating only "active" as OK undercounted
	// uptime for client-speaks-first services like Minecraft.
	_ = db.RecordHealthCheck(&db.HealthCheck{
		Subject:   "tunnel:" + t.ID,
		OK:        status != "offline",
		LatencyMS: latency,
		ErrorMsg:  "",
	})
}

// debounceOffline suppresses a single transient "offline" reading before it
// reaches the dashboard. "offline" can only come from tunnelStatus's very
// first edge-side dial failing outright — every other branch (including the
// disambiguate fallback when the agent hop itself fails) resolves to
// "connected" as long as the owning machine is up. For a public tunnel that
// dial targets the VPS's own public bind address rather than loopback
// (tunnelProbeHost), which is a known-flaky path (hairpin NAT / provider
// security-group quirks on a box connecting to its own public IP) — a single
// blip there shouldn't flip the badge red for one 30s cycle and back the
// next. Two consecutive raw "offline" reads are required before we actually
// report it; any non-offline read resets the streak immediately, so a real
// outage still surfaces within one extra cycle (~60s worst case).
func (s *MonitorService) debounceOffline(tunnelID, raw string) string {
	s.offlineMu.Lock()
	defer s.offlineMu.Unlock()
	if raw != "offline" {
		delete(s.offlineStreak, tunnelID)
		return raw
	}
	s.offlineStreak[tunnelID]++
	if s.offlineStreak[tunnelID] < 2 {
		return "connected"
	}
	return "offline"
}

// tunnelStatus determines a tunnel's status with a hybrid probe:
//
//  1. Pass real traffic end-to-end through the tunnel's own rathole port. A
//     positive result (response bytes for TCP, a datagram back for UDP) proves
//     the whole path — VPS bind → service tunnel → origin → back — so it's
//     "active" with no inference.
//  2. When the probe connects but the service stays silent (TCP speak-first
//     apps; UDP no reply — both ambiguous), ask the origin's agent whether the
//     local port is actually bound: listening → "connected", not → "idle". This
//     is the only reliable idle signal for UDP and de-muddies the TCP timeout.
//  3. With no reachable agent, fall back to the owning machine's status.
//
// Shared by the monitor and the manual Test action so they never disagree.
func tunnelStatus(t db.Tunnel) string {
	if t.RatholePort == 0 {
		return "offline"
	}
	if t.Transport == "udp" {
		if probeUDPPath(t) == "active" {
			return "active" // origin replied through the tunnel — full path proven
		}
		return disambiguate(t) // no reply — ambiguous, resolve via the agent
	}
	switch p := probeTunnel(t); p {
	case "active", "idle", "offline":
		return p // definitive from the edge round-trip
	default: // "connected": reachable but silent — ambiguous
		return disambiguate(t)
	}
}

// disambiguate resolves a "reachable but silent" probe. It prefers the origin
// agent's view of whether the local service port is bound (listening →
// "connected", not → "idle"); with no reachable agent it treats an offline
// machine as "offline", otherwise "connected".
func disambiguate(t db.Tunnel) string {
	if listening, ok := agentPortListening(t); ok {
		if listening {
			return "connected"
		}
		return "idle"
	}
	if machineOffline(t) {
		return "offline"
	}
	return "connected"
}

// probeUDPPath sends a datagram through the tunnel's rathole port and reads for
// a reply. A reply proves the full path works ("active"); no reply is ambiguous
// ("silent") — a UDP service may simply ignore an unrecognised probe — so the
// caller disambiguates via the agent. UDP is connectionless, so this alone
// never yields "idle"/"offline".
func probeUDPPath(t db.Tunnel) string {
	addr := net.JoinHostPort(tunnelProbeHost(t), strconv.Itoa(t.RatholePort))
	conn, err := net.DialTimeout("udp", addr, 3*time.Second)
	if err != nil {
		return "silent"
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte{0}); err != nil {
		return "silent"
	}
	buf := make([]byte, 8)
	if n, rerr := conn.Read(buf); rerr == nil && n > 0 {
		return "active"
	}
	return "silent"
}

// agentPortListening asks the origin's agent whether the tunnel's local service
// port is bound (read from /proc/net). Returns ok=false when the machine has no
// reachable/new-enough agent (no agent, port 0, unreachable, or pre-0.2.3
// returning Unimplemented) so the caller falls back.
func agentPortListening(t db.Tunnel) (listening bool, ok bool) {
	if t.MachineID == "" {
		return false, false
	}
	machine, err := db.GetMachine(t.MachineID)
	if err != nil || machine == nil || !machine.AgentInstalled || machine.AgentRemotePort == 0 {
		return false, false
	}
	proto := t.Transport
	if proto == "" {
		proto = "tcp"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	l, err := NewAgentClient(machine).PortListening(ctx, t.LocalPort, proto)
	if err != nil {
		return false, false
	}
	return l, true
}

// machineOffline reports whether the tunnel's owning machine is marked offline.
func machineOffline(t db.Tunnel) bool {
	if t.MachineID == "" {
		return false
	}
	m, err := db.GetMachine(t.MachineID)
	return err == nil && m != nil && m.Status == "offline"
}

// probeTunnel connects directly to the rathole port and classifies the result.
//
// Strategy: first attempt a passive read (services like SSH, SMTP send a banner
// immediately on connect). If nothing arrives within the deadline we fall back
// to an HTTP HEAD probe — this covers the very common case of a tcp-typed
// tunnel that actually fronts an HTTP service, and lets us distinguish
// "rathole has no client" (HEAD disappears into rathole's buffer, second
// timeout) from "client connected, HTTP service running" (HEAD elicits a
// response).
// tunnelProbeHost returns the IP to dial when probing a tunnel port.
// Private tunnels always bind 127.0.0.1. Public tunnels bind BindIP (or 0.0.0.0
// when unset, which includes loopback — so 127.0.0.1 is still reachable).
func tunnelProbeHost(t db.Tunnel) string {
	if !t.Private {
		if settings, err := db.GetSettings(); err == nil && settings.BindIP != "" {
			return settings.BindIP
		}
	}
	return "127.0.0.1"
}

func probeTunnel(t db.Tunnel) string {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", tunnelProbeHost(t), t.RatholePort), 3*time.Second)
	if err != nil {
		return "offline"
	}
	defer conn.Close()

	isHTTP := t.Protocol == "http" || t.Protocol == "https"

	// For HTTP/HTTPS send the probe request up front so the service responds.
	if isHTTP {
		_, _ = fmt.Fprintf(conn, "HEAD / HTTP/1.0\r\nHost: localhost\r\n\r\n")
	}

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 8)
	n, readErr := conn.Read(buf)

	if n > 0 {
		return "active"
	}

	isTimeout := func(e error) bool {
		ne, ok := e.(net.Error)
		return ok && ne.Timeout()
	}

	if isTimeout(readErr) {
		if isHTTP {
			// HTTP tunnel timed out — can't distinguish "no rathole client"
			// from "client connected, service slow". Return "connected" so a
			// running service is never falsely shown as offline.
			return "connected"
		}

		// TCP tunnel: passive read timed out (no banner). Fall back to an
		// HTTP HEAD probe and classify by the response.
		// • Bytes back            → "active" (service responded — common
		//                          for HTTP tunnels)
		// • EOF / RST quickly     → "idle" (rathole forwarded, upstream
		//                          rejected — port not bound on the client)
		// • Second timeout        → "connected" (genuinely ambiguous — could
		//                          be no rathole client OR a client-speaks-
		//                          first service like Minecraft / MySQL that
		//                          ignores garbage bytes without closing).
		//                          checkTunnel resolves the ambiguity by
		//                          consulting machine.Status: if the machine
		//                          is offline the tunnel is too; otherwise
		//                          the rathole-client is up and we count it
		//                          as a working tunnel.
		_, _ = fmt.Fprintf(conn, "HEAD / HTTP/1.0\r\nHost: localhost\r\n\r\n")
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n2, readErr2 := conn.Read(buf)
		if n2 > 0 {
			return "active"
		}
		if isTimeout(readErr2) {
			return "connected"
		}
		// EOF after HEAD: rathole forwarded but service closed immediately.
		return "idle"
	}

	// EOF or connection reset on first read: rathole forwarded to the client,
	// the client couldn't reach the service, and closed the channel.
	return "idle"
}
