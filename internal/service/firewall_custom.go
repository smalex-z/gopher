package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/db"
)

// gopherCustomChain holds user-defined rules, jumped to before gopherChain.
const gopherCustomChain = "GOPHER_CUSTOM"

// requireGopherMode gates every firewall-mutating entry point: in "manual" or
// "none" mode the operator owns iptables and gopher must neither touch it nor
// accept rules it can't apply (a saved-but-never-applied rule is worse than a
// rejected one — it looks like protection that isn't there).
func requireGopherMode() error {
	settings, err := db.GetSettings()
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	if settings.FirewallMode != "gopher" {
		return ErrFirewallNotManaged
	}
	return nil
}

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
//
// Honours FirewallMode: a no-op (returns nil) when the operator chose
// "manual" or "none". Mirrors ApplyTunnelPort's behaviour — without this
// guard, every custom-rule write through the dashboard silently mutates
// iptables on a host whose owner explicitly opted out of Gopher managing
// the firewall.
func reloadCustomChain() error {
	settings, err := db.GetSettings()
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	if settings.FirewallMode != "gopher" {
		return nil
	}

	sudo := privilegedCmdPrefix()

	if err := ensureCustomChain(); err != nil {
		return err
	}

	// Flush existing rules in the chain.
	flushArgs := append(append([]string{}, sudo...), "iptables", "-F", gopherCustomChain)
	if out, err := exec.Command(flushArgs[0], flushArgs[1:]...).CombinedOutput(); err != nil { // #nosec G204
		return fmt.Errorf("flush %s: %w (%s)", gopherCustomChain, err, strings.TrimSpace(string(out)))
	}

	// Apply structured + raw rules best-effort: the chain was just flushed, so a
	// single failing rule must NOT abort the loop — that would leave the other
	// (valid) rules unapplied. Collect failures and report them, but apply
	// everything that's valid.
	rules, err := db.GetFirewallRules()
	if err != nil {
		return fmt.Errorf("load firewall rules: %w", err)
	}
	var applyErrs []string
	for _, rule := range rules {
		if err := applyStructuredRule(rule, sudo); err != nil {
			applyErrs = append(applyErrs, fmt.Sprintf("rule %s: %v", rule.ID, err))
		}
	}
	if err := applyRawCustomRules(settings.CustomIPTables, sudo); err != nil {
		applyErrs = append(applyErrs, err.Error())
	}

	persistRules()
	if len(applyErrs) > 0 {
		return fmt.Errorf("%d custom rule(s) failed to apply (the rest were applied): %s", len(applyErrs), strings.Join(applyErrs, "; "))
	}
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
	var errs []string
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
		// Best-effort: a bad line is skipped, not allowed to abort the rest.
		if out, err := exec.Command(cmdArgs[0], cmdArgs[1:]...).CombinedOutput(); err != nil { // #nosec G204
			errs = append(errs, fmt.Sprintf("custom rule %q: %v (%s)", line, err, strings.TrimSpace(string(out))))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// -- Public service methods --------------------------------------------------

// FirewallEntry is one row in the unified firewall overview table.
type FirewallEntry struct {
	// "system" | "tunnel" | "machine-ssh" | "custom"
	Type        string `json:"type"`
	ID          string `json:"id,omitempty"` // set for custom rules (FirewallRule.ID)
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

	settings, err := db.GetSettings()
	if err != nil {
		return nil, err
	}

	// 1. Base system rules that gopher sets up on takeover.
	dashboardSource := "0.0.0.0/0"
	if settings.DashboardPrivate {
		dashboardSource = "127.0.0.1"
	}
	for _, e := range []struct {
		port   int
		desc   string
		source string
	}{
		{22, "SSH", "0.0.0.0/0"},
		{80, "HTTP", "0.0.0.0/0"},
		{443, "HTTPS", "0.0.0.0/0"},
		{2333, "Rathole control", "0.0.0.0/0"},
		{dashboardPort, "Gopher dashboard", dashboardSource},
	} {
		entries = append(entries, FirewallEntry{
			Type: "system", Description: e.desc,
			Protocol: "tcp", PortRange: fmt.Sprintf("%d", e.port),
			Source: e.source, Action: "ACCEPT",
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
	if err := requireGopherMode(); err != nil {
		return nil, err
	}
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
	if err := requireGopherMode(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(rawSpec) == "" {
		return nil, fmt.Errorf("raw rule spec cannot be empty")
	}
	// The spec is applied as `iptables -A GOPHER_CUSTOM <spec>`, so an unqualified
	// -j DROP/REJECT here is still a drop-all in a chain that precedes the SSH
	// allow. Reject the lockout shapes.
	if err := rawRuleLockoutCheck(strings.TrimSpace(rawSpec)); err != nil {
		return nil, err
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
//
// Deliberately NOT gated on gopher mode: rules saved before a switch to
// manual/none are latent DB rows, and deleting is the only way to clean them
// up — reloadCustomChain's own mode guard makes the iptables side a no-op.
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

// SetCustomIPTables saves raw custom iptables text and reloads the chain. This
// blob is applied verbatim (lines may name a chain), so it's the strongest
// lockout vector — validate every line before persisting.
func (s *LocalSetupService) SetCustomIPTables(text string) error {
	if err := requireGopherMode(); err != nil {
		return err
	}
	if err := validateRawCustomText(text); err != nil {
		return err
	}
	if err := db.MutateSettings(func(s *db.AppSettings) error {
		s.CustomIPTables = text
		return nil
	}); err != nil {
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
//
// Gated on gopher mode: without this, POST /api/local/firewall/reload on a
// manual/none host would create the GOPHER_TUNNELS chain, jump INPUT to it,
// open every tunnel port, and persistRules() would overwrite the operator's
// own saved ruleset. (The startup caller in cmd/server/main.go checks the
// mode itself, so it never sees this error.)
func (s *LocalSetupService) ReloadFirewall() error {
	if err := requireGopherMode(); err != nil {
		return err
	}
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
	// Reject an unqualified terminating rule. The custom chain is jumped from
	// INPUT position 1 — ahead of the loopback/established/SSH allows — so a
	// DROP/REJECT with neither a port nor a source is a drop-everything that
	// instantly locks the operator out of SSH and the dashboard. A DROP scoped
	// to a port and/or a source (e.g. block an attacker CIDR) is still allowed.
	if action != "ACCEPT" && portRange == "" && (source == "" || source == "0.0.0.0/0") {
		return fmt.Errorf("a %s rule must specify a port and/or a source — an unqualified %s would drop all traffic and lock you out", action, action)
	}
	// Validate source up front — an invalid `-s` value makes the iptables -A
	// fail, and since reloadCustomChain flushes before re-applying, a bad source
	// would otherwise take down every other custom rule with it.
	if source != "" && source != "0.0.0.0/0" {
		if _, _, err := net.ParseCIDR(source); err != nil {
			if net.ParseIP(source) == nil {
				return fmt.Errorf("invalid source %q: expected an IP or CIDR (e.g. 1.2.3.4 or 1.2.3.0/24)", source)
			}
		}
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

// rawRuleLockoutCheck rejects a single raw iptables fragment that could lock the
// operator out of the box. Custom rules only ever belong in the GOPHER_CUSTOM
// chain (jumped from INPUT position 1), so anything that manipulates the
// INPUT/FORWARD/OUTPUT chains directly, changes a default policy, flushes, uses
// a different table, or is an unqualified drop-all is refused. A DROP/REJECT
// scoped by a port and/or source (e.g. ban an attacker CIDR) is still allowed.
func rawRuleLockoutCheck(line string) error {
	f := strings.Fields(line)
	if len(f) == 0 || strings.HasPrefix(f[0], "#") {
		return nil
	}
	switch f[0] {
	case "-P", "-F", "-X", "-N", "-E", "-Z", "-t", "--policy", "--flush", "--table":
		return fmt.Errorf("raw rule may not use %q — custom rules can only add filter rules to the %s chain", f[0], gopherCustomChain)
	}
	if f[0] == "-A" || f[0] == "-I" || f[0] == "-D" || f[0] == "--append" || f[0] == "--insert" || f[0] == "--delete" {
		if len(f) < 2 {
			return fmt.Errorf("raw rule %q is incomplete", line)
		}
		if f[1] != gopherCustomChain {
			return fmt.Errorf("raw rule may only target the %s chain, not %q — that could drop management traffic and lock you out", gopherCustomChain, f[1])
		}
	}
	up := strings.ToUpper(line)
	if strings.Contains(up, "-J DROP") || strings.Contains(up, "-J REJECT") {
		hasMatch := false
		for _, tok := range []string{"-p ", "--protocol", "--dport", "--sport", "-s ", "--source", "-d ", "--destination", "-m ", "-i ", "-o "} {
			if strings.Contains(line, tok) {
				hasMatch = true
				break
			}
		}
		if !hasMatch {
			return fmt.Errorf("raw rule %q is an unqualified DROP/REJECT — it would drop all traffic and lock you out; scope it with a port or source", line)
		}
	}
	return nil
}

// validateRawCustomText runs rawRuleLockoutCheck over every non-empty,
// non-comment line of a raw custom-iptables blob.
func validateRawCustomText(text string) error {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := rawRuleLockoutCheck(line); err != nil {
			return err
		}
	}
	return nil
}

func firewallRuleID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failure in firewallRuleID: %v", err))
	}
	return "fw-" + hex.EncodeToString(b)
}
