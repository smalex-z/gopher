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
	}
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
// the agent is installed, fallback TCP-to-tunnel-port otherwise.
func (s *HealthService) checkMachine(m *db.Machine) {
	subject := "machine:" + m.ID
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	if m.AgentInstalled && m.AgentRemotePort > 0 {
		s.checkViaAgent(ctx, m, subject, start)
		return
	}
	s.checkViaTCP(ctx, m, subject, start)
}

func (s *HealthService) checkViaAgent(ctx context.Context, m *db.Machine, subject string, start time.Time) {
	client := NewAgentClient(m)
	status, err := client.Status(ctx)
	latency := int(time.Since(start) / time.Millisecond)

	if err != nil {
		_ = db.RecordHealthCheck(&db.HealthCheck{
			Subject:  subject,
			OK:       false,
			LatencyMS: latency,
			ErrorMsg: "agent unreachable: " + err.Error(),
		})
		// Fall back to TCP so the operator still gets some signal.
		s.checkViaTCP(ctx, m, subject, time.Now())
		return
	}

	// Agent reachable — but is rathole healthy on the box?
	if !status.Rathole.Active {
		errMsg := fmt.Sprintf("rathole-client not active (state=%s/%s)", status.Rathole.State, status.Rathole.Substate)
		_ = db.RecordHealthCheck(&db.HealthCheck{
			Subject:  subject,
			OK:       false,
			LatencyMS: latency,
			ErrorMsg: errMsg,
		})
		s.maybeRecover(m, "rathole inactive")
		return
	}

	// Healthy. Update agent_last_seen + agent_version for the dashboard.
	now := time.Now()
	m.AgentLastSeen = &now
	m.AgentVersion = status.AgentVersion
	if m.Status != "connected" {
		m.Status = "connected"
		m.LastSeen = &now
	}
	_ = db.UpdateMachine(m)

	_ = db.RecordHealthCheck(&db.HealthCheck{
		Subject:  subject,
		OK:       true,
		LatencyMS: latency,
	})
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
			Subject:  subject,
			OK:       false,
			LatencyMS: latency,
			ErrorMsg: "tcp probe failed: " + err.Error(),
		})
		// Without the agent we can't auto-restart. Just record the failure.
		return
	}
	_ = conn.Close()

	_ = db.RecordHealthCheck(&db.HealthCheck{
		Subject:  subject,
		OK:       true,
		LatencyMS: latency,
	})
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
			return
		}
		// Record the recovery attempt + an immediate re-check.
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
