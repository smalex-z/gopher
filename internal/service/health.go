package service

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/smalex-z/gopher/internal/db"
)

// HealthService runs an in-process loop that polls every connected machine's
// gopher-agent every healthCheckInterval, records the result, and triggers
// auto-recovery when the rathole client on the machine looks unhealthy.
//
// Machines without the agent installed are still polled with a TCP probe to
// the SSH tunnel port (the same signal the existing monitor used) — they
// produce health records too, just less detailed.
type HealthService struct {
	interval         time.Duration
	purgeInterval    time.Duration
	purgeOlderThan   time.Duration
	autoRecover      bool

	stopCh   chan struct{}
	stopOnce sync.Once
	running  atomic.Bool

	// Per-machine state for the auto-recovery throttle. We don't want to spam
	// `systemctl restart rathole-client` if the machine is durably broken.
	mu             sync.Mutex
	lastRecovery   map[string]time.Time

	// Per-machine last observed status so we can emit events on transition only,
	// not on every poll. Values: "ok" | "degraded" | "offline". Empty until the
	// first check for a given machine — a "first observation" is not a
	// transition and produces no event.
	lastStatus map[string]string
}

const (
	healthCheckInterval        = 60 * time.Second
	healthRecoveryCooldown     = 3 * time.Minute
	healthCheckRetentionDays   = 7
	healthCheckPurgeFrequency  = 6 * time.Hour
)

func NewHealthService(autoRecover bool) *HealthService {
	return &HealthService{
		interval:       healthCheckInterval,
		purgeInterval:  healthCheckPurgeFrequency,
		purgeOlderThan: time.Duration(healthCheckRetentionDays) * 24 * time.Hour,
		autoRecover:    autoRecover,
		stopCh:         make(chan struct{}),
		lastRecovery:   map[string]time.Time{},
		lastStatus:     map[string]string{},
	}
}

// emitTransition records a state change as an event, but only when it's
// actually a change. First observations produce nothing — we don't want a
// "machine_connected" event for every machine on every server boot.
//
// Returns the previous status string so callers can branch on whether this
// was a fresh observation (returns "") or a real transition.
func (s *HealthService) emitTransition(m *db.Machine, newStatus, reason string) string {
	s.mu.Lock()
	prev := s.lastStatus[m.ID]
	s.lastStatus[m.ID] = newStatus
	s.mu.Unlock()

	if prev == "" || prev == newStatus {
		return prev
	}

	var kind string
	switch newStatus {
	case "ok":
		// Came back from a non-ok state. Distinguish recovery (we had recently
		// triggered a restart) from a passive reconnect.
		s.mu.Lock()
		recoveredRecently := false
		if last, ok := s.lastRecovery[m.ID]; ok && time.Since(last) < 2*healthRecoveryCooldown {
			recoveredRecently = true
		}
		s.mu.Unlock()
		if recoveredRecently {
			kind = "machine_recovered"
		} else {
			kind = "machine_connected"
		}
	case "degraded":
		kind = "machine_degraded"
	case "offline":
		kind = "machine_disconnected"
	default:
		return prev
	}

	meta := ""
	if reason != "" {
		meta = fmt.Sprintf(`{"reason":%q}`, reason)
	}
	def := db.LookupKindDefault(kind)
	db.RecordEvent(&db.Event{
		Severity:     def.Severity,
		Source:       "machine",
		Kind:         kind,
		Actor:        "system",
		ResourceType: "machine",
		ResourceID:   m.ID,
		ResourceName: m.Name,
		Message:      fmt.Sprintf(def.MessageTemplate, m.Name),
		Metadata:     meta,
	})
	return prev
}

func (s *HealthService) Start() {
	if !s.running.CompareAndSwap(false, true) {
		return
	}
	go s.loop()
	go s.janitorLoop()
}

func (s *HealthService) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

func (s *HealthService) loop() {
	// Run the first sweep immediately so the dashboard isn't blank for a minute
	// after startup.
	s.tick()
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
			s.tick()
		}
	}
}

// tick walks every machine and runs a check. Concurrency is capped at
// healthCheckParallelism so we don't hammer SQLite (single writer) with N
// parallel inserts when there are dozens of machines.
func (s *HealthService) tick() {
	machines, err := db.GetMachines()
	if err != nil {
		log.Printf("health: list machines failed: %v", err)
		return
	}
	const healthCheckParallelism = 4
	sem := make(chan struct{}, healthCheckParallelism)
	var wg sync.WaitGroup
	for i := range machines {
		m := machines[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			s.checkMachine(&m)
		}()
	}
	wg.Wait()
}

// checkMachine runs the appropriate probe for a machine: agent /status when
// the agent is reachable through the rathole back-channel, fallback
// TCP-to-tunnel-port otherwise.
//
// We try the agent path even when AgentInstalled=false as long as
// AgentRemotePort is allocated. That covers the inline-bootstrap case: the
// bootstrap script installs the agent without a separate VPS-side callback,
// so the first successful agent probe is what flips AgentInstalled true.
func (s *HealthService) checkMachine(m *db.Machine) {
	subject := "machine:" + m.ID
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	if m.AgentRemotePort > 0 {
		if ok := s.checkViaAgent(ctx, m, subject, start); ok {
			return
		}
		// Agent unreachable — fall through to TCP probe so the dashboard
		// still gets a connectivity signal. Don't flip AgentInstalled false:
		// the agent might be transiently down and we know it's been
		// installed previously.
	}
	s.checkViaTCP(ctx, m, subject, start)
}

// checkViaAgent returns true if the agent answered (regardless of rathole
// health on the box). false means the agent is unreachable and the caller
// should fall back to the TCP probe.
func (s *HealthService) checkViaAgent(ctx context.Context, m *db.Machine, subject string, start time.Time) bool {
	client := NewAgentClient(m)
	status, err := client.Status(ctx)
	latency := int(time.Since(start) / time.Millisecond)

	if err != nil {
		_ = db.RecordHealthCheck(&db.HealthCheck{
			Subject:   subject,
			OK:        false,
			LatencyMS: latency,
			ErrorMsg:  "agent unreachable: " + err.Error(),
		})
		return false
	}

	// Agent reachable — but is rathole healthy on the box? An "agent up,
	// rathole down" state means the back-channel works but tunnels don't.
	// We must flip machine.Status to offline so the dashboard stops claiming
	// the machine is connected (its tunnels can't actually serve traffic).
	// Without this update the previous "connected" value from the last good
	// poll sticks indefinitely — the agent's existence doesn't certify
	// tunnel health, only its own reachability.
	if !status.Rathole.Active {
		errMsg := fmt.Sprintf("rathole-client not active (state=%s/%s)", status.Rathole.State, status.Rathole.Substate)
		_ = db.RecordHealthCheck(&db.HealthCheck{
			Subject:   subject,
			OK:        false,
			LatencyMS: latency,
			ErrorMsg:  errMsg,
		})
		// AgentLastSeen still updates so the operator can see the agent is
		// up (the back-channel works). Status reflects rathole, not agent.
		now := time.Now()
		if err := db.SetMachineAgentDegraded(m.ID, status.AgentVersion, now); err != nil {
			log.Printf("health: persist agent-degraded for %s: %v", m.ID, err)
		}
		s.emitTransition(m, "degraded", errMsg)
		s.maybeRecover(m, "rathole inactive")
		return true
	}

	// Healthy. Use a partial Updates call so this write doesn't clobber
	// fields the monitor or another writer touched between our load and our
	// save (full-record GORM Save was racing with monitor.go).
	now := time.Now()
	if err := db.SetMachineAgentSeen(m.ID, status.AgentVersion, now); err != nil {
		log.Printf("health: persist agent-seen for %s: %v", m.ID, err)
	}

	_ = db.RecordHealthCheck(&db.HealthCheck{
		Subject:   subject,
		OK:        true,
		LatencyMS: latency,
	})
	s.emitTransition(m, "ok", "")
	return true
}

func (s *HealthService) checkViaTCP(ctx context.Context, m *db.Machine, subject string, start time.Time) {
	if m.TunnelPort == 0 {
		return
	}
	addr := fmt.Sprintf("127.0.0.1:%d", m.TunnelPort)
	d := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	latency := int(time.Since(start) / time.Millisecond)
	if err != nil {
		_ = db.RecordHealthCheck(&db.HealthCheck{
			Subject:   subject,
			OK:        false,
			LatencyMS: latency,
			ErrorMsg:  "tcp probe failed: " + err.Error(),
		})
		// Without the agent we can't auto-restart. Just record the failure.
		_ = db.SetMachineStatus(m.ID, "offline", nil)
		s.emitTransition(m, "offline", err.Error())
		return
	}
	_ = conn.Close()

	now := time.Now()
	_ = db.SetMachineStatus(m.ID, "connected", &now)

	_ = db.RecordHealthCheck(&db.HealthCheck{
		Subject:   subject,
		OK:        true,
		LatencyMS: latency,
	})
	s.emitTransition(m, "ok", "")
}

// maybeRecover triggers `restart-rathole` via the agent when:
//   - auto-recovery is enabled, AND
//   - we haven't already triggered a recovery for this machine within the cooldown window.
func (s *HealthService) maybeRecover(m *db.Machine, reason string) {
	if !s.autoRecover {
		return
	}
	s.mu.Lock()
	if last, ok := s.lastRecovery[m.ID]; ok && time.Since(last) < healthRecoveryCooldown {
		s.mu.Unlock()
		return
	}
	s.lastRecovery[m.ID] = time.Now()
	s.mu.Unlock()

	go func() {
		log.Printf("health: triggering rathole restart for machine %s (%s): %s", m.ID, m.Name, reason)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		client := NewAgentClient(m)
		if err := client.RestartRathole(ctx); err != nil {
			log.Printf("health: restart-rathole on %s failed: %v", m.ID, err)
			def := db.LookupKindDefault("recovery_failed")
			db.RecordEvent(&db.Event{
				Severity:     def.Severity,
				Source:       "machine",
				Kind:         "recovery_failed",
				Actor:        "system",
				ResourceType: "machine",
				ResourceID:   m.ID,
				ResourceName: m.Name,
				Message:      fmt.Sprintf(def.MessageTemplate, m.Name),
				Metadata:     fmt.Sprintf(`{"reason":%q,"error":%q}`, reason, err.Error()),
			})
			return
		}
		// Record the recovery attempt + an immediate re-check. We don't emit
		// a "machine_recovered" event here yet — that follows on the next
		// successful poll, where emitTransition can confirm the restart
		// actually brought rathole back up. That avoids false-positive
		// "recovered" events when the restart command succeeds but rathole
		// fails to start.
		_ = db.RecordHealthCheck(&db.HealthCheck{
			Subject:   "machine:" + m.ID,
			OK:        true,
			Recovered: true,
		})
	}()
}

// janitorLoop trims old health-check rows so the table doesn't grow forever.
func (s *HealthService) janitorLoop() {
	t := time.NewTicker(s.purgeInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
			cutoff := time.Now().Add(-s.purgeOlderThan)
			n, err := db.PurgeHealthChecksBefore(cutoff)
			if err != nil {
				log.Printf("health: purge failed: %v", err)
				continue
			}
			if n > 0 {
				log.Printf("health: purged %d old check rows (older than %s)", n, cutoff.Format(time.RFC3339))
			}
		}
	}
}

// CheckMachineNow runs a one-off health check on demand and returns the result.
// Used by the manual "Test now" button.
func (s *HealthService) CheckMachineNow(machineID string) (*db.HealthCheck, error) {
	m, err := db.GetMachine(machineID)
	if err != nil {
		return nil, err
	}
	subject := "machine:" + m.ID
	s.checkMachine(m)
	return db.LatestHealthCheck(subject)
}
