package service

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/smalex-z/gopher/internal/paths"
)

// defaultCmdTimeout bounds any shell-out that runs on an HTTP request path.
// A real outage was caused by a handler blocking indefinitely on a slow
// `journalctl`/`fail2ban-client` on a busy box: with no timeout, the request
// goroutine hung until the command returned (8–15s+ each), polls piled up,
// the single CPU saturated, and the whole dashboard went dead. Every
// request-reachable shell-out must be bounded so a slow command fails fast
// instead of wedging a handler.
const defaultCmdTimeout = 6 * time.Second

// runOutputCtx runs name+args with a hard timeout, returning combined output.
// On timeout the process is killed (via CommandContext) and a timeout error is
// returned, so the caller unblocks in bounded time no matter how slow or hung
// the underlying command is. Use this for anything reachable from an HTTP
// handler; the plain exec.Command wrappers are only safe on install/startup/
// background paths where a caller can afford to wait.
func runOutputCtx(timeout time.Duration, name string, args ...string) (string, error) {
	if timeout <= 0 {
		timeout = defaultCmdTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput() // #nosec G204 — callers pass hardcoded argv
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("%s timed out after %s", name, timeout)
	}
	return string(out), err
}

// ── ttlCache: a tiny TTL + single-flight cache ───────────────────────────────
//
// Wraps an expensive fetch (a slow shell-out) so that (a) a fresh result is
// reused for the TTL window instead of re-running the command on every poll,
// and (b) concurrent callers collapse onto ONE in-flight fetch rather than
// each spawning their own — the two properties whose absence caused the
// outage (the frontend polled a 15s shell-out every few seconds and every
// poll spawned a fresh process). The value is returned even on a stale-but-
// present error only when there is no prior good value.
type ttlCache[T any] struct {
	mu      sync.Mutex
	ttl     time.Duration
	val     T
	err     error
	hasVal  bool
	expires time.Time
	// inflight serializes fetches: a caller that arrives while another is
	// fetching waits on this and then sees the fresh cached result, so N
	// concurrent polls trigger at most one command.
	fetching bool
	waiters  *sync.Cond
}

func newTTLCache[T any](ttl time.Duration) *ttlCache[T] {
	c := &ttlCache[T]{ttl: ttl}
	c.waiters = sync.NewCond(&c.mu)
	return c
}

// get returns a cached value if fresh; otherwise runs fetch exactly once
// across all concurrent callers and caches the result for the TTL window.
func (c *ttlCache[T]) get(fetch func() (T, error)) (T, error) {
	c.mu.Lock()
	// Fast path: fresh cached value.
	if c.hasVal && time.Now().Before(c.expires) {
		v, e := c.val, c.err
		c.mu.Unlock()
		return v, e
	}
	// A fetch is already running — wait for it, then return its result.
	if c.fetching {
		for c.fetching {
			c.waiters.Wait()
		}
		v, e := c.val, c.err
		c.mu.Unlock()
		return v, e
	}
	// We own the fetch.
	c.fetching = true
	c.mu.Unlock()

	v, e := fetch()

	c.mu.Lock()
	c.val, c.err, c.hasVal = v, e, true
	c.expires = time.Now().Add(c.ttl)
	c.fetching = false
	c.waiters.Broadcast()
	c.mu.Unlock()
	return v, e
}

// goSafe wraps a goroutine body with panic recovery so a panic in a
// long-running async worker (bootstrap polls, install streams, monitor
// probes) doesn't take down the whole daemon. label is used in the log
// message so operators can match panics to the failing goroutine.
//
// Usage:
//   go goSafe("awaitSSHHealth", func() { ... })
func goSafe(label string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic in %s: %v\n%s", label, r, debug.Stack())
		}
	}()
	fn()
}

// runSudoCommand runs name+args under sudo, captures combined stdout+stderr,
// logs a structured warning on non-zero exit, and returns the wrapped error.
// Replaces the prior `_ = exec.Command("sudo", ...).Run()` pattern that
// silently dropped systemctl/caddy/iptables failures — when a Caddy reload
// fails because of a syntax error in the user's custom block, the API was
// returning success while the subdomain stayed 502'd until somebody read
// `journalctl -u caddy`.
//
// Callers that have an io.Writer hooked to the LogHub should prefer
// runSudoCommandToLog so the failure also reaches the dashboard's live log.
func runSudoCommand(name string, args ...string) error {
	// Bounded (sudoCmdTimeout): several callers run on tunnel/machine mutation
	// request paths (reconcile → caddy reload, rm of managed conf files,
	// systemctl reloads). Individually fast, but a wedged systemd/dbus must
	// not hang the request goroutine — a slow command fails fast instead.
	full := append([]string{name}, args...)
	out, err := runOutputCtx(sudoCmdTimeout, "sudo", full...) // #nosec G204 — args are caller-controlled hardcoded strings
	if err != nil {
		trimmed := strings.TrimSpace(out)
		log.Printf("sudo %s %s: %v (output: %s)", name, strings.Join(args, " "), err, trimmed)
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// sudoCmdTimeout is a generous bound for privileged one-shot mutations
// (systemctl reload, rm, chown). Larger than defaultCmdTimeout because a
// legitimate `systemctl reload` under load can take longer than a status
// query, but still bounded so a hung call can't wedge a handler.
const sudoCmdTimeout = 30 * time.Second

// systemctlReload runs `sudo systemctl reload <unit>`. Convenience wrapper.
func systemctlReload(unit string) error {
	return runSudoCommand("systemctl", "reload", unit)
}

// systemctlReloadOrRestart runs `sudo systemctl reload-or-restart <unit>`.
func systemctlReloadOrRestart(unit string) error {
	return runSudoCommand("systemctl", "reload-or-restart", unit)
}

// caddyReload reloads the running Caddy via its admin API (`caddy reload` posts
// to the admin endpoint), so it works whether Caddy is managed by systemd or by
// gopher's own supervisor — unlike `systemctl reload caddy`, which assumes a
// caddy.service exists. Prefers the bundled binary, falling back to a caddy on
// PATH. No sudo: it only reads its own config and hits the local admin socket.
func caddyReload() error {
	bin := paths.CaddyBin
	if _, err := os.Stat(bin); err != nil {
		if p := findCommandPath("caddy"); p != "" {
			bin = p
		} else {
			return fmt.Errorf("caddy reload: no caddy binary found")
		}
	}
	// Bounded: caddyReload runs on tunnel create/update/delete + reconcile
	// request paths. `caddy reload` posts to the local admin API and is
	// normally sub-second, but must not hang the handler if the admin socket
	// wedges.
	out, err := runOutputCtx(sudoCmdTimeout, bin, "reload", "--config", paths.CaddyfilePath, "--adapter", "caddyfile") // #nosec G204 — fixed args
	if err != nil {
		return fmt.Errorf("caddy reload: %w (%s)", err, strings.TrimSpace(out))
	}
	return nil
}
