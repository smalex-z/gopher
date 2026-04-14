package service

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	loginRateLimit  = 5               // max attempts
	loginRateWindow = 5 * time.Minute // per window
)

type loginRateLimiter struct {
	mu      sync.Mutex
	buckets map[string][]time.Time
}

func newLoginRateLimiter() *loginRateLimiter {
	rl := &loginRateLimiter{buckets: make(map[string][]time.Time)}
	go rl.cleanup()
	return rl
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
	for range ticker.C {
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

// ClientIP extracts the real client IP from a request, respecting
// X-Forwarded-For set by Caddy when the request comes through the proxy.
func ClientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		// X-Forwarded-For may be a comma-separated list; take the first entry.
		parts := splitAndTrim(fwd, ',')
		if len(parts) > 0 && parts[0] != "" {
			return parts[0]
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
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
