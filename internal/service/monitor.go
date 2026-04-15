package service

import (
	"fmt"
	"log"
	"net"
	"time"

	"github.com/smalex-z/gopher/internal/db"
	sshpkg "github.com/smalex-z/gopher/internal/ssh"
)

type MonitorService struct{}

func NewMonitorService() *MonitorService {
	return &MonitorService{}
}

func (s *MonitorService) Start() {
	go s.run()
}

func (s *MonitorService) run() {
	// Check immediately on start, then every 30 seconds.
	s.checkAll()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.checkAll()
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
		go s.checkMachine(machine)
	}
}

func (s *MonitorService) checkMachine(machine db.Machine) {
	if machine.TunnelPort == 0 || machine.Username == "" {
		return
	}
	sshKey, err := db.GetSSHKeyForMachine(&machine)
	if err != nil {
		// No key configured — can't check, leave status unchanged.
		return
	}
	client, err := sshpkg.NewClient("localhost", machine.TunnelPort, machine.Username, sshKey.PrivateKey)
	if err != nil {
		// SSH failed — rathole client is not connected or machine is unreachable.
		machine.Status = "offline"
		if dbErr := db.UpdateMachine(&machine); dbErr != nil {
			log.Printf("monitor: failed to update machine %s: %v", machine.ID, dbErr)
		}
		return
	}
	_ = client.Close()

	now := time.Now()
	machine.LastSeen = &now
	machine.Status = "connected"
	if err := db.UpdateMachine(&machine); err != nil {
		log.Printf("monitor: failed to update machine %s: %v", machine.ID, err)
	}
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
		go s.checkTunnel(t)
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
	t.Status = probeTunnel(t)
	if err := db.UpdateTunnel(&t); err != nil {
		log.Printf("monitor: failed to update tunnel %s: %v", t.ID, err)
	}
}

// probeTunnel connects directly to the rathole port and classifies the result.
// For HTTP/HTTPS it sends a HEAD request first so the service actually responds.
func probeTunnel(t db.Tunnel) string {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", t.RatholePort), 3*time.Second)
	if err != nil {
		return "offline"
	}
	defer conn.Close()

	// For HTTP/HTTPS services we need to send a request before the server will
	// send any data back.
	if t.Protocol == "http" || t.Protocol == "https" {
		_, _ = fmt.Fprintf(conn, "HEAD / HTTP/1.0\r\nHost: localhost\r\n\r\n")
	}

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 8)
	n, readErr := conn.Read(buf)

	if n > 0 {
		return "active"
	}
	if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
		// Read deadline expired: rathole is holding the connection open waiting
		// for a client — no client is connected.
		return "offline"
	}
	// EOF or connection reset: rathole forwarded to the client, the client
	// couldn't reach the service, and closed the channel.
	return "idle"
}
