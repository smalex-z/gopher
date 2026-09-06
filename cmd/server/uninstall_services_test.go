package main

import "testing"

// The uninstall prompt only fires when the operator actually has custom
// rathole services — comment-only or empty marker blocks must not count,
// or every fresh install would get asked about config it never wrote.
func TestRatholeCustomBlockHasContent(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"no markers", "[server]\nbind_addr = \"0.0.0.0:2333\"\n", false},
		{"empty block", "[server]\n# ===== BEGIN CUSTOM CONFIGURATION =====\n# ===== END CUSTOM CONFIGURATION =====\n", false},
		{"comments only", "[server]\n# ===== BEGIN CUSTOM CONFIGURATION =====\n# Add your own services here.\n\n# ===== END CUSTOM CONFIGURATION =====\n", false},
		{"real service", "[server]\n# ===== BEGIN CUSTOM CONFIGURATION =====\n[server.services.myapp]\nbind_addr = \"0.0.0.0:9999\"\n# ===== END CUSTOM CONFIGURATION =====\n", true},
		{"unterminated block with service", "[server]\n# ===== BEGIN CUSTOM CONFIGURATION =====\n[server.services.myapp]\n", true},
	}
	for _, c := range cases {
		if got := ratholeCustomBlockHasContent(c.content); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
