package service

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/db"
)

// ─── Fail2ban config defaults ─────────────────────────────────────────────────

const (
	defaultMaxRetry = 5
	defaultFindTime = 300
	defaultBanTime  = 3600
)

type Fail2banConfig struct {
	MaxRetry  int      `json:"max_retry"`
	FindTime  int      `json:"find_time"`
	BanTime   int      `json:"ban_time"`
	IgnoreIPs []string `json:"ignore_ips"`
}

// ─── Fail2ban status types ───────────────────────────────────────────────────

type JailStatus struct {
	Name            string   `json:"name"`
	CurrentlyFailed int      `json:"currently_failed"`
	TotalFailed     int      `json:"total_failed"`
	CurrentlyBanned int      `json:"currently_banned"`
	TotalBanned     int      `json:"total_banned"`
	BannedIPs       []string `json:"banned_ips"`
}

type Fail2banStatus struct {
	Available bool         `json:"available"`
	Running   bool         `json:"running"`
	Jails     []JailStatus `json:"jails"`
}

// ─── SecurityService ─────────────────────────────────────────────────────────

type SecurityService struct {
	// Caches for the two expensive, frontend-polled, shell-out-backed read
	// endpoints. Short TTL: the data is diagnostic (ban lists, stale-token
	// attempts) and a few seconds of staleness is invisible to an operator,
	// but it's the difference between one bounded shell-out per ~10s and one
	// per poll per client — the latter is what self-DoS'd the box.
	statusCache *ttlCache[*Fail2banStatus]
	staleCache  *ttlCache[[]StaleTokenAttempt]
}

func NewSecurityService() *SecurityService {
	return &SecurityService{
		statusCache: newTTLCache[*Fail2banStatus](10 * time.Second),
		staleCache:  newTTLCache[[]StaleTokenAttempt](15 * time.Second),
	}
}

// SyncFail2banConfig writes the latest filter files and jail config if fail2ban
// is installed, then reloads it. Called on startup and after updates so every
// deploy picks up config changes without requiring a fresh install.
func (s *SecurityService) SyncFail2banConfig() {
	if !isCommandAvailable("fail2ban-client") {
		return
	}
	if err := writeLocalFile("/etc/fail2ban/filter.d/gopher-auth.conf", fail2banFilterConfig); err != nil {
		return
	}
	cfg, err := s.GetFail2banConfig()
	if err != nil {
		return
	}
	_ = s.rewriteJailConfig(cfg)
}

// GetFail2banConfig reads stored config, filling in defaults for zero values.
func (s *SecurityService) GetFail2banConfig() (*Fail2banConfig, error) {
	settings, err := db.GetSettings()
	if err != nil {
		return nil, err
	}
	cfg := &Fail2banConfig{
		MaxRetry: settings.Fail2banMaxRetry,
		FindTime: settings.Fail2banFindTime,
		BanTime:  settings.Fail2banBanTime,
	}
	if cfg.MaxRetry == 0 {
		cfg.MaxRetry = defaultMaxRetry
	}
	if cfg.FindTime == 0 {
		cfg.FindTime = defaultFindTime
	}
	if cfg.BanTime == 0 {
		cfg.BanTime = defaultBanTime
	}
	if settings.Fail2banIgnoreIPs != "" {
		_ = json.Unmarshal([]byte(settings.Fail2banIgnoreIPs), &cfg.IgnoreIPs)
	}
	return cfg, nil
}

// SaveFail2banConfig persists the config and rewrites the jail file.
func (s *SecurityService) SaveFail2banConfig(cfg *Fail2banConfig) error {
	ipsJSON, err := json.Marshal(cfg.IgnoreIPs)
	if err != nil {
		return err
	}
	if err := db.MutateSettings(func(settings *db.AppSettings) error {
		settings.Fail2banMaxRetry = cfg.MaxRetry
		settings.Fail2banFindTime = cfg.FindTime
		settings.Fail2banBanTime = cfg.BanTime
		settings.Fail2banIgnoreIPs = string(ipsJSON)
		return nil
	}); err != nil {
		return err
	}
	return s.rewriteJailConfig(cfg)
}

// AddIgnoreIP adds an IP/CIDR to the whitelist.
func (s *SecurityService) AddIgnoreIP(ip string) error {
	cfg, err := s.GetFail2banConfig()
	if err != nil {
		return err
	}
	ip = strings.TrimSpace(ip)
	for _, existing := range cfg.IgnoreIPs {
		if existing == ip {
			return nil // already present
		}
	}
	cfg.IgnoreIPs = append(cfg.IgnoreIPs, ip)
	return s.SaveFail2banConfig(cfg)
}

// RemoveIgnoreIP removes an IP/CIDR from the whitelist.
func (s *SecurityService) RemoveIgnoreIP(ip string) error {
	cfg, err := s.GetFail2banConfig()
	if err != nil {
		return err
	}
	filtered := cfg.IgnoreIPs[:0]
	for _, existing := range cfg.IgnoreIPs {
		if existing != ip {
			filtered = append(filtered, existing)
		}
	}
	cfg.IgnoreIPs = filtered
	return s.SaveFail2banConfig(cfg)
}

// Fail2banStatus queries fail2ban-client for status of all managed jails.
// If fail2ban is installed but not running, it is started automatically.
//
// Cached + single-flighted: the frontend Security page polls this on a timer,
// and each uncached call shells out to `fail2ban-client status` once per jail.
// On a busy box those calls stack up and were part of the CPU saturation that
// took the dashboard down; the cache collapses concurrent polls to one refresh
// per TTL window.
func (s *SecurityService) Fail2banStatus() (*Fail2banStatus, error) {
	return s.statusCache.get(s.fetchFail2banStatus)
}

func (s *SecurityService) fetchFail2banStatus() (*Fail2banStatus, error) {
	result := &Fail2banStatus{}
	if !isCommandAvailable("fail2ban-client") {
		return result, nil
	}
	result.Available = true

	// Use systemctl to reliably determine if the service is active, independent
	// of whether the fail2ban socket is ready yet.
	if !isFail2banActive() {
		_ = s.StartFail2ban()
		if !isFail2banActive() {
			return result, nil
		}
	}
	result.Running = true

	// Best-effort: get jail details via fail2ban-client. If the socket isn't
	// ready yet (e.g. still initializing after a fresh start), skip jail data
	// rather than marking the service as not running.
	out, err := runPrivilegedOutput("fail2ban-client", "status")
	if err != nil {
		return result, nil
	}

	jailNames := parseJailList(out)
	for _, name := range jailNames {
		jailOut, err := runPrivilegedOutput("fail2ban-client", "status", name)
		if err != nil {
			continue
		}
		result.Jails = append(result.Jails, parseJailStatus(name, jailOut))
	}
	return result, nil
}

// isFail2banActive returns true when the fail2ban systemd unit is active.
// Bounded: reached from the polled Fail2banStatus handler, so a hung
// systemctl must not wedge the request goroutine.
func isFail2banActive() bool {
	prefix := privilegedCmdPrefix()
	args := append(prefix, "systemctl", "is-active", "--quiet", "fail2ban")
	_, err := runOutputCtx(defaultCmdTimeout, args[0], args[1:]...)
	return err == nil
}

// UnbanIP removes a ban for the given IP from the given jail.
func (s *SecurityService) UnbanIP(jail, ip string) error {
	_, err := runPrivilegedOutput("fail2ban-client", "set", jail, "unbanip", ip)
	return err
}

// StartFail2ban enables and starts the fail2ban service. Bounded (reached from
// the polled Fail2banStatus handler when the unit is down); `enable --now`
// legitimately takes a couple seconds, hence a slightly longer budget than a
// plain status query.
func (s *SecurityService) StartFail2ban() error {
	sudo := privilegedCmdPrefix()
	cmd := append(sudo, "systemctl", "enable", "--now", "fail2ban")
	out, err := runOutputCtx(15*time.Second, cmd[0], cmd[1:]...)
	if err != nil {
		return fmt.Errorf("failed to start fail2ban: %w: %s", err, out)
	}
	return nil
}

// ─── Jail config generation ──────────────────────────────────────────────────

func (s *SecurityService) rewriteJailConfig(cfg *Fail2banConfig) error {
	content := buildJailConfig(cfg)
	if err := writeLocalFile("/etc/fail2ban/jail.d/gopher.conf", content); err != nil {
		return fmt.Errorf("failed to write fail2ban jail: %w", err)
	}
	// Reload fail2ban to pick up the changes. Bounded so a wedged
	// fail2ban-client can't hang the config-save request.
	sudo := privilegedCmdPrefix()
	cmd := append(sudo, "fail2ban-client", "reload")
	_, _ = runOutputCtx(defaultCmdTimeout, cmd[0], cmd[1:]...)
	return nil
}

func buildJailConfig(cfg *Fail2banConfig) string {
	var b strings.Builder
	b.WriteString("[sshd]\nenabled = true\nmode    = aggressive\n\n")
	b.WriteString("[gopher-auth]\n")
	b.WriteString("enabled  = true\n")
	b.WriteString("filter   = gopher-auth\n")
	b.WriteString("backend  = systemd\n")
	b.WriteString("journalmatch = _SYSTEMD_UNIT=gopher.service\n")
	fmt.Fprintf(&b, "maxretry = %d\n", cfg.MaxRetry)
	fmt.Fprintf(&b, "findtime = %d\n", cfg.FindTime)
	fmt.Fprintf(&b, "bantime  = %d\n", cfg.BanTime)
	if len(cfg.IgnoreIPs) > 0 {
		fmt.Fprintf(&b, "ignoreip = 127.0.0.1/8 ::1 %s\n", strings.Join(cfg.IgnoreIPs, " "))
	} else {
		b.WriteString("ignoreip = 127.0.0.1/8 ::1\n")
	}
	b.WriteString("action   = iptables-allports[name=gopher, protocol=all]\n")
	return b.String()
}

// ─── Stale token detection ────────────────────────────────────────────────────

// StaleTokenAttempt represents a rathole client repeatedly connecting with a
// service name that no longer exists in the server config.
type StaleTokenAttempt struct {
	Token    string    `json:"token"`     // full service name (token hash)
	IP       string    `json:"ip"`        // source IP
	LastSeen time.Time `json:"last_seen"` // most recent attempt
	Count    int       `json:"count"`     // total attempts in the journal window
}

var staleTokenRe = regexp.MustCompile(`(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z)\s+ERROR connection\{addr=(\d+\.\d+\.\d+\.\d+):\d+\}.*No such a service\s+(\S+)`)

// StaleTokenAttempts scans the rathole-server journal for "No such a service"
// errors and returns them grouped by (token, ip), sorted by last seen descending.
// Returns an empty slice if journalctl is unavailable or the unit has no logs.
func (s *SecurityService) StaleTokenAttempts() ([]StaleTokenAttempt, error) {
	return s.staleCache.get(s.fetchStaleTokenAttempts)
}

// fetchStaleTokenAttempts is the uncached body, called at most once per TTL
// window (and never concurrently) via staleCache. The journalctl scan below
// is the exact call that took 8–15s on a scanner-flooded box and, being
// unbounded and re-run on every poll, took the dashboard down — it is now
// both time-bounded (runOutputCtx) and cached (see StaleTokenAttempts).
func (s *SecurityService) fetchStaleTokenAttempts() ([]StaleTokenAttempt, error) {
	prefix := privilegedCmdPrefix()
	args := append(prefix, "journalctl", "-u", "rathole-server", "--no-pager", "-n", "2000", "--output=short-iso")
	out, err := runOutputCtx(defaultCmdTimeout, args[0], args[1:]...)
	if err != nil {
		// journalctl exits non-zero when the unit doesn't exist yet, or times
		// out on a huge journal — treat as empty rather than surfacing an error
		// (this is a best-effort diagnostic panel, not core functionality).
		return []StaleTokenAttempt{}, nil
	}

	type key struct{ token, ip string }
	byKey := map[key]*StaleTokenAttempt{}
	var order []key

	for _, line := range strings.Split(string(out), "\n") {
		m := staleTokenRe.FindStringSubmatch(line)
		if len(m) != 4 {
			continue
		}
		ts, ip, token := m[1], m[2], m[3]
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			t, _ = time.Parse("2006-01-02T15:04:05Z", ts)
		}
		k := key{token, ip}
		if e, ok := byKey[k]; ok {
			e.Count++
			if t.After(e.LastSeen) {
				e.LastSeen = t
			}
		} else {
			byKey[k] = &StaleTokenAttempt{Token: token, IP: ip, LastSeen: t, Count: 1}
			order = append(order, k)
		}
	}

	result := make([]StaleTokenAttempt, 0, len(order))
	for _, k := range order {
		result = append(result, *byKey[k])
	}
	// sort newest-first
	for i := 0; i < len(result)-1; i++ {
		for j := i + 1; j < len(result); j++ {
			if result[j].LastSeen.After(result[i].LastSeen) {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	return result, nil
}

// ─── Parsing helpers ─────────────────────────────────────────────────────────

var jailListRe = regexp.MustCompile(`Jail list:\s+(.+)`)

func parseJailList(out string) []string {
	m := jailListRe.FindStringSubmatch(out)
	if len(m) < 2 {
		return nil
	}
	var names []string
	for _, n := range strings.Split(m[1], ",") {
		n = strings.TrimSpace(n)
		if n != "" {
			names = append(names, n)
		}
	}
	return names
}

var (
	currentlyFailedRe = regexp.MustCompile(`Currently failed:\s+(\d+)`)
	totalFailedRe     = regexp.MustCompile(`Total failed:\s+(\d+)`)
	currentlyBannedRe = regexp.MustCompile(`Currently banned:\s+(\d+)`)
	totalBannedRe     = regexp.MustCompile(`Total banned:\s+(\d+)`)
	bannedIPListRe    = regexp.MustCompile(`Banned IP list:\s+(.*)`)
)

func parseJailStatus(name, out string) JailStatus {
	js := JailStatus{Name: name}
	if m := currentlyFailedRe.FindStringSubmatch(out); len(m) == 2 {
		js.CurrentlyFailed, _ = strconv.Atoi(m[1])
	}
	if m := totalFailedRe.FindStringSubmatch(out); len(m) == 2 {
		js.TotalFailed, _ = strconv.Atoi(m[1])
	}
	if m := currentlyBannedRe.FindStringSubmatch(out); len(m) == 2 {
		js.CurrentlyBanned, _ = strconv.Atoi(m[1])
	}
	if m := totalBannedRe.FindStringSubmatch(out); len(m) == 2 {
		js.TotalBanned, _ = strconv.Atoi(m[1])
	}
	if m := bannedIPListRe.FindStringSubmatch(out); len(m) == 2 {
		for _, ip := range strings.Fields(m[1]) {
			if ip != "" {
				js.BannedIPs = append(js.BannedIPs, ip)
			}
		}
	}
	return js
}

// runPrivilegedOutput runs a command with sudo if needed and returns combined
// output, bounded by defaultCmdTimeout. This is reached from HTTP handlers
// (fail2ban status polling), so it MUST NOT be able to block a request
// goroutine indefinitely — see runOutputCtx / the outage note there.
func runPrivilegedOutput(name string, args ...string) (string, error) {
	prefix := privilegedCmdPrefix()
	fullArgs := append(prefix, name)
	fullArgs = append(fullArgs, args...)
	return runOutputCtx(defaultCmdTimeout, fullArgs[0], fullArgs[1:]...)
}
