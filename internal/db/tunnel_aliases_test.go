package db

import "testing"

// Aliases must: parse into AliasList/AllSubdomains, resolve a tunnel by any of
// its hostnames, and count toward the uniqueness set (with self-exclusion).
func TestTunnelAliases_ResolveAndUniqueness(t *testing.T) {
	initTestDB(t)
	if err := CreateMachine(&Machine{ID: "m1", Name: "box"}); err != nil {
		t.Fatalf("seed machine: %v", err)
	}
	if err := CreateTunnel(&Tunnel{ID: "t1", MachineID: "m1", Subdomain: "members", Aliases: `["member","www"]`}); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := GetTunnel("t1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.AliasList) != 2 {
		t.Errorf("AliasList = %v, want 2 entries", got.AliasList)
	}
	if all := got.AllSubdomains(); len(all) != 3 {
		t.Errorf("AllSubdomains = %v, want 3", all)
	}

	for _, label := range []string{"members", "member", "www"} {
		r, err := GetTunnelByAnySubdomain(label)
		if err != nil || r.ID != "t1" {
			t.Errorf("resolve %q: got %v err %v, want t1", label, r, err)
		}
	}
	if _, err := GetTunnelByAnySubdomain("nope"); err == nil {
		t.Error("resolving an unknown label should error")
	}

	used, err := UsedSubdomains("")
	if err != nil {
		t.Fatalf("used: %v", err)
	}
	for _, s := range []string{"members", "member", "www"} {
		if used[s] != "t1" {
			t.Errorf("UsedSubdomains[%q] = %q, want t1", s, used[s])
		}
	}
	if excl, _ := UsedSubdomains("t1"); len(excl) != 0 {
		t.Errorf("UsedSubdomains(exclude t1) = %v, want empty", excl)
	}
}
