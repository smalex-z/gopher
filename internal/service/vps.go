package service

import (
	"github.com/smalex-z/gopher/internal/db"
)

// VPSService exposes the read-only VPS record (host/username/domain) that the
// local "Server Info" UI and jumpbox-command builders read. The old remote
// "configure a VPS over SSH" write/bootstrap path was removed — Gopher now runs
// locally on the VPS via the setup wizard.
type VPSService struct{}

func NewVPSService() *VPSService {
	return &VPSService{}
}

func (s *VPSService) Get() (*db.VPSConfig, error) {
	return db.GetVPS()
}
