package service

import "testing"

// The custom chain is jumped from INPUT position 1 (ahead of the SSH/loopback
// allows), so an unqualified DROP/REJECT there is a lockout. These guard the
// structured-rule validator.
func TestValidateFirewallRule_RejectsUnqualifiedDrop(t *testing.T) {
	cases := []struct {
		name                             string
		proto, portRange, source, action string
		wantErr                          bool
	}{
		{"unqualified DROP", "tcp", "", "", "DROP", true},
		{"DROP w/ 0.0.0.0/0 == unqualified", "tcp", "", "0.0.0.0/0", "DROP", true},
		{"unqualified REJECT", "tcp", "", "", "REJECT", true},
		{"DROP scoped by source ok", "tcp", "", "1.2.3.4", "DROP", false},
		{"DROP scoped by port ok", "tcp", "8080", "", "DROP", false},
		{"unqualified ACCEPT ok", "tcp", "", "", "ACCEPT", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFirewallRule(tc.proto, tc.portRange, tc.source, tc.action)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateFirewallRule(%q,%q,%q,%q) err=%v, wantErr=%v",
					tc.proto, tc.portRange, tc.source, tc.action, err, tc.wantErr)
			}
		})
	}
}

// Raw custom rules must not touch INPUT/policy or be an unqualified drop-all.
func TestRawRuleLockoutCheck(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		wantErr bool
	}{
		{"insert INPUT drop-all", "-I INPUT 1 -j DROP", true},
		{"append INPUT drop", "-A INPUT -j DROP", true},
		{"bare unqualified drop", "-j DROP", true},
		{"unqualified drop in custom chain", "-A GOPHER_CUSTOM -j DROP", true},
		{"policy change", "-P INPUT DROP", true},
		{"flush", "-F", true},
		{"nat table", "-t nat -A POSTROUTING -j MASQUERADE", true},
		{"qualified drop in custom chain ok", "-A GOPHER_CUSTOM -s 1.2.3.4 -j DROP", false},
		{"qualified accept ok", "-p tcp --dport 8080 -j ACCEPT", false},
		{"comment ok", "# a comment", false},
		{"empty ok", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rawRuleLockoutCheck(tc.line)
			if (err != nil) != tc.wantErr {
				t.Fatalf("rawRuleLockoutCheck(%q) err=%v, wantErr=%v", tc.line, err, tc.wantErr)
			}
		})
	}
}
