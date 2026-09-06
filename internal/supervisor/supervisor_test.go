package supervisor

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// safeBuf is a mutex-guarded buffer — child output is written from exec's
// copying goroutines while the test reads, so the writer must be concurrency-safe.
type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fast returns a supervisor with tiny timings so restart behaviour is testable
// in milliseconds rather than seconds.
func fast(logw *safeBuf, specs ...Spec) *Supervisor {
	s := New(logw, specs...)
	s.MinBackoff = time.Millisecond
	s.MaxBackoff = 5 * time.Millisecond
	s.HealthyAfter = 50 * time.Millisecond
	s.StopGrace = time.Second
	return s
}

// A process that exits immediately must be restarted repeatedly.
func TestSupervisor_RestartsOnExit(t *testing.T) {
	buf := &safeBuf{}
	s := fast(buf, Spec{Name: "quitter", Path: "/bin/true"})
	s.Start(context.Background())
	time.Sleep(80 * time.Millisecond)
	s.Stop()

	st := s.Status()[0]
	if st.Restarts < 2 {
		t.Fatalf("expected >=2 restarts for an instantly-exiting process, got %d", st.Restarts)
	}
}

// Stop must terminate a long-running child promptly (well under StopGrace,
// since SIGTERM kills sleep immediately) and leave the loop stopped.
func TestSupervisor_GracefulStop(t *testing.T) {
	buf := &safeBuf{}
	s := fast(buf, Spec{Name: "sleeper", Path: "/bin/sleep", Args: []string{"30"}})
	s.Start(context.Background())

	// Wait for it to actually be running.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if s.Status()[0].Running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !s.Status()[0].Running {
		t.Fatal("child never reported running")
	}

	start := time.Now()
	s.Stop()
	if elapsed := time.Since(start); elapsed > 900*time.Millisecond {
		t.Fatalf("Stop took %s — SIGTERM should have killed sleep near-instantly", elapsed)
	}
	if s.Status()[0].Running {
		t.Fatal("child still marked running after Stop")
	}
}

// Child output must be forwarded with a per-child line prefix.
func TestSupervisor_PrefixesChildOutput(t *testing.T) {
	buf := &safeBuf{}
	s := fast(buf, Spec{Name: "talker", Path: "/bin/sh", Args: []string{"-c", "echo hello-from-child"}})
	s.Start(context.Background())
	time.Sleep(60 * time.Millisecond)
	s.Stop()

	if got := buf.String(); !strings.Contains(got, "[talker] hello-from-child") {
		t.Fatalf("expected prefixed child output, got:\n%s", got)
	}
}

// lineWriter must hold a partial line until its newline arrives, never emitting
// a prefix mid-line.
func TestLineWriter_BuffersPartialLines(t *testing.T) {
	buf := &safeBuf{}
	lw := &lineWriter{w: buf, prefix: "x"}
	lw.Write([]byte("abc"))      // no newline yet -> nothing emitted
	if buf.String() != "" {
		t.Fatalf("partial line emitted early: %q", buf.String())
	}
	lw.Write([]byte("def\nghi")) // completes line 1; "ghi" still buffered
	if got := buf.String(); got != "[x] abcdef\n" {
		t.Fatalf("got %q, want %q", got, "[x] abcdef\n")
	}
}
