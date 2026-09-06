package config

import "testing"

// "router" is the dashboard's own vhost (gopher-router.caddy); a tunnel with
// that subdomain shadows the dashboard in Caddy AND gets captured by the gate
// proxy's tunnel resolution — a silent self-lockout.
func TestValidateSubdomain_RejectsReserved(t *testing.T) {
	if err := ValidateSubdomain("router"); err == nil {
		t.Fatal("subdomain \"router\" must be rejected as reserved")
	}
	for _, ok := range []string{"myapp", "router2", "my-router"} {
		if err := ValidateSubdomain(ok); err != nil {
			t.Errorf("ValidateSubdomain(%q) = %v, want nil", ok, err)
		}
	}
}
