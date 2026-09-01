package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/smalex-z/gopher/internal/db"
	"github.com/smalex-z/gopher/internal/paths"
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
	// opMu serializes long-running ops that all share this hub. The hub is
	// a single bus and the dashboard's WS subscriber doesn't distinguish
	// op IDs, so two concurrent ops would interleave their log lines and
	// the first \x00DONE would close the WS while the other op is still
	// mid-step. Each op TryAcquireOp's before broadcasting and Release's
	// after DONE; double-fired ops get back ErrOpInProgress instead of a
	// corrupted log stream.
	opMu sync.Mutex
}

func NewLogHub() *LogHub {
	return &LogHub{
		subscribers: make(map[chan string]struct{}),
	}
}

// TryAcquireOp attempts to claim the op lock for this hub. Returns true if
// claimed (caller must defer ReleaseOp). Returns false if another op is
// already streaming through this hub.
func (h *LogHub) TryAcquireOp() bool {
	return h.opMu.TryLock()
}

// ReleaseOp releases the op lock. Pair with a successful TryAcquireOp.
func (h *LogHub) ReleaseOp() {
	h.opMu.Unlock()
}

func (h *LogHub) Subscribe() chan string {
	ch := make(chan string, 100)
	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *LogHub) Unsubscribe(ch chan string) {
	// Close under the write lock (not after releasing it). broadcastSentinel
	// sends under the read lock, so closing here — mutually exclusive with any
	// in-flight send — is what prevents the send-on-closed-channel panic that
	// would otherwise crash the whole daemon.
	h.mu.Lock()
	delete(h.subscribers, ch)
	close(ch)
	h.mu.Unlock()
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
	// Hold the read lock across the whole dispatch — including wg.Wait(). Because
	// Unsubscribe closes channels under the write lock, keeping RLock until every
	// per-subscriber send has completed or timed out guarantees no goroutine ever
	// sends on a channel being closed (which would panic and crash the daemon).
	// Other broadcasts still proceed concurrently (they also hold only RLock);
	// only Unsubscribe blocks, and only until the bounded sentinel timeout.
	h.mu.RLock()
	defer h.mu.RUnlock()

	var wg sync.WaitGroup
	for ch := range h.subscribers {
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

// ErrOpInProgress is returned when a caller tries to start a long-running
// op (install, firewall configure, etc.) while another is already running.
var ErrOpInProgress = errors.New("another long-running operation is in progress")

func NewDeployService() *DeployService {
	return &DeployService{
		Hub: NewLogHub(),
	}
}

// ratholeHostFromSettings returns the address that should appear as
// `remote_addr` in client rathole configs. ServerHost wins when set (covers
// skipCaddy installs where Domain is empty; a scheme prefix is stripped since
// the value lands in a host:port). With only Domain set, the answer is
// router.<domain> — the name the edge actually serves at — NEVER the bare
// apex: apex DNS frequently points at an org's main site on entirely
// different hosting (uclaacm.com is the club website; router.uclaacm.com is
// the VPS), and a client aimed at the apex can never connect.
// MigrateServerHostToRouter rewrote persisted apex ServerHosts for exactly
// that reason, but installs with an EMPTY ServerHost fell through to the raw
// Domain here and kept the bug. Returns "" when neither is set; callers
// should treat that as "we have nothing useful to write into a fresh
// client.toml" and either fail loudly or preserve the existing value.
func ratholeHostFromSettings(settings *db.AppSettings) string {
	if settings == nil {
		return ""
	}
	if host := strings.TrimSpace(settings.ServerHost); host != "" {
		host = strings.TrimPrefix(host, "https://")
		host = strings.TrimPrefix(host, "http://")
		return strings.TrimSuffix(host, "/")
	}
	if settings.Domain != "" {
		return "router." + settings.Domain
	}
	return ""
}

func (s *DeployService) logWriter() io.Writer {
	return &hubWriter{hub: s.Hub}
}

// DeployClient re-syncs a machine's rathole client.toml (agent push first, SSH
// fallback), streaming progress to the shared hub. Like Install/Fail2ban it
// takes the hub's op-lock so its log lines — and its terminating \x00DONE —
// can't interleave with a concurrent install or firewall op on the same WS
// bus. Returns ErrOpInProgress if another op is already streaming; otherwise
// the work runs in a background goroutine that releases the lock and fires a
// single sentinel when done.
func (s *DeployService) DeployClient(machine *db.Machine) error {
	if !s.Hub.TryAcquireOp() {
		return ErrOpInProgress
	}
	go goSafe("deployClient", func() {
		defer s.Hub.ReleaseOp()
		w := s.logWriter()
		if err := s.doDeployClient(machine, w); err != nil {
			fmt.Fprintf(w, "ERROR: %v\n", err)
		}
		s.Hub.Broadcast("\x00DONE")
	})
	return nil
}

// doDeployClient performs the actual config re-sync and returns an error rather
// than broadcasting — DeployClient owns the op-lock and the single terminating
// sentinel, so this body must never emit \x00DONE itself.
func (s *DeployService) doDeployClient(machine *db.Machine, w io.Writer) error {
	settings, err := db.GetSettings()
	if err != nil {
		return fmt.Errorf("failed to get settings: %w", err)
	}

	tunnels, err := db.GetTunnelsByMachine(machine.ID)
	if err != nil {
		return fmt.Errorf("failed to get tunnels: %w", err)
	}

	ratholeHost := ratholeHostFromSettings(settings)
	noisePub := settings.RatholeNoisePubKey

	// Agent-first: on an agent machine, rathole is already installed — a
	// "redeploy" is just a config re-sync. Read the current client.toml via the
	// agent, merge the managed sections, push it back. No SSH, no private key.
	if machine.AgentInstalled && machine.AgentRemotePort > 0 {
		fmt.Fprintln(w, "Syncing rathole client config via agent...")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		ac := NewAgentClient(machine)
		existing, gerr := ac.GetRatholeConfig(ctx)
		if gerr == nil {
			merged, merr := mergeClientManagedConfig(existing, machine, tunnels, ratholeHost, noisePub)
			if merr != nil {
				cancel()
				return fmt.Errorf("failed to generate client config: %w", merr)
			}
			perr := ac.PutRatholeConfig(ctx, merged)
			cancel()
			if perr == nil {
				fmt.Fprintln(w, "✓ Client config synced via agent (rathole reloads in place)")
				_ = db.SetMachineConfigPushPending(machine.ID, false)
				return nil
			}
			fmt.Fprintf(w, "WARN: agent config push failed (%v) — trying SSH\n", perr)
		} else {
			cancel()
			fmt.Fprintf(w, "WARN: agent unreachable (%v) — trying SSH\n", gerr)
		}
	}

	// SSH fallback — agent-down machines, needs a stored private key over the
	// tunnel. No key → don't attempt SSH; the agent is the only transport.
	var sshKey *db.SSHKey
	if machine.TunnelPort > 0 {
		sshKey, _ = db.GetSSHKeyForMachine(machine)
	}
	if sshKey == nil || sshKey.PrivateKey == "" {
		// Same retry semantics as updateClientToml: the machine's config may be
		// stale and nothing pushed — flag it so the health loop re-pushes via
		// the agent once it reconnects.
		_ = db.SetMachineConfigPushPending(machine.ID, true)
		return fmt.Errorf("no agent and no stored SSH private key (public-only machine) — nothing to redeploy over; the agent keeps config in sync automatically once reachable")
	}

	fmt.Fprintln(w, "Connecting to machine via tunnel...")
	client, err := sshpkg.NewClient(TunnelDialHost(machine), machine.TunnelPort, machine.Username, sshKey.PrivateKey)
	if err != nil {
		_ = db.SetMachineConfigPushPending(machine.ID, true)
		return fmt.Errorf("failed to connect to machine: %w", err)
	}
	defer client.Close()

	existingConfig, _ := client.Execute("cat " + paths.RatholeClientConfig + " 2>/dev/null || cat " + paths.LegacyRatholeClientConfig + " 2>/dev/null || cat ~/.config/rathole/client.toml 2>/dev/null")
	clientConfig, err := mergeClientManagedConfig(existingConfig, machine, tunnels, ratholeHost, noisePub)
	if err != nil {
		return fmt.Errorf("failed to generate client config: %w", err)
	}

	if err := sshpkg.DeployClient(client, machine.ID, machine.Username, clientConfig, w); err != nil {
		_ = db.SetMachineConfigPushPending(machine.ID, true)
		return err
	}
	_ = db.SetMachineConfigPushPending(machine.ID, false)
	return nil
}
