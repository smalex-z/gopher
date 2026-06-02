package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// DNS preflight: a structured battery of checks run during the setup wizard
// to give the operator a clear picture of *why* DNS is or isn't working —
// not just "DNS not found." Each check returns a pass/warn/fail/skip status
// and a human-readable message; the frontend renders them as a checklist.
//
// Five checks run in parallel:
//
//  1. wildcard       — query a random subdomain to prove `*.<domain>` exists
//                       (vs. router. being an explicit record that happens to
//                       work while the wildcard itself is broken)
//  2. router         — query router.<domain> directly via the system resolver
//  3. propagation    — query router.<domain> against 1.1.1.1, 8.8.8.8, 9.9.9.9
//                       in parallel and check for cross-resolver consensus —
//                       catches the "DNS still propagating" case where the
//                       local cache is correct but public users see stale data
//  4. ip_match       — when the caller provides an expected_ip, confirm the
//                       resolved IPs include it (catches parking-page IPs and
//                       stale records pointing at the wrong host)
//  5. caa            — raw CAA query to see if the domain has restrictive CAA
//                       records that would block Let's Encrypt issuance
//                       (the cause of the Dynadot wildcard cert failure)

type DNSCheckStatus string

const (
	DNSCheckPass DNSCheckStatus = "pass"
	DNSCheckWarn DNSCheckStatus = "warn"
	DNSCheckFail DNSCheckStatus = "fail"
	DNSCheckSkip DNSCheckStatus = "skip"
)

type DNSCheck struct {
	Name    string         `json:"name"`
	Label   string         `json:"label"`
	Status  DNSCheckStatus `json:"status"`
	Message string         `json:"message"`
}

type DNSPreflightResult struct {
	OK         bool       `json:"ok"`
	Host       string     `json:"host"`
	ResolvedTo string     `json:"resolved_to,omitempty"`
	ExpectedIP string     `json:"expected_ip,omitempty"`
	Message    string     `json:"message,omitempty"`
	Checks     []DNSCheck `json:"checks"`
}

const (
	dnsCheckTimeout = 3 * time.Second
	probePrefix     = "gopher-probe-"
)

var publicResolvers = []struct {
	name string
	addr string
}{
	{"Cloudflare", "1.1.1.1"},
	{"Google", "8.8.8.8"},
	{"Quad9", "9.9.9.9"},
}

// RunDNSPreflight runs the full battery in parallel and returns a structured
// result. expectedIP may be empty — when empty, the IP-match check is skipped.
func RunDNSPreflight(ctx context.Context, domain, expectedIP string) DNSPreflightResult {
	domain = strings.TrimSuffix(strings.TrimSpace(domain), ".")
	routerHost := "router." + domain
	probeHost := probePrefix + randomProbeLabel() + "." + domain

	var (
		wg                          sync.WaitGroup
		wildcardCheck, routerCheck  DNSCheck
		propagationCheck, caaCheck  DNSCheck
		wildcardIPs, routerIPs      []string
		resolverResults             map[string][]string
	)

	wg.Add(4)
	go func() {
		defer wg.Done()
		wildcardCheck, wildcardIPs = checkWildcardA(ctx, probeHost, domain)
	}()
	go func() {
		defer wg.Done()
		routerCheck, routerIPs = checkRouterA(ctx, routerHost)
	}()
	go func() {
		defer wg.Done()
		propagationCheck, resolverResults = checkMultiResolver(ctx, routerHost)
	}()
	go func() {
		defer wg.Done()
		caaCheck = checkCAA(ctx, domain)
	}()
	wg.Wait()

	// IP-match check derives from already-resolved data; build it last.
	resolvedIPs := routerIPs
	if len(resolvedIPs) == 0 {
		resolvedIPs = wildcardIPs
	}
	ipMatchCheck := buildIPMatchCheck(resolvedIPs, expectedIP, resolverResults)

	checks := []DNSCheck{
		wildcardCheck,
		routerCheck,
		propagationCheck,
		ipMatchCheck,
		caaCheck,
	}

	// Overall ok: wildcard must resolve AND router must resolve AND no
	// fail-status check that would block cert issuance later (CAA / IP mismatch).
	// Propagation warnings don't block — the user can wait it out.
	ok := wildcardCheck.Status == DNSCheckPass &&
		routerCheck.Status == DNSCheckPass &&
		caaCheck.Status != DNSCheckFail &&
		ipMatchCheck.Status != DNSCheckFail

	resolvedTo := ""
	if len(resolvedIPs) > 0 {
		resolvedTo = resolvedIPs[0]
	}

	// Surface the most informative failure as the top-level message so the
	// existing terse-banner code path still has something to show.
	msg := ""
	switch {
	case routerCheck.Status == DNSCheckFail:
		msg = routerCheck.Message
	case wildcardCheck.Status == DNSCheckFail:
		msg = wildcardCheck.Message
	case ipMatchCheck.Status == DNSCheckFail:
		msg = ipMatchCheck.Message
	case caaCheck.Status == DNSCheckFail:
		msg = caaCheck.Message
	}

	return DNSPreflightResult{
		OK:         ok,
		Host:       routerHost,
		ResolvedTo: resolvedTo,
		ExpectedIP: expectedIP,
		Message:    msg,
		Checks:     checks,
	}
}

func randomProbeLabel() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func checkWildcardA(ctx context.Context, probeHost, domain string) (DNSCheck, []string) {
	ctx, cancel := context.WithTimeout(ctx, dnsCheckTimeout)
	defer cancel()
	ips, err := net.DefaultResolver.LookupHost(ctx, probeHost)
	if err != nil || len(ips) == 0 {
		return DNSCheck{
			Name:    "wildcard",
			Label:   "Wildcard A record",
			Status:  DNSCheckFail,
			Message: fmt.Sprintf("No wildcard A record. Add *.%s at your DNS provider pointing to this server.", domain),
		}, nil
	}
	return DNSCheck{
		Name:    "wildcard",
		Label:   "Wildcard A record",
		Status:  DNSCheckPass,
		Message: fmt.Sprintf("Random subdomain resolves to %s — *.%s is configured.", strings.Join(ips, ", "), domain),
	}, ips
}

func checkRouterA(ctx context.Context, host string) (DNSCheck, []string) {
	ctx, cancel := context.WithTimeout(ctx, dnsCheckTimeout)
	defer cancel()
	ips, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil || len(ips) == 0 {
		msg := fmt.Sprintf("DNS lookup for %s returned no results.", host)
		if err != nil {
			msg = err.Error()
		}
		return DNSCheck{
			Name:    "router",
			Label:   "router subdomain",
			Status:  DNSCheckFail,
			Message: msg,
		}, nil
	}
	return DNSCheck{
		Name:    "router",
		Label:   "router subdomain",
		Status:  DNSCheckPass,
		Message: fmt.Sprintf("%s resolves to %s.", host, strings.Join(ips, ", ")),
	}, ips
}

// checkMultiResolver queries the host against several public resolvers in
// parallel. Returns a check plus the per-resolver IP sets so other checks
// (notably ip_match) can reason about partial-propagation cases.
func checkMultiResolver(ctx context.Context, host string) (DNSCheck, map[string][]string) {
	results := make(map[string][]string)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, r := range publicResolvers {
		r := r
		wg.Add(1)
		go func() {
			defer wg.Done()
			ips := resolveAt(ctx, host, r.addr)
			mu.Lock()
			results[r.name] = ips
			mu.Unlock()
		}()
	}
	wg.Wait()

	answered := 0
	for _, ips := range results {
		if len(ips) > 0 {
			answered++
		}
	}

	if answered == 0 {
		return DNSCheck{
			Name:    "propagation",
			Label:   "Public resolver propagation",
			Status:  DNSCheckFail,
			Message: "No public resolver could resolve " + host + " — DNS hasn't propagated to the public internet yet.",
		}, results
	}

	// All resolvers answered — check for consensus on the IP set.
	if answered == len(publicResolvers) {
		var reference []string
		consensus := true
		for _, ips := range results {
			if reference == nil {
				reference = ips
				continue
			}
			if !sameIPSet(reference, ips) {
				consensus = false
				break
			}
		}
		if consensus {
			return DNSCheck{
				Name:    "propagation",
				Label:   "Public resolver propagation",
				Status:  DNSCheckPass,
				Message: fmt.Sprintf("All %d public resolvers agree on the IP.", len(publicResolvers)),
			}, results
		}
		return DNSCheck{
			Name:    "propagation",
			Label:   "Public resolver propagation",
			Status:  DNSCheckWarn,
			Message: "Public resolvers returned different IPs — DNS is still propagating. New users may see stale data for a few minutes.",
		}, results
	}

	// Some answered, some didn't.
	return DNSCheck{
		Name:    "propagation",
		Label:   "Public resolver propagation",
		Status:  DNSCheckWarn,
		Message: fmt.Sprintf("%d of %d public resolvers returned an answer — propagation in progress.", answered, len(publicResolvers)),
	}, results
}

func buildIPMatchCheck(resolvedIPs []string, expectedIP string, resolverResults map[string][]string) DNSCheck {
	if expectedIP == "" {
		return DNSCheck{
			Name:    "ip_match",
			Label:   "Resolved IP matches server",
			Status:  DNSCheckSkip,
			Message: "Server IP not known — skipping.",
		}
	}
	if len(resolvedIPs) == 0 {
		// Try the public-resolver results as a fallback if the local
		// resolver didn't answer.
		for _, ips := range resolverResults {
			if len(ips) > 0 {
				resolvedIPs = ips
				break
			}
		}
	}
	if len(resolvedIPs) == 0 {
		return DNSCheck{
			Name:    "ip_match",
			Label:   "Resolved IP matches server",
			Status:  DNSCheckSkip,
			Message: "No DNS answer to compare against.",
		}
	}
	for _, ip := range resolvedIPs {
		if ip == expectedIP {
			return DNSCheck{
				Name:    "ip_match",
				Label:   "Resolved IP matches server",
				Status:  DNSCheckPass,
				Message: fmt.Sprintf("DNS points at this server (%s).", expectedIP),
			}
		}
	}
	return DNSCheck{
		Name:   "ip_match",
		Label:  "Resolved IP matches server",
		Status: DNSCheckFail,
		Message: fmt.Sprintf(
			"DNS resolves to %s but this server is %s. Likely a stale record or a parking page at your registrar — delete it and re-add the wildcard.",
			strings.Join(resolvedIPs, ", "),
			expectedIP,
		),
	}
}

// checkCAA queries CAA records for the domain and reports whether Let's Encrypt
// is allowed to issue. CAA isn't part of net.Resolver, so the query is built
// manually via dnsmessage and sent over UDP to a public resolver.
func checkCAA(ctx context.Context, domain string) DNSCheck {
	records, err := lookupCAA(ctx, domain, "1.1.1.1:53")
	if err != nil {
		// Resolver unreachable / network blocked — don't fail the preflight
		// over an inability to *check* CAA. Skip and let cert issuance be
		// the source of truth if there really is a CAA problem.
		return DNSCheck{
			Name:    "caa",
			Label:   "CAA records",
			Status:  DNSCheckSkip,
			Message: "Could not query CAA records: " + err.Error(),
		}
	}
	if len(records) == 0 {
		return DNSCheck{
			Name:    "caa",
			Label:   "CAA records",
			Status:  DNSCheckPass,
			Message: "No CAA records — any CA may issue certificates for this domain.",
		}
	}

	var issueValues []string
	letsEncryptAllowed := false
	for _, r := range records {
		tag := strings.ToLower(r.tag)
		if tag != "issue" && tag != "issuewild" {
			continue
		}
		issueValues = append(issueValues, r.value)
		v := strings.ToLower(strings.TrimSpace(r.value))
		// ";" is the explicit "no issuance allowed" sentinel; anything else
		// is a CA domain we compare against letsencrypt.org.
		if v == "letsencrypt.org" || strings.HasPrefix(v, "letsencrypt.org;") {
			letsEncryptAllowed = true
		}
	}

	if len(issueValues) == 0 {
		// CAA records exist but none of them are issue/issuewild — they're
		// purely advisory (iodef etc.) and don't restrict issuance.
		return DNSCheck{
			Name:    "caa",
			Label:   "CAA records",
			Status:  DNSCheckPass,
			Message: "CAA records present but no issue/issuewild restriction.",
		}
	}

	if !letsEncryptAllowed {
		return DNSCheck{
			Name:   "caa",
			Label:  "CAA records",
			Status: DNSCheckFail,
			Message: fmt.Sprintf(
				"CAA restricts cert issuance to: %s. Let's Encrypt is not in the allowlist — Caddy will fail to obtain certificates. Add a CAA record allowing letsencrypt.org, or remove the existing CAA records.",
				strings.Join(issueValues, ", "),
			),
		}
	}

	return DNSCheck{
		Name:    "caa",
		Label:   "CAA records",
		Status:  DNSCheckPass,
		Message: "CAA records allow Let's Encrypt.",
	}
}

// ---- helpers ----------------------------------------------------------------

// resolveAt queries a specific upstream resolver for A records. Returns nil
// on error (callers use len() to detect failure — caller doesn't care about
// the error type, just whether an answer came back).
func resolveAt(ctx context.Context, host, resolverAddr string) []string {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: dnsCheckTimeout}
			return d.DialContext(ctx, network, net.JoinHostPort(resolverAddr, "53"))
		},
	}
	ctx, cancel := context.WithTimeout(ctx, dnsCheckTimeout)
	defer cancel()
	ips, err := r.LookupHost(ctx, host)
	if err != nil {
		return nil
	}
	return ips
}

func sameIPSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

type caaRecord struct {
	tag   string
	value string
}

// CAA isn't a defined constant in dnsmessage; the IANA type code is 257.
const dnsTypeCAA dnsmessage.Type = 257

// lookupCAA queries a single upstream resolver for the domain's CAA records.
// Builds the DNS message by hand via dnsmessage, sends it over UDP, and parses
// the raw CAA RDATA out of the answer section (dnsmessage doesn't natively
// understand CAA, so unknown RRs come back as UnknownResource with the raw
// rdata bytes — which is exactly what we need).
func lookupCAA(ctx context.Context, domain, resolverAddr string) ([]caaRecord, error) {
	name, err := dnsmessage.NewName(domain + ".")
	if err != nil {
		return nil, fmt.Errorf("invalid domain: %w", err)
	}

	var idBytes [2]byte
	_, _ = rand.Read(idBytes[:])
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{
			ID:               uint16(idBytes[0])<<8 | uint16(idBytes[1]),
			RecursionDesired: true,
		},
		Questions: []dnsmessage.Question{
			{Name: name, Type: dnsTypeCAA, Class: dnsmessage.ClassINET},
		},
	}
	buf, err := msg.Pack()
	if err != nil {
		return nil, fmt.Errorf("pack query: %w", err)
	}

	d := net.Dialer{Timeout: dnsCheckTimeout}
	conn, err := d.DialContext(ctx, "udp", resolverAddr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(dnsCheckTimeout)
	}
	_ = conn.SetDeadline(deadline)

	if _, err := conn.Write(buf); err != nil {
		return nil, err
	}
	reply := make([]byte, 4096)
	n, err := conn.Read(reply)
	if err != nil {
		return nil, err
	}

	var parser dnsmessage.Parser
	if _, err := parser.Start(reply[:n]); err != nil {
		return nil, fmt.Errorf("parse reply: %w", err)
	}
	if err := parser.SkipAllQuestions(); err != nil {
		return nil, fmt.Errorf("skip questions: %w", err)
	}

	var records []caaRecord
	for {
		h, err := parser.AnswerHeader()
		if err == dnsmessage.ErrSectionDone {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("answer header: %w", err)
		}
		if h.Type != dnsTypeCAA {
			if err := parser.SkipAnswer(); err != nil {
				return nil, fmt.Errorf("skip answer: %w", err)
			}
			continue
		}
		raw, err := parser.UnknownResource()
		if err != nil {
			return nil, fmt.Errorf("read CAA rdata: %w", err)
		}
		rec, err := parseCAARData(raw.Data)
		if err != nil {
			// Skip a malformed record rather than failing the whole check.
			continue
		}
		records = append(records, rec)
	}
	return records, nil
}

// parseCAARData decodes a single CAA RDATA blob (RFC 8659 §4.1):
//
//	+0  flags (1 byte)
//	+1  tag length (1 byte)
//	+2  tag (tag-length bytes, ASCII)
//	+   value (remaining bytes, octets)
func parseCAARData(data []byte) (caaRecord, error) {
	if len(data) < 2 {
		return caaRecord{}, fmt.Errorf("CAA rdata too short")
	}
	tagLen := int(data[1])
	if 2+tagLen > len(data) {
		return caaRecord{}, fmt.Errorf("CAA tag length out of range")
	}
	return caaRecord{
		tag:   string(data[2 : 2+tagLen]),
		value: string(data[2+tagLen:]),
	}, nil
}
