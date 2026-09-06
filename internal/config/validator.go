package config

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/smalex-z/gopher/internal/db"
)

var subdomainRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9\-]{0,61}[a-z0-9])?$`)

// ValidateSubdomain checks if a string is a valid subdomain
// reservedSubdomains are labels gopher provisions for itself. Without this
// check a tunnel named "router" shadows the dashboard's own router.<domain>
// Caddy block AND gets matched by the gate proxy's tunnel resolution —
// locking the operator out of their dashboard with no error anywhere.
var reservedSubdomains = map[string]bool{
	"router": true, // dashboard vhost (gopher-router.caddy)
}

func ValidateSubdomain(s string) error {
	if !subdomainRegex.MatchString(s) {
		return fmt.Errorf("invalid subdomain: must be lowercase alphanumeric and hyphens only")
	}
	if reservedSubdomains[s] {
		return fmt.Errorf("subdomain %q is reserved for the gopher dashboard", s)
	}
	return nil
}

// ValidatePort checks if a port number is valid for use as a tunnel server port.
// Ports below 1024 are privileged and typically require root — we reject them.
func ValidatePort(p int) error {
	if p < 1024 || p > 65535 {
		return fmt.Errorf("invalid port: must be between 1024 and 65535 (ports below 1024 are privileged)")
	}
	return nil
}

// ValidateRatholeConfig checks that the config matches database state
// Detects parser errors, duplicates, orphaned entries, and missing entries
func ValidateRatholeConfig(configContent string, expectedMachines []db.Machine, expectedTunnels []db.Tunnel) *ValidationResult {
	result := &ValidationResult{
		Valid:      true,
		Errors:     []string{},
		Duplicates: []string{},
		Orphans:    []string{},
		Missing:    []string{},
	}

	// Parse config to extract gopher entries
	parsed := parseRatholeConfig(configContent)

	// Report any parsing errors
	if len(parsed.Errors) > 0 {
		result.Valid = false
		result.Errors = append(result.Errors, parsed.Errors...)
	}

	// Report duplicates
	if len(parsed.DuplicateIDs) > 0 {
		result.Valid = false
		result.Duplicates = append(result.Duplicates, parsed.DuplicateIDs...)
	}

	// Build expected sets from DB
	expectedMachineIDs := make(map[string]*db.Machine)
	expectedAgentMachineIDs := make(map[string]*db.Machine)
	for i := range expectedMachines {
		if expectedMachines[i].RatholeSSHToken != "" && expectedMachines[i].TunnelPort != 0 {
			expectedMachineIDs[expectedMachines[i].ID] = &expectedMachines[i]
		}
		if expectedMachines[i].AgentRatholeToken != "" && expectedMachines[i].AgentRemotePort != 0 {
			expectedAgentMachineIDs[expectedMachines[i].ID] = &expectedMachines[i]
		}
	}

	expectedTunnelIDs := make(map[string]*db.Tunnel)
	for i := range expectedTunnels {
		if expectedTunnels[i].RatholePort != 0 {
			expectedTunnelIDs[expectedTunnels[i].ID] = &expectedTunnels[i]
		}
	}

	// Check for orphaned entries (in config but not in DB)
	for id := range parsed.Machines {
		if _, found := expectedMachineIDs[id]; !found {
			result.Valid = false
			result.Orphans = append(result.Orphans, fmt.Sprintf("machine-%s", id))
			result.Errors = append(result.Errors, fmt.Sprintf("Orphaned machine in config: %s", id))
		}
	}
	for id := range parsed.MachineAgents {
		if _, found := expectedAgentMachineIDs[id]; !found {
			result.Valid = false
			result.Orphans = append(result.Orphans, fmt.Sprintf("machine-agent-%s", id))
			result.Errors = append(result.Errors, fmt.Sprintf("Orphaned machine-agent in config: %s", id))
		}
	}
	for id := range parsed.Tunnels {
		if _, found := expectedTunnelIDs[id]; !found {
			result.Valid = false
			result.Orphans = append(result.Orphans, fmt.Sprintf("tunnel-%s", id))
			result.Errors = append(result.Errors, fmt.Sprintf("Orphaned tunnel in config: %s", id))
		}
	}

	// Check for missing entries (in DB but not in config)
	for id := range expectedMachineIDs {
		if _, found := parsed.Machines[id]; !found {
			result.Valid = false
			result.Missing = append(result.Missing, fmt.Sprintf("machine-%s", id))
			result.Errors = append(result.Errors, fmt.Sprintf("Missing machine in config: %s", id))
		}
	}
	for id := range expectedAgentMachineIDs {
		if _, found := parsed.MachineAgents[id]; !found {
			result.Valid = false
			result.Missing = append(result.Missing, fmt.Sprintf("machine-agent-%s", id))
			result.Errors = append(result.Errors, fmt.Sprintf("Missing machine-agent in config: %s", id))
		}
	}
	for id := range expectedTunnelIDs {
		if _, found := parsed.Tunnels[id]; !found {
			result.Valid = false
			result.Missing = append(result.Missing, fmt.Sprintf("tunnel-%s", id))
			result.Errors = append(result.Errors, fmt.Sprintf("Missing tunnel in config: %s", id))
		}
	}

	// Check for token / port mismatches
	for id, machine := range expectedMachineIDs {
		if entry, found := parsed.Machines[id]; found {
			if entry.Token != machine.RatholeSSHToken {
				result.Valid = false
				result.Errors = append(result.Errors, fmt.Sprintf("Token mismatch for machine %s", id))
			}
			if extractPortFromBindAddr(entry.BindAddr) != machine.TunnelPort {
				result.Valid = false
				result.Errors = append(result.Errors, fmt.Sprintf("Port mismatch for machine %s", id))
			}
			// Defense-in-depth: a private machine SSH tunnel must bind loopback.
			// If a future edit inverted a bind, this catches the public exposure
			// before the config is written.
			if !machine.PublicSSH && !strings.HasPrefix(entry.BindAddr, "127.0.0.1:") {
				result.Valid = false
				result.Errors = append(result.Errors, fmt.Sprintf("Private machine %s must bind loopback, got %q", id, entry.BindAddr))
			}
		}
	}
	for id, machine := range expectedAgentMachineIDs {
		if entry, found := parsed.MachineAgents[id]; found {
			if entry.Token != machine.AgentRatholeToken {
				result.Valid = false
				result.Errors = append(result.Errors, fmt.Sprintf("Token mismatch for machine-agent %s", id))
			}
			if extractPortFromBindAddr(entry.BindAddr) != machine.AgentRemotePort {
				result.Valid = false
				result.Errors = append(result.Errors, fmt.Sprintf("Port mismatch for machine-agent %s", id))
			}
		}
	}
	for id, tunnel := range expectedTunnelIDs {
		if entry, found := parsed.Tunnels[id]; found {
			expectedToken := tunnel.RatholeToken
			if expectedToken == "" {
				expectedToken = tunnel.ID // backward compat
			}
			if entry.Token != expectedToken {
				result.Valid = false
				result.Errors = append(result.Errors, fmt.Sprintf("Token mismatch for tunnel %s", id))
			}
			if extractPortFromBindAddr(entry.BindAddr) != tunnel.RatholePort {
				result.Valid = false
				result.Errors = append(result.Errors, fmt.Sprintf("Port mismatch for tunnel %s", id))
			}
			if tunnel.Private && !strings.HasPrefix(entry.BindAddr, "127.0.0.1:") {
				result.Valid = false
				result.Errors = append(result.Errors, fmt.Sprintf("Private tunnel %s must bind loopback, got %q", id, entry.BindAddr))
			}
		}
	}

	// Duplicate-listener detection. DB uniqueness is per-column-per-table, so a
	// tunnel's rathole_port can collide with a machine's tunnel_port or
	// agent_remote_port across tables — the app-level check races, and rathole
	// would then silently fail to bind the second listener. gopher only ever
	// binds a given port on one host (127.0.0.1, 0.0.0.0, or the single bind_ip),
	// so a shared port always means a real conflict; catch it before writing.
	seenPort := map[int]string{}
	checkPort := func(label, bindAddr string) {
		p := extractPortFromBindAddr(bindAddr)
		if p == 0 {
			return
		}
		if prev, dup := seenPort[p]; dup {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("Duplicate rathole port %d: %s collides with %s", p, label, prev))
		} else {
			seenPort[p] = label
		}
	}
	for id, e := range parsed.Machines {
		checkPort("machine-"+id, e.BindAddr)
	}
	for id, e := range parsed.MachineAgents {
		checkPort("machine-agent-"+id, e.BindAddr)
	}
	for id, e := range parsed.Tunnels {
		checkPort("tunnel-"+id, e.BindAddr)
	}

	return result
}
