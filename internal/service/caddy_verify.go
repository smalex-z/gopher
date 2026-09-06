package service

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/db"
)

// Issue #93: after tunnel create, the rathole port binds (monitor says
// "active") seconds-to-minutes before the public URL actually works — the
// Caddy route needs its reload applied and, for TLS vhosts, a certificate
// issued. Until then visitors get a TLS "internal error" alert. Tunnels
// carry a CaddyPending flag from create/subdomain-change until the probe
// below confirms the route serves; the API presents that as "provisioning".
//
// Package vars, not consts, so tests can shrink the cadence.
var (
	caddyVerifyInterval = 3 * time.Second
	caddyVerifyTimeout  = 90 * time.Second // matches LE issuance worst cases seen in #93
	caddyProbeDial      = 4 * time.Second
	// Injectable for tests — binding real 443/80 needs root.
	caddyHTTPSPort = "443"
	caddyHTTPPort  = "80"
)

// caddyRouteServing reports whether Caddy is serving the tunnel's vhost,
// probed locally so it works before public DNS resolves from this host.
//
// TLS vhosts: a bare handshake against Caddy's 443 with the fqdn as SNI.
// While the certificate is still being obtained Caddy aborts the handshake
// (the exact failure #93 reports), so a completed handshake — regardless of
// who signed the cert, hence InsecureSkipVerify — proves route + cert.
// no_tls vhosts: any HTTP response on :80 for the Host. Coarse — but no_tls
// routes have no issuance wait, so they genuinely serve the moment the
// (synchronous) reload before this probe returns.
func caddyRouteServing(fqdn string, noTLS bool, bindIP string) bool {
	host := bindIP
	if host == "" {
		host = "127.0.0.1"
	}
	if noTLS {
		c := &http.Client{
			Timeout: caddyProbeDial,
			// The probe asks "does Caddy route this Host" — don't chase the
			// app's redirects.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		req, err := http.NewRequest(http.MethodHead, fmt.Sprintf("http://%s/", net.JoinHostPort(host, caddyHTTPPort)), nil)
		if err != nil {
			return false
		}
		req.Host = fqdn
		resp, err := c.Do(req)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return true
	}
	d := &net.Dialer{Timeout: caddyProbeDial}
	conn, err := tls.DialWithDialer(d, "tcp", net.JoinHostPort(host, caddyHTTPSPort), &tls.Config{
		ServerName:         fqdn,
		InsecureSkipVerify: true, // #nosec G402 — see doc comment: handshake completion is the signal, not chain trust
	})
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// tunnelHasHTTPRoute mirrors AddServiceTunnel's condition for writing a
// Caddy block — keep the two in sync.
func tunnelHasHTTPRoute(t *db.Tunnel, domain string) bool {
	return t.Subdomain != "" && domain != "" && t.Transport != "udp"
}

// verifyCaddyServing polls until the route serves, then clears CaddyPending.
// On timeout the flag stays set and the monitor keeps probing each cycle, so
// a slow ACME issuance self-heals; a warn event records the stall for the
// operator. Run via goSafe in a goroutine.
func verifyCaddyServing(tunnelID, subdomain string, noTLS bool) {
	settings, err := db.GetSettings()
	if err != nil || settings.Domain == "" {
		return
	}
	fqdn := subdomain + "." + settings.Domain
	deadline := time.Now().Add(caddyVerifyTimeout)
	for {
		if caddyRouteServing(fqdn, noTLS, settings.BindIP) {
			// Tunnel may have been deleted or re-pointed mid-verify; the
			// partial update is a no-op for a missing row and the monitor
			// re-checks pending rows anyway, so last-write races are benign.
			if err := db.SetTunnelCaddyPending(tunnelID, false); err != nil {
				log.Printf("caddy verify: clear pending for %s: %v", tunnelID, err)
			}
			return
		}
		if time.Now().After(deadline) {
			db.RecordEvent(&db.Event{
				Severity:     "warn",
				Source:       "tunnel",
				Kind:         "tunnel_provisioning_slow",
				ResourceType: "tunnel",
				ResourceID:   tunnelID,
				ResourceName: fqdn,
				Message:      fmt.Sprintf("Tunnel %s is still provisioning after %s — Caddy hasn't served the route yet (certificate issuance stuck? DNS not pointed here?). The monitor keeps checking.", fqdn, caddyVerifyTimeout),
			})
			return
		}
		time.Sleep(caddyVerifyInterval)
	}
}

// presentTunnelStatus caps the presented status at "provisioning" while the
// Caddy route is unverified: the rathole path may genuinely be active, but
// the URL the operator is about to click still throws TLS alerts (#93).
// config-error stays visible — it's the more actionable signal.
func presentTunnelStatus(t *db.Tunnel) {
	if t.CaddyPending && !strings.HasPrefix(t.Status, "config-error") {
		t.Status = "provisioning"
	}
}

// beginCaddyVerification marks the tunnel provisioning and kicks the async
// verifier. Call after a successful Caddy write+reload for a routed tunnel.
// Dev mode skips system-state writes entirely, so there is nothing to probe.
func beginCaddyVerification(t *db.Tunnel, domain string) {
	if devMode || !tunnelHasHTTPRoute(t, domain) {
		return
	}
	if err := db.SetTunnelCaddyPending(t.ID, true); err != nil {
		log.Printf("caddy verify: set pending for %s: %v", t.ID, err)
		return
	}
	t.CaddyPending = true
	id, sub, noTLS := t.ID, t.Subdomain, t.NoTLS
	go goSafe("caddy.verify", func() { verifyCaddyServing(id, sub, noTLS) })
}
