package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/db"
)

// gopherCustomChain holds user-defined rules, jumped to before gopherChain.
const gopherCustomChain = "GOPHER_CUSTOM"

// -- Chain lifecycle ---------------------------------------------------------

// ensureCustomChain creates GOPHER_CUSTOM if missing and inserts the INPUT
// jump before the GOPHER_TUNNELS jump (position 1) so custom rules are
// evaluated first.
func ensureCustomChain() error {
	sudo := privilegedCmdPrefix()

	// Create chain if it doesn't exist.
	createArgs := append(append([]string{}, sudo...), "iptables", "-N", gopherCustomChain)
	out, err := exec.Command(createArgs[0], createArgs[1:]...).CombinedOutput() // #nosec G204
	if err != nil && !strings.Contains(string(out), "already exists") && !strings.Contains(string(out), "Chain already exists") {
		return fmt.Errorf("create %s: %w (%s)", gopherCustomChain, err, strings.TrimSpace(string(out)))
	}

	// Add INPUT → GOPHER_CUSTOM jump if not already present.
	checkArgs := append(append([]string{}, sudo...), "iptables", "-C", "INPUT", "-j", gopherCustomChain)
	if exec.Command(checkArgs[0], checkArgs[1:]...).Run() != nil { // #nosec G204
		// Insert at position 1 so it runs before GOPHER_TUNNELS.
		insArgs := append(append([]string{}, sudo...), "iptables", "-I", "INPUT", "1", "-j", gopherCustomChain)
		if out, err := exec.Command(insArgs[0], insArgs[1:]...).CombinedOutput(); err != nil { // #nosec G204
			return fmt.Errorf("insert INPUT → %s: %w (%s)", gopherCustomChain, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// reloadCustomChain flushes GOPHER_CUSTOM and re-applies all structured rules
// and raw custom iptables from the DB.
func reloadCustomChain() error {
	sudo := privilegedCmdPrefix()

	if err := ensureCustomChain(); err != nil {
		return err
	}

	// Flush existing rules in the chain.
	flushArgs := append(append([]string{}, sudo...), "iptables", "-F", gopherCustomChain)
	if out, err := exec.Command(flushArgs[0], flushArgs[1:]...).CombinedOutput(); err != nil { // #nosec G204
		return fmt.Errorf("flush %s: %w (%s)", gopherCustomChain, err, strings.TrimSpace(string(out)))
	}

	// Apply structured rules from DB.
	rules, err := db.GetFirewallRules()
	if err != nil {
		return fmt.Errorf("load firewall rules: %w", err)
	}
	for _, rule := range rules {
		if err := applyStructuredRule(rule, sudo); err != nil {
			return fmt.Errorf("apply rule %s: %w", rule.ID, err)
		}
	}

	// Apply raw custom iptables text from settings.
	settings, err := db.GetSettings()
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	if err := applyRawCustomRules(settings.CustomIPTables, sudo); err != nil {
		return err
	}

	persistRules()
	return nil
}

func applyStructuredRule(rule db.FirewallRule, sudo []string) error {
	if rule.Raw {
		cmdArgs := append(append([]string{}, sudo...), append([]string{"iptables", "-A", gopherCustomChain}, strings.Fields(rule.RawSpec)...)...)
		if out, err := exec.Command(cmdArgs[0], cmdArgs[1:]...).CombinedOutput(); err != nil { // #nosec G204
			return fmt.Errorf("%w (%s)", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	args := append(append([]string{}, sudo...), "iptables", "-A", gopherCustomChain)
	if rule.Protocol != "" && rule.Protocol != "all" {
		args = append(args, "-p", rule.Protocol)
	}
	if rule.PortRange != "" {
		if strings.Contains(rule.PortRange, ":") {
			args = append(args, "-m", "multiport", "--dports", strings.ReplaceAll(rule.PortRange, ":", ","))
		} else {
			args = append(args, "--dport", rule.PortRange)
		}
	}
	if rule.Source != "" && rule.Source != "0.0.0.0/0" {
		args = append(args, "-s", rule.Source)
	}
	args = append(args, "-j", rule.Action)

	if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil { // #nosec G204
		return fmt.Errorf("%w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// applyRawCustomRules parses and executes raw iptables rule specs stored as
// multi-line text. Each non-empty, non-comment line is treated as arguments
// to `iptables -A GOPHER_CUSTOM`. Lines that already start with a chain name
// or "-A"/"-I" are passed through as-is (allowing full flexibility).
func applyRawCustomRules(text string, sudo []string) error {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// If the line doesn't already specify a chain action, prepend -A GOPHER_CUSTOM.
		var cmdArgs []string
		if strings.HasPrefix(line, "-A ") || strings.HasPrefix(line, "-I ") || strings.HasPrefix(line, "-D ") {
			cmdArgs = append(append([]string{}, sudo...), append([]string{"iptables"}, strings.Fields(line)...)...)
		} else {
			cmdArgs = append(append([]string{}, sudo...), append([]string{"iptables", "-A", gopherCustomChain}, strings.Fields(line)...)...)
		}
		if out, err := exec.Command(cmdArgs[0], cmdArgs[1:]...).CombinedOutput(); err != nil { // #nosec G204
			return fmt.Errorf("custom rule %q: %w (%s)", line, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// -- Public service methods --------------------------------------------------

// FirewallEntry is one row in the unified firewall overview table.
type FirewallEntry struct {
	// "system" | "tunnel" | "machine-ssh" | "custom"
	Type        string `json:"type"`
	ID          string `json:"id,omitempty"`   // set for custom rules (FirewallRule.ID)
	Description string `json:"description"`
	Protocol    string `json:"protocol"`
	PortRange   string `json:"port_range"`
	Source      string `json:"source"`
	Action      string `json:"action"`
	Raw         bool   `json:"raw,omitempty"`
	RawSpec     string `json:"raw_spec,omitempty"`
}

// FirewallOverview returns a unified list of all firewall entries:
// system base rules, gopher-managed tunnel/machine ports, and custom rules.
func (s *LocalSetupService) FirewallOverview() ([]FirewallEntry, error) {
	var entries []FirewallEntry

	// 1. Base system rules that gopher sets up on takeover.
	for _, e := range []struct{ port int; desc string }{
		{22, "SSH"}, {80, "HTTP"}, {443, "HTTPS"}, {2333, "Rathole control"}, {dashboardPort, "Gopher dashboard"},
	} {
		entries = append(entries, FirewallEntry{
			Type: "system", Description: e.desc,
			Protocol: "tcp", PortRange: fmt.Sprintf("%d", e.port),
			Source: "0.0.0.0/0", Action: "ACCEPT",
		})
	}

	// 2. Tunnel ports.
	tunnels, err := db.GetTunnels()
	if err != nil {
		return nil, err
	}
	for _, t := range tunnels {
		proto := t.Transport
		if proto == "" {
			proto = "tcp"
		}
		source := "0.0.0.0/0"
		if t.Private {
			source = "127.0.0.1"
		}
		entries = append(entries, FirewallEntry{
			Type: "tunnel", Description: t.Name,
			Protocol: proto, PortRange: fmt.Sprintf("%d", t.RatholePort),
			Source: source, Action: "ACCEPT",
		})
	}

	// 3. Machine SSH ports.
	machines, err := db.GetMachines()
	if err != nil {
		return nil, err
	}
	for _, m := range machines {
		if m.TunnelPort == 0 {
			continue
		}
		source := "127.0.0.1"
		if m.PublicSSH {
			source = "0.0.0.0/0"
		}
		entries = append(entries, FirewallEntry{
			Type: "machine-ssh", Description: m.Name + " SSH",
			Protocol: "tcp", PortRange: fmt.Sprintf("%d", m.TunnelPort),
			Source: source, Action: "ACCEPT",
		})
	}

	// 4. Custom rules.
	rules, err := db.GetFirewallRules()
	if err != nil {
		return nil, err
	}
	for _, r := range rules {
		entries = append(entries, FirewallEntry{
			Type: "custom", ID: r.ID, Description: r.Description,
			Protocol: r.Protocol, PortRange: r.PortRange,
			Source: r.Source, Action: r.Action,
			Raw: r.Raw, RawSpec: r.RawSpec,
		})
	}

	return entries, nil
}

// ListFirewallRules returns all custom rules from the DB.
func (s *LocalSetupService) ListFirewallRules() ([]db.FirewallRule, error) {
	return db.GetFirewallRules()
}

// CreateFirewallRule saves a structured rule and applies it.
func (s *LocalSetupService) CreateFirewallRule(description, protocol, portRange, source, action string) (*db.FirewallRule, error) {
	if err := validateFirewallRule(protocol, portRange, source, action); err != nil {
		return nil, err
	}
	rule := &db.FirewallRule{
		ID:          firewallRuleID(),
		Description: description,
		Protocol:    protocol,
		PortRange:   portRange,
		Source:      source,
		Action:      action,
		CreatedAt:   time.Now(),
	}
	if err := db.CreateFirewallRule(rule); err != nil {
		return nil, err
	}
	if err := reloadCustomChain(); err != nil {
		return rule, fmt.Errorf("rule saved but could not apply: %w", err)
	}
	return rule, nil
}

// CreateRawFirewallRule saves a raw iptables rule spec and applies it.
func (s *LocalSetupService) CreateRawFirewallRule(description, rawSpec string) (*db.FirewallRule, error) {
	if strings.TrimSpace(rawSpec) == "" {
		return nil, fmt.Errorf("raw rule spec cannot be empty")
	}
	rule := &db.FirewallRule{
		ID:          firewallRuleID(),
		Description: description,
		Raw:         true,
		RawSpec:     strings.TrimSpace(rawSpec),
		CreatedAt:   time.Now(),
	}
	if err := db.CreateFirewallRule(rule); err != nil {
		return nil, err
	}
	if err := reloadCustomChain(); err != nil {
		return rule, fmt.Errorf("rule saved but could not apply: %w", err)
	}
	return rule, nil
}

// DeleteFirewallRule removes a rule from the DB and reloads the chain.
func (s *LocalSetupService) DeleteFirewallRule(id string) error {
	if err := db.DeleteFirewallRule(id); err != nil {
		return err
	}
	return reloadCustomChain()
}

// GetCustomIPTables returns the raw custom iptables text.
func (s *LocalSetupService) GetCustomIPTables() (string, error) {
	settings, err := db.GetSettings()
	if err != nil {
		return "", err
	}
	return settings.CustomIPTables, nil
}

// SetCustomIPTables saves raw custom iptables text and reloads the chain.
func (s *LocalSetupService) SetCustomIPTables(text string) error {
	settings, err := db.GetSettings()
	if err != nil {
		return err
	}
	settings.CustomIPTables = text
	if err := db.SaveSettings(settings); err != nil {
		return err
	}
	return reloadCustomChain()
}

// GetLiveRules returns the output of `iptables -L -n --line-numbers` for the
// two gopher chains, suitable for display in the UI.
func (s *LocalSetupService) GetLiveRules() (map[string]string, error) {
	sudo := privilegedCmdPrefix()
	result := map[string]string{}
	for _, chain := range []string{gopherCustomChain, gopherChain} {
		args := append(append([]string{}, sudo...), "iptables", "-L", chain, "-n", "--line-numbers", "-v")
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput() // #nosec G204
		if err != nil {
			result[chain] = "(chain not found — firewall not yet configured)"
			continue
		}
		result[chain] = string(out)
	}
	return result, nil
}

// ReloadFirewall rebuilds both chains from scratch: ensures chain/jump
// exists (deduplicating any stale jumps), flushes both chains, then
// re-applies all tunnel ports and custom rules.
func (s *LocalSetupService) ReloadFirewall() error {
	sudo := privilegedCmdPrefix()
	if err := firewallCreateChain(nil, sudo); err != nil {
		return err
	}
	// Flush GOPHER_TUNNELS so re-applying ports doesn't create duplicates.
	flushArgs := append(append([]string{}, sudo...), "iptables", "-F", gopherChain)
	if out, err := exec.Command(flushArgs[0], flushArgs[1:]...).CombinedOutput(); err != nil { // #nosec G204
		return fmt.Errorf("flush %s: %w (%s)", gopherChain, err, strings.TrimSpace(string(out)))
	}
	if err := reloadCustomChain(); err != nil {
		return err
	}
	if err := firewallOpenExistingTunnelPorts(nil); err != nil {
		return err
	}
	persistRules()
	return nil
}

// -- Helpers -----------------------------------------------------------------

func validateFirewallRule(protocol, portRange, source, action string) error {
	validProtocols := map[string]bool{"tcp": true, "udp": true, "all": true, "icmp": true}
	if !validProtocols[protocol] {
		return fmt.Errorf("protocol must be one of: tcp, udp, all, icmp")
	}
	validActions := map[string]bool{"ACCEPT": true, "DROP": true, "REJECT": true}
	if !validActions[action] {
		return fmt.Errorf("action must be one of: ACCEPT, DROP, REJECT")
	}
	if portRange != "" {
		for _, part := range strings.Split(portRange, ":") {
			for _, p := range strings.Split(part, ",") {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				for _, c := range p {
					if c < '0' || c > '9' {
						return fmt.Errorf("invalid port range: %q", portRange)
					}
				}
			}
		}
	}
	return nil
}

func firewallRuleID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "fw-" + hex.EncodeToString(b)
}
