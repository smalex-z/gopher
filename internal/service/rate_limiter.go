package service

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	loginRateLimit  = 10              // max attempts
	loginRateWindow = 5 * time.Minute // per window
)

type loginRateLimiter struct {
	mu       sync.Mutex
	buckets  map[string][]time.Time
	stopCh   chan struct{}
	stopOnce sync.Once
}

func newLoginRateLimiter() *loginRateLimiter {
	rl := &loginRateLimiter{
		buckets: make(map[string][]time.Time),
		stopCh:  make(chan struct{}),
	}
	go rl.cleanup()
	return rl
}

// Stop halts the background cleanup goroutine. Idempotent. The production
// singletons (AuthService, BootstrapService) live for the whole process so
// they never call this; it exists so tests and any future short-lived limiter
// don't leak the ticker goroutine.
func (rl *loginRateLimiter) Stop() {
	rl.stopOnce.Do(func() { close(rl.stopCh) })
}

// record adds an attempt for the given IP and reports whether it is allowed.
func (rl *loginRateLimiter) record(ip string) bool {
	now := time.Now()
	cutoff := now.Add(-loginRateWindow)

	rl.mu.Lock()
	defer rl.mu.Unlock()

	times := rl.buckets[ip]
	// Drop expired entries
	valid := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	rl.buckets[ip] = append(valid, now)
	return len(rl.buckets[ip]) <= loginRateLimit
}

// RetryAfter returns how long until the oldest attempt expires.
func (rl *loginRateLimiter) RetryAfter(ip string) time.Duration {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	times := rl.buckets[ip]
	if len(times) == 0 {
		return 0
	}
	return time.Until(times[0].Add(loginRateWindow))
}

// Reset clears all recorded attempts for the given IP (call after successful login).
func (rl *loginRateLimiter) Reset(ip string) {
	rl.mu.Lock()
	delete(rl.buckets, ip)
	rl.mu.Unlock()
}

// cleanup removes stale buckets every minute to prevent unbounded memory growth.
func (rl *loginRateLimiter) cleanup() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-rl.stopCh:
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-loginRateWindow)
			rl.mu.Lock()
			for ip, times := range rl.buckets {
				var valid []time.Time
				for _, t := range times {
					if t.After(cutoff) {
						valid = append(valid, t)
					}
				}
				if len(valid) == 0 {
					delete(rl.buckets, ip)
				} else {
					rl.buckets[ip] = valid
				}
			}
			rl.mu.Unlock()
		}
	}
}

// ClientIP extracts the real client IP from a request. It trusts
// X-Forwarded-For ONLY when the request came from our local Caddy (a loopback
// peer): Caddy appends the real client as the last XFF entry, while earlier
// entries are client-supplied and forgeable. A direct (non-loopback) connection
// uses its RemoteAddr and ignores XFF entirely — otherwise an attacker could
// spoof XFF to dodge the login lockout or get another IP banned.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			parts := splitAndTrim(fwd, ',')
			if n := len(parts); n > 0 && parts[n-1] != "" {
				return parts[n-1]
			}
		}
	}
	return host
}

func splitAndTrim(s string, sep rune) []string {
	var out []string
	start := 0
	for i, c := range s {
		if c == sep {
			out = append(out, trimSpace(s[start:i]))
			start = i + 1
		}
	}
	out = append(out, trimSpace(s[start:]))
	return out
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
