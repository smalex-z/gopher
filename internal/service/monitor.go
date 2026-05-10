package service

import (
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/smalex-z/gopher/internal/db"
)

type MonitorService struct {
	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
}

func NewMonitorService() *MonitorService {
	return &MonitorService{
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
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

// Stop signals the polling goroutine to exit and waits up to 5s for it to
// drain. Idempotent — extra calls return immediately. Wired to the SIGTERM
// handler in cmd/server/main.go so a `systemctl stop gopher` doesn't kill
// the goroutine mid-probe and leave a half-written status row.
func (s *MonitorService) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	select {
	case <-s.doneCh:
	case <-time.After(5 * time.Second):
		log.Printf("monitor: stop timeout — goroutine may still be in-flight")
	}
}

func (s *MonitorService) run() {
	defer close(s.doneCh)
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
		go goSafe("monitor.checkMachine", func() { s.checkMachine(m) })
	}
}

func (s *MonitorService) checkMachine(machine db.Machine) {
	if machine.TunnelPort == 0 {
		return
	}
	// Skip machines the HealthService is already polling via the agent —
	// running both writers against the same row was clobbering agent fields
	// every 30s. Health owns agent-installed machines; monitor stays the
	// fallback for legacy / un-migrated ones.
	if machine.AgentInstalled {
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
		go goSafe("monitor.checkTunnel", func() { s.checkTunnel(tunnel) })
	}
}

// checkTunnel probes the tunnel and stores one of three status values:
//
//   - "active"    — rathole client connected, service responding
//   - "connected" — rathole client connected, but service not responding on the client side
//   - "offline"   — rathole client not connected
//
// The key distinction between "connected" and "offline" relies on rathole's
// behaviour: when no client is connected, rathole holds the data-channel TCP
// connection open indefinitely (waiting for a client), so a read times out.
// When a client is connected but the service is not listening, rathole forwards
// the connection, the client gets an immediate connection refused, and closes
// the channel — so we receive an EOF with no data almost immediately.
func (s *MonitorService) checkTunnel(t db.Tunnel) {
	if t.RatholePort == 0 {
		return
	}
	start := time.Now()
	status := probeTunnel(t)
	latency := int(time.Since(start) / time.Millisecond)

	// Partial Update — see SetTunnelStatus godoc. Avoids the race where
	// the monitor's stale snapshot of the row would otherwise revert a
	// concurrent operator edit on next save.
	if err := db.SetTunnelStatus(t.ID, status); err != nil {
		log.Printf("monitor: failed to update tunnel %s: %v", t.ID, err)
	}

	// Record a health-check row per probe so the dashboard can render
	// per-tunnel uptime % and a sparkline. "active" is the only fully-OK
	// state — "connected" means rathole sees a client but the upstream
	// service didn't respond, which the operator should still notice.
	_ = db.RecordHealthCheck(&db.HealthCheck{
		Subject:   "tunnel:" + t.ID,
		OK:        status == "active",
		LatencyMS: latency,
		ErrorMsg:  "",
	})
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

		// TCP tunnel: passive read timed out (service speaks first — SSH, SMTP,
		// etc. — but nothing arrived). Fall back to an HTTP HEAD probe.
		// • If a client IS connected and the service is HTTP, it will respond →
		//   we get data → "active".
		// • If no client is connected, rathole buffers the HEAD but can't
		//   forward it → second read also times out → "offline".
		// • If the service is non-HTTP passive (rare), second read times out
		//   too → "offline" (acceptable: we can't confirm connectivity).
		_, _ = fmt.Fprintf(conn, "HEAD / HTTP/1.0\r\nHost: localhost\r\n\r\n")
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n2, readErr2 := conn.Read(buf)
		if n2 > 0 {
			return "active"
		}
		if isTimeout(readErr2) {
			return "offline"
		}
		// EOF after HEAD: rathole forwarded but service closed immediately.
		return "idle"
	}

	// EOF or connection reset on first read: rathole forwarded to the client,
	// the client couldn't reach the service, and closed the channel.
	return "idle"
}
