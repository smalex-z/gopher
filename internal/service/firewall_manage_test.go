package service

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/smalex-z/gopher/internal/db"
)

// stepIndex returns the index of the first step whose joined args contain every
// substring in `want`, or -1 if no step matches.
func stepIndex(steps [][]string, want ...string) int {
	for i, step := range steps {
		joined := strings.Join(step, " ")
		all := true
		for _, w := range want {
			if !strings.Contains(joined, w) {
				all = false
				break
			}
		}
		if all {
			return i
		}
	}
	return -1
}

// TestFirewallInitRuleOrdering locks the anti-lockout invariant: on a re-run
// (INPUT already DROP), the sequence must open INPUT to ACCEPT before flushing,
// lay down every allow (loopback, established, SSH/HTTP/HTTPS/rathole) before
// the default-deny policy, and set INPUT DROP only after all of them. Getting
// this wrong locks the operator out of their VPS.
func TestFirewallInitRuleOrdering(t *testing.T) {
	for _, sudo := range [][]string{nil, {"sudo", "-n"}} {
		steps := firewallInitRuleSteps(sudo)

		acceptBeforeFlush := stepIndex(steps, "iptables -P INPUT ACCEPT")
		flush := stepIndex(steps, "iptables -F")
		loopback := stepIndex(steps, "-i lo -j ACCEPT")
		established := stepIndex(steps, "ESTABLISHED,RELATED")
		ssh := stepIndex(steps, "--dport 22 -j ACCEPT")
		http := stepIndex(steps, "--dport 80 -j ACCEPT")
		https := stepIndex(steps, "--dport 443 -j ACCEPT")
		rathole := stepIndex(steps, "--dport 2333 -j ACCEPT")
		inputDrop := stepIndex(steps, "iptables -P INPUT DROP")
		forwardDrop := stepIndex(steps, "iptables -P FORWARD DROP")

		// Every required step must be present.
		for name, idx := range map[string]int{
			"INPUT ACCEPT": acceptBeforeFlush, "flush": flush, "loopback": loopback,
			"established": established, "ssh": ssh, "http": http, "https": https,
			"rathole": rathole, "INPUT DROP": inputDrop, "FORWARD DROP": forwardDrop,
		} {
			if idx < 0 {
				t.Fatalf("sudo=%v: missing required step %q", sudo, name)
			}
		}

		// 1. INPUT is opened to ACCEPT before the flush (so the flush can't strip
		//    the SSH allow while DROP is still the policy).
		if !(acceptBeforeFlush < flush) {
			t.Errorf("sudo=%v: INPUT ACCEPT (%d) must precede flush (%d)", sudo, acceptBeforeFlush, flush)
		}

		// 2. The default-deny INPUT DROP must come AFTER every allow rule.
		for name, idx := range map[string]int{
			"loopback": loopback, "established": established, "ssh": ssh,
			"http": http, "https": https, "rathole": rathole,
		} {
			if !(idx < inputDrop) {
				t.Errorf("sudo=%v: allow %q (%d) must precede INPUT DROP (%d)", sudo, name, idx, inputDrop)
			}
		}

		// 3. INPUT DROP is the last INPUT-affecting step — nothing appends to
		//    INPUT after the policy flips to DROP.
		for i := inputDrop + 1; i < len(steps); i++ {
			joined := strings.Join(steps[i], " ")
			if strings.Contains(joined, "-A INPUT") {
				t.Errorf("sudo=%v: step %d appends to INPUT after DROP policy: %q", sudo, i, joined)
			}
		}

		// 4. FORWARD is also default-deny.
		if forwardDrop < 0 {
			t.Errorf("sudo=%v: missing FORWARD DROP", sudo)
		}
	}
}

// TestSSHRateLimitDropSpec verifies the public-SSH brute-force defense rule is
// well-formed: a per-source-IP hashlimit DROP on NEW connections, with a
// per-port hashlimit name so different SSH ports don't share a bucket. The -A
// and -D paths both build from this one spec, so this is the single source of
// truth for that rule.
func TestSSHRateLimitDropSpec(t *testing.T) {
	const port = 2222
	spec := strings.Join(sshRateLimitDropSpec(strconv.Itoa(port)), " ")

	for _, want := range []string{
		"--dport 2222",
		"--ctstate NEW",
		"-m hashlimit",
		"--hashlimit-mode srcip",
		"--hashlimit-name gopher_ssh_2222", // per-port bucket
		"-j DROP",
	} {
		if !strings.Contains(spec, want) {
			t.Errorf("sshRateLimitDropSpec missing %q\n  got: %s", want, spec)
		}
	}

	// Distinct ports must get distinct hashlimit buckets, else one port's brute
	// force would rate-limit another's legitimate traffic.
	other := strings.Join(sshRateLimitDropSpec("2200"), " ")
	if strings.Contains(other, "gopher_ssh_2222") {
		t.Error("different ports share a hashlimit bucket name")
	}
}

// Regression for a QA-sweep finding: ApplyTunnelPort/RevokeTunnelPort/
// ApplyDashboardPort/ApplyPublicSSHPort were void — a failed iptables call
// only ever reached log.Printf, never the caller, never the DB, and with no
// periodic firewall reconciliation loop anywhere, that failure had no path to
// visibility or self-healing. recordFirewallFailure is the fix: it must both
// return a real error (so a caller that checks can react) AND persist a
// firewall_apply_failed event (so callers that don't check — most of the
// existing non-fatal call sites — still make the failure visible somewhere
// an operator will actually look).
func TestRecordFirewallFailure_ReturnsErrorAndPersistsEvent(t *testing.T) {
	initTestDB(t)

	cause := errors.New("iptables: no chain/target/match by that name")
	err := recordFirewallFailure(9443, "tcp", "restrict", cause)

	if err == nil {
		t.Fatal("expected a non-nil error")
	}
	if !strings.Contains(err.Error(), "9443") || !strings.Contains(err.Error(), "restrict") || !errors.Is(err, cause) {
		t.Errorf("returned error should mention the port/action and wrap the cause, got: %v", err)
	}

	events, gerr := db.GetRecentEvents(10)
	if gerr != nil {
		t.Fatalf("GetRecentEvents: %v", gerr)
	}
	var found *db.Event
	for i := range events {
		if events[i].Kind == "firewall_apply_failed" {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected a firewall_apply_failed event to be persisted, got events: %+v", events)
	}
	if found.Severity != "error" {
		t.Errorf("expected severity=error, got %q", found.Severity)
	}
	if found.Source != "firewall" {
		t.Errorf("expected source=firewall, got %q", found.Source)
	}
	if found.ResourceID != "9443" {
		t.Errorf("expected resource_id=9443, got %q", found.ResourceID)
	}
	if !strings.Contains(found.Message, "restrict") || !strings.Contains(found.Message, cause.Error()) {
		t.Errorf("event message should describe the action and cause, got: %q", found.Message)
	}
}
