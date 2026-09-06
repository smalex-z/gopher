package service

import (
	"testing"

	"github.com/smalex-z/gopher/internal/db"
)

// remote_addr derivation from settings. The Domain fallback must yield
// router.<domain>, never the bare apex — apex DNS frequently points at an
// org's main site on different hosting, and a client aimed there can never
// connect (this bit the noise-migration and dial-home paths in the field).
func TestRatholeHostFromSettings(t *testing.T) {
	cases := []struct {
		name       string
		serverHost string
		domain     string
		want       string
	}{
		{"explicit server host wins verbatim", "gateway.example.com", "example.com", "gateway.example.com"},
		{"server host scheme stripped", "https://router.example.com/", "example.com", "router.example.com"},
		{"domain fallback gets router prefix, never the apex", "", "uclaacm.com", "router.uclaacm.com"},
		{"nothing configured", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ratholeHostFromSettings(&db.AppSettings{ServerHost: tc.serverHost, Domain: tc.domain})
			if got != tc.want {
				t.Errorf("ratholeHostFromSettings(ServerHost=%q, Domain=%q) = %q, want %q", tc.serverHost, tc.domain, got, tc.want)
			}
		})
	}
	if got := ratholeHostFromSettings(nil); got != "" {
		t.Errorf("nil settings = %q, want empty", got)
	}
}
