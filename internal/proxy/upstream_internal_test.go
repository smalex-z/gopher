package proxy

import "testing"

// TestRatholeUpstreamHost guards the post-PoW backend selection. Bot-protected
// tunnels are always private and bind rathole to 127.0.0.1, so forward() must
// dial localhost even when a bind_ip is configured — otherwise it dials
// bind_ip:port (nothing listening) and the browser gets a 502 after solving the
// challenge. (Regression: bot shielding 502 on bind-ip instances.)
func TestRatholeUpstreamHost(t *testing.T) {
	cases := []struct {
		name    string
		private bool
		bindIP  string
		want    string
	}{
		{"private tunnel ignores bind_ip", true, "203.0.113.5", "localhost"},
		{"private tunnel, no bind_ip", true, "", "localhost"},
		{"public tunnel uses bind_ip", false, "203.0.113.5", "203.0.113.5"},
		{"public tunnel, no bind_ip", false, "", "localhost"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ratholeUpstreamHost(c.private, c.bindIP); got != c.want {
				t.Errorf("ratholeUpstreamHost(%v, %q) = %q, want %q", c.private, c.bindIP, got, c.want)
			}
		})
	}
}
