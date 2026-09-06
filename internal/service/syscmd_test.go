package service

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// runOutputCtx must return in bounded time even when the command hangs far
// longer than the timeout — this is the core guarantee that keeps a slow
// journalctl/fail2ban-client from wedging an HTTP handler.
func TestRunOutputCtx_TimesOutOnHangingCommand(t *testing.T) {
	start := time.Now()
	_, err := runOutputCtx(200*time.Millisecond, "sleep", "5")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error for a command that outruns the deadline")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("runOutputCtx did not return promptly on timeout: took %s", elapsed)
	}
}

func TestRunOutputCtx_ReturnsOutputForFastCommand(t *testing.T) {
	out, err := runOutputCtx(2*time.Second, "echo", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "hello\n" {
		t.Fatalf("got %q, want %q", out, "hello\n")
	}
}

// ttlCache must collapse a burst of concurrent callers onto a single fetch —
// this is the property whose absence let the frontend's polling spawn one
// expensive shell-out per request until the box fell over.
func TestTTLCache_ConcurrentCallersCollapseToOneFetch(t *testing.T) {
	c := newTTLCache[int](time.Minute)
	var fetches int32

	fetch := func() (int, error) {
		atomic.AddInt32(&fetches, 1)
		time.Sleep(50 * time.Millisecond) // simulate a slow shell-out
		return 42, nil
	}

	const callers = 50
	var wg sync.WaitGroup
	results := make([]int, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := c.get(fetch)
			if err != nil {
				t.Errorf("caller %d: %v", i, err)
			}
			results[i] = v
		}(i)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&fetches); got != 1 {
		t.Fatalf("expected exactly 1 fetch across %d concurrent callers, got %d", callers, got)
	}
	for i, v := range results {
		if v != 42 {
			t.Fatalf("caller %d got %d, want 42", i, v)
		}
	}
}

// A fresh value is served from cache without re-fetching; once it expires the
// next caller triggers exactly one refresh.
func TestTTLCache_ServesFreshThenRefreshesAfterTTL(t *testing.T) {
	c := newTTLCache[int](80 * time.Millisecond)
	var fetches int32
	fetch := func() (int, error) {
		return int(atomic.AddInt32(&fetches, 1)), nil
	}

	// First call fetches (v=1); immediate second call is cached (still v=1).
	if v, _ := c.get(fetch); v != 1 {
		t.Fatalf("first get = %d, want 1", v)
	}
	if v, _ := c.get(fetch); v != 1 {
		t.Fatalf("cached get = %d, want 1 (should not have re-fetched)", v)
	}
	if got := atomic.LoadInt32(&fetches); got != 1 {
		t.Fatalf("expected 1 fetch while fresh, got %d", got)
	}

	// After the TTL, the next get refreshes exactly once (v=2).
	time.Sleep(120 * time.Millisecond)
	if v, _ := c.get(fetch); v != 2 {
		t.Fatalf("post-expiry get = %d, want 2", v)
	}
	if got := atomic.LoadInt32(&fetches); got != 2 {
		t.Fatalf("expected 2 fetches after expiry, got %d", got)
	}
}
