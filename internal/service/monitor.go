package service

import (
"log"
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
ticker := time.NewTicker(30 * time.Second)
defer ticker.Stop()

for range ticker.C {
s.checkMachines()
}
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
