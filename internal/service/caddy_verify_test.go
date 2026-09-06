package service

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/smalex-z/gopher/internal/api/dto"
	"github.com/smalex-z/gopher/internal/db"
)

// selfSignedTLSListener binds 127.0.0.1:0 with a throwaway cert. The probe
// dials with InsecureSkipVerify (a completed handshake is the signal — see
// caddyRouteServing), so the cert's subject doesn't matter.
func selfSignedTLSListener(t *testing.T) net.Listener {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "probe-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"probe-test.example.com"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("tls listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	// Complete handshakes so the client-side dial succeeds.
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				if tc, ok := c.(*tls.Conn); ok {
					_ = tc.Handshake()
				}
				_ = c.Close()
			}(c)
		}
	}()
	return ln
}

func TestCaddyRouteServing_TLS(t *testing.T) {
	ln := selfSignedTLSListener(t)
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	origPort, origDial := caddyHTTPSPort, caddyProbeDial
	caddyHTTPSPort, caddyProbeDial = port, time.Second
	t.Cleanup(func() { caddyHTTPSPort, caddyProbeDial = origPort, origDial })

	if !caddyRouteServing("probe-test.example.com", false, "") {
		t.Error("probe should succeed against a listener that completes the handshake")
	}
	_ = ln.Close()
	if caddyRouteServing("probe-test.example.com", false, "") {
		t.Error("probe should fail once the listener is gone")
	}
}

// Create with a subdomain on a domain-configured edge must come back
// provisioning (#93): the rathole port binds before the URL actually
// serves, and presenting "active" during that window is the bug.
func TestCreateTunnel_SubdomainStartsProvisioning(t *testing.T) {
	initTestDB(t)
	if err := db.MutateSettings(func(s *db.AppSettings) error {
		s.Domain = "example.com"
		return nil
	}); err != nil {
		t.Fatalf("set domain: %v", err)
	}
	// Point the create-time verifier at a dead port with no patience so its
	// goroutine exits immediately (timeout event) instead of outliving the
	// test's in-memory DB.
	origTimeout, origPort, origDial := caddyVerifyTimeout, caddyHTTPSPort, caddyProbeDial
	caddyVerifyTimeout, caddyHTTPSPort, caddyProbeDial = 0, "1", 50*time.Millisecond
	t.Cleanup(func() { caddyVerifyTimeout, caddyHTTPSPort, caddyProbeDial = origTimeout, origPort, origDial })

	seedMachine(t, "m1", 7001)
	svc := NewTunnelService(&fakeLocalOps{})

	routed, err := svc.Create(dto.CreateTunnelRequest{
		MachineID: "m1", Name: "web", LocalPort: 8080, Subdomain: "web",
	})
	if err != nil {
		t.Fatalf("create routed tunnel: %v", err)
	}
	if !routed.CaddyPending {
		t.Error("routed tunnel should start with CaddyPending set")
	}
	got, err := svc.Get(routed.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "provisioning" {
		t.Errorf("presented status = %q, want provisioning", got.Status)
	}

	// No subdomain → no Caddy route → no provisioning window.
	bare, err := svc.Create(dto.CreateTunnelRequest{
		MachineID: "m1", Name: "tcp-only", LocalPort: 5432,
	})
	if err != nil {
		t.Fatalf("create bare tunnel: %v", err)
	}
	if bare.CaddyPending {
		t.Error("subdomain-less tunnel must not be marked provisioning")
	}

	// Clearing the flag drops the provisioning presentation.
	if err := db.SetTunnelCaddyPending(routed.ID, false); err != nil {
		t.Fatalf("clear pending: %v", err)
	}
	got, _ = svc.Get(routed.ID)
	if got.Status == "provisioning" {
		t.Error("status should stop presenting provisioning once the flag clears")
	}
}
