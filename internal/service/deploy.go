package service

import (
"fmt"
"io"
"sync"

"github.com/smalex-z/gopher/internal/config"
"github.com/smalex-z/gopher/internal/db"
sshpkg "github.com/smalex-z/gopher/internal/ssh"
)

type LogHub struct {
mu          sync.RWMutex
subscribers map[chan string]struct{}
}

func NewLogHub() *LogHub {
return &LogHub{
subscribers: make(map[chan string]struct{}),
}
}

func (h *LogHub) Subscribe() chan string {
ch := make(chan string, 100)
h.mu.Lock()
h.subscribers[ch] = struct{}{}
h.mu.Unlock()
return ch
}

func (h *LogHub) Unsubscribe(ch chan string) {
h.mu.Lock()
delete(h.subscribers, ch)
h.mu.Unlock()
close(ch)
}

func (h *LogHub) Broadcast(msg string) {
h.mu.RLock()
defer h.mu.RUnlock()
for ch := range h.subscribers {
select {
case ch <- msg:
default:
}
}
}

type hubWriter struct {
hub *LogHub
}

func (w *hubWriter) Write(p []byte) (n int, err error) {
w.hub.Broadcast(string(p))
return len(p), nil
}

type DeployService struct {
Hub *LogHub
}

func NewDeployService() *DeployService {
return &DeployService{
Hub: NewLogHub(),
}
}

func (s *DeployService) logWriter() io.Writer {
return &hubWriter{hub: s.Hub}
}

func (s *DeployService) Bootstrap(vpsConfig *db.VPSConfig) error {
w := s.logWriter()
client, err := sshpkg.NewClient(vpsConfig.Host, vpsConfig.Port, vpsConfig.Username, vpsConfig.PrivateKey)
if err != nil {
fmt.Fprintf(w, "ERROR: Failed to connect: %v\n", err)
return err
}
defer client.Close()

return sshpkg.BootstrapVPS(client, w)
}

func (s *DeployService) DeployVPS(vpsConfig *db.VPSConfig) error {
w := s.logWriter()
tunnels, err := db.GetAllTunnelsForVPS()
if err != nil {
fmt.Fprintf(w, "ERROR: Failed to get tunnels: %v\n", err)
return err
}

caddyfile, err := config.GenerateCaddyfile(*vpsConfig, tunnels)
if err != nil {
fmt.Fprintf(w, "ERROR: Failed to generate Caddyfile: %v\n", err)
return err
}

ratholeConfig, err := config.GenerateServerConfig(tunnels)
if err != nil {
fmt.Fprintf(w, "ERROR: Failed to generate rathole config: %v\n", err)
return err
}

client, err := sshpkg.NewClient(vpsConfig.Host, vpsConfig.Port, vpsConfig.Username, vpsConfig.PrivateKey)
if err != nil {
fmt.Fprintf(w, "ERROR: Failed to connect: %v\n", err)
return err
}
defer client.Close()

return sshpkg.DeployVPS(client, caddyfile, ratholeConfig, w)
}

func (s *DeployService) DeployClient(machine *db.Machine, vpsConfig *db.VPSConfig) error {
w := s.logWriter()
tunnels, err := db.GetTunnelsByMachine(machine.ID)
if err != nil {
fmt.Fprintf(w, "ERROR: Failed to get tunnels: %v\n", err)
return err
}

clientConfig, err := config.GenerateClientConfig(vpsConfig.Host, tunnels)
if err != nil {
fmt.Fprintf(w, "ERROR: Failed to generate client config: %v\n", err)
return err
}

client, err := sshpkg.NewClient(machine.Host, machine.Port, machine.Username, machine.PrivateKey)
if err != nil {
fmt.Fprintf(w, "ERROR: Failed to connect to machine: %v\n", err)
return err
}
defer client.Close()

return sshpkg.DeployClient(client, machine.ID, clientConfig, w)
}
