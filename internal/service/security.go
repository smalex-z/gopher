package service

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

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

type SecurityService struct{}

func NewSecurityService() *SecurityService { return &SecurityService{} }

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
	settings, err := db.GetSettings()
	if err != nil {
		return err
	}
	settings.Fail2banMaxRetry = cfg.MaxRetry
	settings.Fail2banFindTime = cfg.FindTime
	settings.Fail2banBanTime = cfg.BanTime
	ipsJSON, err := json.Marshal(cfg.IgnoreIPs)
	if err != nil {
		return err
	}
	settings.Fail2banIgnoreIPs = string(ipsJSON)
	if err := db.SaveSettings(settings); err != nil {
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
func (s *SecurityService) Fail2banStatus() (*Fail2banStatus, error) {
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
func isFail2banActive() bool {
	prefix := privilegedCmdPrefix()
	args := append(prefix, "systemctl", "is-active", "--quiet", "fail2ban")
	err := exec.Command(args[0], args[1:]...).Run() // #nosec G204
	return err == nil
}

// UnbanIP removes a ban for the given IP from the given jail.
func (s *SecurityService) UnbanIP(jail, ip string) error {
	_, err := runPrivilegedOutput("fail2ban-client", "set", jail, "unbanip", ip)
	return err
}

// StartFail2ban enables and starts the fail2ban service.
func (s *SecurityService) StartFail2ban() error {
	sudo := privilegedCmdPrefix()
	cmd := append(sudo, "systemctl", "enable", "--now", "fail2ban")
	out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput() // #nosec G204
	if err != nil {
		return fmt.Errorf("failed to start fail2ban: %w: %s", err, string(out))
	}
	return nil
}

// ─── Jail config generation ──────────────────────────────────────────────────

func (s *SecurityService) rewriteJailConfig(cfg *Fail2banConfig) error {
	content := buildJailConfig(cfg)
	if err := writeLocalFile("/etc/fail2ban/jail.d/gopher.conf", content); err != nil {
		return fmt.Errorf("failed to write fail2ban jail: %w", err)
	}
	// Reload fail2ban to pick up the changes.
	sudo := privilegedCmdPrefix()
	cmd := append(sudo, "fail2ban-client", "reload")
	_ = exec.Command(cmd[0], cmd[1:]...).Run() // #nosec G204 — args are hardcoded
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

// runPrivilegedOutput runs a command with sudo if needed and returns combined output.
func runPrivilegedOutput(name string, args ...string) (string, error) {
	prefix := privilegedCmdPrefix()
	fullArgs := append(prefix, name)
	fullArgs = append(fullArgs, args...)
	out, err := exec.Command(fullArgs[0], fullArgs[1:]...).CombinedOutput() // #nosec G204
	return string(out), err
}
