package build

import "testing"

func TestRatholeTargetForUname(t *testing.T) {
	cases := []struct {
		uname   string
		wantTag string
		wantOK  bool
	}{
		{"x86_64", "x86_64-unknown-linux-gnu", true},
		{"aarch64", "aarch64-unknown-linux-musl", true},
		{"armv7l", "armv7-unknown-linux-musleabihf", true}, // 32-bit ARM origins
		{"riscv64", "", false},                             // unsupported
	}
	for _, c := range cases {
		got, ok := RatholeTargetForUname(c.uname)
		if ok != c.wantOK || got.ReleaseTag != c.wantTag {
			t.Errorf("RatholeTargetForUname(%q) = (%q, %v), want (%q, %v)",
				c.uname, got.ReleaseTag, ok, c.wantTag, c.wantOK)
		}
	}
}

func TestEdgeRatholeTargetIsBundled(t *testing.T) {
	// The edge must always be able to run its own arch, so its target has to be
	// one of the embedded set.
	edge := EdgeRatholeTarget()
	found := false
	for _, tt := range RatholeTargets {
		if tt.Name == edge.Name {
			found = true
		}
	}
	if !found {
		t.Fatalf("edge target %q is not in RatholeTargets", edge.Name)
	}
}
