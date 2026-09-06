// Package supervisor runs and keeps alive the child processes Gopher owns —
// Caddy and rathole. Gopher is the parent/supervisor; systemd supervises Gopher
// (Restart=always). The chain of custody is therefore:
//
//	child exits      -> the supervisor restarts it (with backoff)
//	gopher exits     -> systemd restarts gopher -> children respawn
//
// Children are NOT detached into their own process groups: keeping them in
// gopher's process group means systemd's control-group kill (the default
// KillMode) reaps them when gopher stops, and a hard gopher crash can't leave
// orphaned caddy/rathole holding the listen ports. Graceful Stop signals each
// child directly (SIGTERM, then SIGKILL after a grace period).
package supervisor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Spec describes one child process to supervise.
type Spec struct {
	Name string   // short label used in log-line prefixes ("caddy", "rathole")
	Path string   // absolute path to the executable
	Args []string // arguments (without argv[0])
	Env  []string // extra env appended to os.Environ(); nil = inherit only
	Dir  string   // working directory; "" = inherit gopher's
}

// Default supervision timings. Exposed as fields on Supervisor so tests can
// shrink them.
const (
	defaultMinBackoff   = 500 * time.Millisecond
	defaultMaxBackoff   = 30 * time.Second
	defaultHealthyAfter = 10 * time.Second // ran at least this long => reset backoff
	defaultStopGrace    = 10 * time.Second // SIGTERM -> wait -> SIGKILL
)

// State is a point-in-time snapshot of a supervised child.
type State struct {
	Name      string
	Running   bool
	PID       int
	Restarts  int
	LastExit  error
	LastStart time.Time
}

type child struct {
	spec Spec
	mu   sync.Mutex
	st   State
}

// Supervisor runs a fixed set of children until Stop is called. Construct with
// New, optionally tune the *Backoff/Healthy/Grace fields, then Start.
type Supervisor struct {
	MinBackoff   time.Duration
	MaxBackoff   time.Duration
	HealthyAfter time.Duration
	StopGrace    time.Duration

	logw     io.Writer
	children []*child

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New returns a Supervisor for the given specs, writing supervision and child
// output to logw (typically os.Stdout so systemd/journald captures it).
func New(logw io.Writer, specs ...Spec) *Supervisor {
	s := &Supervisor{
		MinBackoff:   defaultMinBackoff,
		MaxBackoff:   defaultMaxBackoff,
		HealthyAfter: defaultHealthyAfter,
		StopGrace:    defaultStopGrace,
		logw:         logw,
	}
	for _, sp := range specs {
		s.children = append(s.children, &child{spec: sp, st: State{Name: sp.Name}})
	}
	return s
}

// Start launches every child and its supervise loop. It returns immediately;
// the loops run until Stop (or the parent ctx) cancels them.
func (s *Supervisor) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	for _, c := range s.children {
		s.wg.Add(1)
		go s.supervise(ctx, c)
	}
}

// Stop signals all children to terminate and blocks until every supervise loop
// has exited. Safe to call once; idempotent thereafter.
func (s *Supervisor) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

// Status returns a snapshot of every child's current state.
func (s *Supervisor) Status() []State {
	out := make([]State, 0, len(s.children))
	for _, c := range s.children {
		c.mu.Lock()
		out = append(out, c.st)
		c.mu.Unlock()
	}
	return out
}

func (s *Supervisor) supervise(ctx context.Context, c *child) {
	defer s.wg.Done()
	backoff := s.MinBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		err := s.runOnce(ctx, c, start)
		ran := time.Since(start)

		c.mu.Lock()
		c.st.Running = false
		c.st.PID = 0
		c.st.LastExit = err
		c.mu.Unlock()

		if ctx.Err() != nil {
			// Intentional shutdown — don't restart, don't log a scary line.
			return
		}

		// A child that stayed up long enough is considered healthy; reset the
		// backoff so a later one-off crash recovers quickly instead of
		// inheriting a long delay.
		if ran >= s.HealthyAfter {
			backoff = s.MinBackoff
		}
		c.mu.Lock()
		c.st.Restarts++
		c.mu.Unlock()
		fmt.Fprintf(s.logw, "[supervisor] %s exited after %s (err=%v); restarting in %s\n",
			c.spec.Name, ran.Round(time.Millisecond), err, backoff)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > s.MaxBackoff {
			backoff = s.MaxBackoff
		}
	}
}

// runOnce starts the process and blocks until it exits on its own or ctx is
// cancelled. On cancellation it terminates the child gracefully (SIGTERM, then
// SIGKILL after StopGrace) and returns ctx.Err().
func (s *Supervisor) runOnce(ctx context.Context, c *child, start time.Time) error {
	cmd := exec.Command(c.spec.Path, c.spec.Args...)
	cmd.Dir = c.spec.Dir
	if c.spec.Env != nil {
		cmd.Env = append(os.Environ(), c.spec.Env...)
	}
	cmd.Stdout = &lineWriter{w: s.logw, prefix: c.spec.Name}
	cmd.Stderr = &lineWriter{w: s.logw, prefix: c.spec.Name}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", c.spec.Name, err)
	}

	c.mu.Lock()
	c.st.Running = true
	c.st.PID = cmd.Process.Pid
	c.st.LastStart = start
	c.mu.Unlock()
	fmt.Fprintf(s.logw, "[supervisor] %s started (pid %d)\n", c.spec.Name, cmd.Process.Pid)

	// Wait in a goroutine so we can react to ctx cancellation. cmd.Wait is
	// called exactly once, here.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(s.StopGrace):
			_ = cmd.Process.Kill()
			<-done
		}
		return ctx.Err()
	}
}

// lineWriter forwards a child's stdout/stderr to the shared log writer,
// prefixing each complete line with the child's name and buffering partial
// lines across writes so a prefix never lands mid-line.
type lineWriter struct {
	w      io.Writer
	prefix string
	mu     sync.Mutex
	buf    []byte
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		fmt.Fprintf(l.w, "[%s] %s\n", l.prefix, l.buf[:i])
		l.buf = l.buf[i+1:]
	}
	return len(p), nil
}
