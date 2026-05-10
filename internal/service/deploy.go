package service

import (
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/smalex-z/gopher/internal/db"
	sshpkg "github.com/smalex-z/gopher/internal/ssh"
)

// Sentinel messages broadcast by LogHub at the end of a streamed operation.
// The dashboard treats either of these as "log stream complete" and closes
// the modal; without them the modal hangs. They get blocking delivery
// semantics in Broadcast (see broadcastSentinel) so a slow subscriber can't
// silently drop them and leave the dashboard waiting forever.
const (
	logSentinelDone  = "\x00DONE"
	logSentinelError = "\x00ERROR"
)

// sentinelDeliveryTimeout caps how long we'll wait for a single subscriber
// to accept a sentinel before giving up on it. Per-subscriber, not global —
// slow subscribers don't block fast ones.
const sentinelDeliveryTimeout = 3 * time.Second

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

// Broadcast delivers msg to every current subscriber. Regular log lines use
// non-blocking sends so a slow client can't head-of-line-block the broadcast
// (lost log lines on a 100-buffer overflow are acceptable). Sentinels
// (\x00DONE / \x00ERROR) are routed through a blocking-with-timeout path
// because losing them leaves the dashboard modal waiting indefinitely on a
// stream that has actually finished.
func (h *LogHub) Broadcast(msg string) {
	if msg == logSentinelDone || msg == logSentinelError {
		h.broadcastSentinel(msg)
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subscribers {
		select {
		case ch <- msg:
		default:
		}
	}
}

// broadcastSentinel delivers msg to every subscriber with a per-subscriber
// timeout. Subscribers are dispatched in parallel so a single slow one
// doesn't delay delivery to the rest. After the timeout, the sentinel is
// dropped for that subscriber and a warning is logged — the WebSocket will
// eventually be torn down by Unsubscribe when the handler returns, so the
// damage is bounded to "this one slow client never sees DONE."
func (h *LogHub) broadcastSentinel(msg string) {
	h.mu.RLock()
	subs := make([]chan string, 0, len(h.subscribers))
	for ch := range h.subscribers {
		subs = append(subs, ch)
	}
	h.mu.RUnlock()

	var wg sync.WaitGroup
	for _, ch := range subs {
		wg.Add(1)
		go func(c chan string) {
			defer wg.Done()
			t := time.NewTimer(sentinelDeliveryTimeout)
			defer t.Stop()
			select {
			case c <- msg:
			case <-t.C:
				log.Printf("LogHub: dropping sentinel %q for slow subscriber after %s", msg, sentinelDeliveryTimeout)
			}
		}(ch)
	}
	wg.Wait()
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

// ratholeHostFromSettings returns the address that should appear as
// `remote_addr` in client rathole configs. ServerHost wins when set
// (covers skipCaddy installs where Domain is empty), then Domain. Returns
// "" when neither is set; callers should treat that as "we have nothing
// useful to write into a fresh client.toml" and either fail loudly or
// preserve the existing value.
func ratholeHostFromSettings(settings *db.AppSettings) string {
	if settings == nil {
		return ""
	}
	if settings.ServerHost != "" {
		return settings.ServerHost
	}
	return settings.Domain
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

func (s *DeployService) DeployClient(machine *db.Machine) error {
	w := s.logWriter()

	settings, err := db.GetSettings()
	if err != nil {
		fmt.Fprintf(w, "ERROR: Failed to get settings: %v\n", err)
		s.Hub.Broadcast("\x00DONE")
		return err
	}

	tunnels, err := db.GetTunnelsByMachine(machine.ID)
	if err != nil {
		fmt.Fprintf(w, "ERROR: Failed to get tunnels: %v\n", err)
		s.Hub.Broadcast("\x00DONE")
		return err
	}

	var sshKey *db.SSHKey
	if machine.TunnelPort > 0 {
		var keyErr error
		sshKey, keyErr = db.GetSSHKeyForMachine(machine)
		if keyErr != nil {
			// Surface the lookup failure rather than silently falling through
			// to the legacy direct-host path. Without this, the caller sees a
			// generic "no SSH access" / "SSH dial failed" error and has no
			// signal that the actual cause is a missing or detached SSH key.
			fmt.Fprintf(w, "WARN: SSH key lookup for machine %s failed: %v\n", machine.ID, keyErr)
		}
	}

	var client *sshpkg.SSHClient
	if machine.TunnelPort > 0 && sshKey != nil {
		fmt.Fprintln(w, "Connecting to machine via tunnel...")
		client, err = sshpkg.NewClient(TunnelDialHost(machine), machine.TunnelPort, machine.Username, sshKey.PrivateKey)
	} else if machine.Host != "" {
		fmt.Fprintln(w, "Connecting directly to machine...")
		client, err = sshpkg.NewClient(machine.Host, machine.Port, machine.Username, machine.PrivateKey)
	} else {
		err = fmt.Errorf("no SSH access: machine has no host and tunnel is not established")
	}
	if err != nil {
		fmt.Fprintf(w, "ERROR: Failed to connect to machine: %v\n", err)
		s.Hub.Broadcast("\x00DONE")
		return err
	}
	defer client.Close()

	existingConfig, _ := client.Execute("cat /etc/rathole/client.toml 2>/dev/null || cat ~/.config/rathole/client.toml 2>/dev/null")
	clientConfig, err := mergeClientManagedConfig(existingConfig, machine, tunnels, ratholeHostFromSettings(settings))
	if err != nil {
		fmt.Fprintf(w, "ERROR: Failed to generate client config: %v\n", err)
		s.Hub.Broadcast("\x00DONE")
		return err
	}

	err = sshpkg.DeployClient(client, machine.ID, machine.Username, clientConfig, w)
	s.Hub.Broadcast("\x00DONE")
	return err
}
