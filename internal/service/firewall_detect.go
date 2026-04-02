package service

import (
	"os"
	"os/exec"
	"strings"
)

// FirewallStatus holds detection results for every known firewall manager.
type FirewallStatus struct {
	UFW       UFWStatus       `json:"ufw"`
	Firewalld FirewalldStatus `json:"firewalld"`
	NFTables  NFTablesStatus  `json:"nftables"`
	IPTables  IPTablesStatus  `json:"iptables"`
	// AnyActive is true when at least one firewall manager is currently running.
	AnyActive bool `json:"any_active"`
}

type UFWStatus struct {
	Installed bool `json:"installed"`
	Active    bool `json:"active"`
}

type FirewalldStatus struct {
	Installed bool `json:"installed"`
	Active    bool `json:"active"`
}

type NFTablesStatus struct {
	Installed bool `json:"installed"`
	Active    bool `json:"active"`
	HasConfig bool `json:"has_config"`
}

type IPTablesStatus struct {
	Available bool `json:"available"`
}

// DetectFirewall inspects the local system for active firewall managers.
// All probes are read-only; this function never modifies system state.
func DetectFirewall() *FirewallStatus {
	s := &FirewallStatus{}

	// UFW — try with sudo -n so we can read the real status without prompting.
	s.UFW.Installed = isCommandAvailable("ufw")
	if s.UFW.Installed {
		out, err := exec.Command("sudo", "-n", "ufw", "status").Output() // #nosec G204
		if err == nil {
			s.UFW.Active = strings.Contains(string(out), "Status: active")
		}
	}

	// firewalld
	s.Firewalld.Installed = isCommandAvailable("firewall-cmd")
	if isCommandAvailable("systemctl") {
		out, _ := exec.Command("systemctl", "is-active", "firewalld").Output() // #nosec G204
		s.Firewalld.Active = strings.TrimSpace(string(out)) == "active"
	}

	// nftables
	s.NFTables.Installed = isCommandAvailable("nft")
	if isCommandAvailable("systemctl") {
		out, _ := exec.Command("systemctl", "is-active", "nftables").Output() // #nosec G204
		s.NFTables.Active = strings.TrimSpace(string(out)) == "active"
	}
	_, err := os.Stat("/etc/nftables.conf")
	s.NFTables.HasConfig = err == nil

	// iptables
	_, err = exec.LookPath("iptables")
	s.IPTables.Available = err == nil

	s.AnyActive = s.UFW.Active || s.Firewalld.Active || s.NFTables.Active

	return s
}
