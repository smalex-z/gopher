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
s.Hub.Broadcast("\x00DONE")
return err
}
defer client.Close()

err = sshpkg.BootstrapVPS(client, w)
s.Hub.Broadcast("\x00DONE")
return err
}

func (s *DeployService) DeployVPS(vpsConfig *db.VPSConfig) error {
w := s.logWriter()
tunnels, err := db.GetAllTunnelsForVPS()
if err != nil {
fmt.Fprintf(w, "ERROR: Failed to get tunnels: %v\n", err)
s.Hub.Broadcast("\x00DONE")
return err
}

caddyfile, err := config.GenerateCaddyfile(*vpsConfig, tunnels)
if err != nil {
fmt.Fprintf(w, "ERROR: Failed to generate Caddyfile: %v\n", err)
s.Hub.Broadcast("\x00DONE")
return err
}

machines, err := db.GetMachines()
if err != nil {
fmt.Fprintf(w, "ERROR: Failed to get machines: %v\n", err)
s.Hub.Broadcast("\x00DONE")
return err
}

ratholeConfig, err := config.GenerateServerConfig(tunnels, machines)
if err != nil {
fmt.Fprintf(w, "ERROR: Failed to generate rathole config: %v\n", err)
s.Hub.Broadcast("\x00DONE")
return err
}

client, err := sshpkg.NewClient(vpsConfig.Host, vpsConfig.Port, vpsConfig.Username, vpsConfig.PrivateKey)
if err != nil {
fmt.Fprintf(w, "ERROR: Failed to connect: %v\n", err)
s.Hub.Broadcast("\x00DONE")
return err
}
defer client.Close()

err = sshpkg.DeployVPS(client, caddyfile, ratholeConfig, w)
s.Hub.Broadcast("\x00DONE")
return err
}

func (s *DeployService) DeployClient(machine *db.Machine, vpsConfig *db.VPSConfig) error {
w := s.logWriter()
tunnels, err := db.GetTunnelsByMachine(machine.ID)
if err != nil {
fmt.Fprintf(w, "ERROR: Failed to get tunnels: %v\n", err)
s.Hub.Broadcast("\x00DONE")
return err
}

clientConfig, err := config.GenerateClientConfig(vpsConfig.Host, tunnels)
if err != nil {
fmt.Fprintf(w, "ERROR: Failed to generate client config: %v\n", err)
s.Hub.Broadcast("\x00DONE")
return err
}

var client *sshpkg.SSHClient
if machine.TunnelPort > 0 && vpsConfig.SSHPrivateKey != "" {
fmt.Fprintln(w, "Connecting via VPS jump tunnel...")
client, err = sshpkg.NewClientViaJump(vpsConfig.Host, vpsConfig.Port, vpsConfig.Username, vpsConfig.PrivateKey,
machine.Username, vpsConfig.SSHPrivateKey, machine.TunnelPort)
} else {
fmt.Fprintln(w, "Connecting directly to machine...")
client, err = sshpkg.NewClient(machine.Host, machine.Port, machine.Username, machine.PrivateKey)
}
if err != nil {
fmt.Fprintf(w, "ERROR: Failed to connect to machine: %v\n", err)
s.Hub.Broadcast("\x00DONE")
return err
}
defer client.Close()

err = sshpkg.DeployClient(client, machine.ID, clientConfig, w)
s.Hub.Broadcast("\x00DONE")
return err
}
