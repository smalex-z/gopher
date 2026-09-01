package service

import (
	"strings"
	"testing"
)

// Field regression (2026-09-01, an OCI install): a record created minutes
// earlier was already correct at the authoritative nameservers, but the box's
// stub, the cloud provider's recursive resolver, and scattered public anycast
// shards served the pre-creation "no such host" for the zone's negative TTL —
// half an hour of hard-red errors and flapping resolver counts over a working
// setup. When authoritative truth says the records exist, cached negatives
// must read as settling warnings, not failures.
func TestReconcileCachedNegatives_AuthoritativeTruthDowngradesCacheFailures(t *testing.T) {
	fail := func(name string) DNSCheck { return DNSCheck{Name: name, Status: DNSCheckFail, Message: "no such host"} }
	auth := authoritativeResult{routerOK: true, wildcardOK: true}

	wildcard, router, propagation := reconcileCachedNegatives(
		fail("wildcard"), fail("router"), DNSCheck{Name: "propagation", Status: DNSCheckWarn, Message: "2 of 3"}, auth)

	for _, c := range []DNSCheck{wildcard, router, propagation} {
		if c.Status != DNSCheckWarn {
			t.Errorf("%s: status = %s, want warn when authoritative proves the record", c.Name, c.Status)
		}
		if !strings.Contains(c.Message, "clears on its own") {
			t.Errorf("%s: message must explain self-healing, got %q", c.Name, c.Message)
		}
	}
}

// Without authoritative confirmation, nothing is downgraded — a genuinely
// missing record must stay loud.
func TestReconcileCachedNegatives_NoAuthoritativeProofKeepsFailures(t *testing.T) {
	fail := DNSCheck{Name: "router", Status: DNSCheckFail, Message: "no such host"}
	auth := authoritativeResult{} // nothing proven (failed or skipped)

	wildcard, router, propagation := reconcileCachedNegatives(
		DNSCheck{Name: "wildcard", Status: DNSCheckFail}, fail, DNSCheck{Name: "propagation", Status: DNSCheckFail}, auth)

	if wildcard.Status != DNSCheckFail || router.Status != DNSCheckFail || propagation.Status != DNSCheckFail {
		t.Errorf("unproven records must keep hard failures, got %s/%s/%s",
			wildcard.Status, router.Status, propagation.Status)
	}
}

// Passing checks are left untouched — reconciliation only rewrites failures.
func TestReconcileCachedNegatives_PassStaysPass(t *testing.T) {
	pass := DNSCheck{Name: "router", Status: DNSCheckPass, Message: "resolves"}
	auth := authoritativeResult{routerOK: true, wildcardOK: true}

	_, router, _ := reconcileCachedNegatives(
		DNSCheck{Name: "wildcard", Status: DNSCheckPass}, pass, DNSCheck{Name: "propagation", Status: DNSCheckPass}, auth)

	if router.Status != DNSCheckPass || router.Message != "resolves" {
		t.Errorf("passing check must be untouched, got %+v", router)
	}
}
