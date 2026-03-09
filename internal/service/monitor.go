package service

import (
	"fmt"
	"log"
	"net"
	"time"

	"github.com/smalex-z/gopher/internal/db"
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
	now := time.Now()
	machine.LastSeen = &now
	if err := db.UpdateMachine(&machine); err != nil {
		log.Printf("monitor: failed to update machine %s: %v", machine.ID, err)
	}
}

// checkTunnels probes each tunnel's rathole_port on localhost and updates
// the tunnel's status in the DB to "active" or "inactive".
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

func (s *MonitorService) checkTunnel(t db.Tunnel) {
	if t.RatholePort == 0 {
		return
	}
	// A tunnel is "active" if rathole is listening on its port (someone is connected).
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", t.RatholePort), 2*time.Second)
	if err == nil {
		conn.Close()
		t.Status = "active"
	} else {
		t.Status = "inactive"
	}
	if err := db.UpdateTunnel(&t); err != nil {
		log.Printf("monitor: failed to update tunnel %s: %v", t.ID, err)
	}
}
