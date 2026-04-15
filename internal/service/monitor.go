package service

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
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
		return
	}
	client, err := sshpkg.NewClient("localhost", machine.TunnelPort, machine.Username, sshKey.PrivateKey)
	if err != nil {
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
// the tunnel's status in the DB to "active" or "inactive".
func (s *MonitorService) checkTunnels() {
	tunnels, err := db.GetTunnels()
	if err != nil {
		log.Printf("monitor: failed to get tunnels: %v", err)
		return
	}
	settings, err := db.GetSettings()
	if err != nil {
		log.Printf("monitor: failed to get settings: %v", err)
		return
	}
	for _, t := range tunnels {
		go s.checkTunnel(t, settings.Domain)
	}
}

// checkTunnel uses a protocol-aware probe to determine whether the tunnel
// client is actually connected and forwarding traffic.
//
// Rathole always keeps the server-side port open, so a plain TCP connect
// succeeds regardless of whether any client is behind the tunnel. Instead:
//
//   - SSH tunnels: read the SSH banner — only present when a client forwards.
//   - HTTP/HTTPS tunnels with a domain: GET the subdomain URL and treat
//     502/503/504 (Caddy can't reach backend) as offline.
//   - Everything else: fall back to TCP connect + read probe.
func (s *MonitorService) checkTunnel(t db.Tunnel, domain string) {
	if t.RatholePort == 0 {
		return
	}

	var online bool
	switch t.Protocol {
	case "ssh":
		online = probeSSHBanner(t.RatholePort)
	default:
		if t.Subdomain != "" && domain != "" {
			online = probeHTTP(t, domain)
		} else {
			online = probeTCPRead(t.RatholePort)
		}
	}

	if online {
		t.Status = "active"
	} else {
		t.Status = "inactive"
	}
	if err := db.UpdateTunnel(&t); err != nil {
		log.Printf("monitor: failed to update tunnel %s: %v", t.ID, err)
	}
}

// probeSSHBanner connects to the rathole port and waits for an SSH banner.
// When a rathole client is connected and forwarding an SSH service, the remote
// SSH daemon sends its version string immediately. An empty read means nobody
// is behind the tunnel.
func probeSSHBanner(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 8)
	n, err := conn.Read(buf)
	return err == nil && n > 0
}

// probeHTTP makes a GET to the tunnel's public URL and treats Caddy gateway
// errors (502/503/504) as offline. Any other response — including auth errors
// or 404 — means the service is reachable through the tunnel.
func probeHTTP(t db.Tunnel, domain string) bool {
	scheme := "https"
	if t.NoTLS {
		scheme = "http"
	}
	url := fmt.Sprintf("%s://%s.%s/", scheme, t.Subdomain, domain)
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 — health probe only
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // don't follow redirects
		},
	}
	resp, err := client.Get(url) // #nosec G107 — URL constructed from trusted DB values
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode != 502 && resp.StatusCode != 503 && resp.StatusCode != 504
}

// probeTCPRead connects to the rathole port and attempts to read data with a
// short deadline. Used for generic TCP tunnels without a known protocol banner.
// Rathole keeps ports open even with no client, so a plain connect is
// insufficient — we need the far end to send something back.
func probeTCPRead(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	n, err := conn.Read(buf)
	return err == nil && n > 0
}
