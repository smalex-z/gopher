package service

import "github.com/smalex-z/gopher/internal/db"

type localOps interface {
	AddServiceTunnel(tunnel *db.Tunnel, machine *db.Machine) error
	RemoveServiceTunnelClient(tunnel *db.Tunnel, machine *db.Machine) error
	ReconcileServerConfig() error
	RemoveServiceTunnelCaddy(tunnel *db.Tunnel) error
	RemoveMachineClient(machine *db.Machine) error
}
