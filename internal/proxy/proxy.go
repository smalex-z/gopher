// Package proxy implements the L7 bot-protection middleware. Instead of running
// a separate listener, it wraps the main Gopher http.Handler. Requests whose
// Host header resolves to a bot-protected tunnel subdomain are intercepted;
// all other requests fall through to the normal Gopher handler unchanged.
package proxy

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/smalex-z/gopher/internal/db"
)

const (
	botCookieName     = "gopher_bot_pass"
	authCookieName    = "gopher_auth"
	defaultSessionTTL = 24 * time.Hour
	// powDifficulty is the number of leading zero hex chars required in the
	// SHA-256 hash. This is a PoC-grade speed bump (Alpha): its real job is to
	// filter non-JS bots, which can't run the challenge at all — so difficulty
	// barely affects security and mostly just taxes real browsers. 4 zeros
	// (16 bits ≈ sub-second even with the current async-digest solver) keeps it
	// from being an 8s wall. A proper solver + adaptive difficulty is v0.2.0 work.
	powDifficulty = 4
	// Password-gate brute-force throttle, per source IP.
	authMaxFails   = 8
	authFailWindow = 5 * time.Minute
)

// Middleware holds the ephemeral HMAC signing key. Create once at startup with
// NewMiddleware, then call Wrap to get the http.Handler.
type Middleware struct {
	hmacKey []byte

	authMu    sync.Mutex
	authFail  map[string]*authFailState // per-IP failed password attempts
	authSweep time.Time                 // next expired-entry sweep (see recordAuthFail)
}

type authFailState struct {
	count int
	reset time.Time
}

// NewMiddleware generates a fresh HMAC key and returns the middleware.
// Restarting the process invalidates outstanding cookies (by design).
func NewMiddleware() (*Middleware, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("proxy: generate HMAC key: %w", err)
	}
	return &Middleware{hmacKey: key, authFail: make(map[string]*authFailState)}, nil
}

// Wrap returns an http.Handler that intercepts requests to gated tunnels — bot
// protection and/or password auth — and passes everything else to next. A gated
// request must clear every enabled gate before it reaches the origin.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tunnel := resolveTunnel(r.Host)
		if tunnel == nil || (!tunnel.BotProtectionEnabled && !tunnel.AuthEnabled) {
			next.ServeHTTP(w, r)
			return
		}

		// Gate post-backs.
		if r.URL.Path == "/bot-verify" && tunnel.BotProtectionEnabled {
			m.handleVerify(w, r, tunnel)
			return
		}
		if r.URL.Path == "/auth-verify" && tunnel.AuthEnabled {
			m.handleAuthVerify(w, r, tunnel)
			return
		}

		// Bot gate first (cheap, filters non-JS clients), then the password gate.
		if tunnel.BotProtectionEnabled && !m.gatePassed(r, botCookieName, "bot", tunnel.ID, tunnel.BotProtectionAllowIP, "") {
			serveBotGate(w, r, tunnel)
			return
		}
		if tunnel.AuthEnabled && !m.gatePassed(r, authCookieName, "auth", tunnel.ID, tunnel.AuthAllowIP, tunnel.AuthPasswordHash) {
			serveAuthGate(w, r)
			return
		}
		m.forward(w, r, tunnel)
	})
}

// gatePassed reports whether the request already satisfies a gate — its source
// IP is allowlisted, or it carries a valid, purpose-scoped cookie.
func (m *Middleware) gatePassed(r *http.Request, cookie, purpose, tunnelID, allowIP, bind string) bool {
	if isIPAllowed(allowIP, clientIP(r)) {
		return true
	}
	return m.hasValidCookie(r, cookie, purpose, tunnelID, bind)
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// serveBotGate responds to a request that hasn't cleared bot protection.
func serveBotGate(w http.ResponseWriter, r *http.Request, tunnel *db.Tunnel) {
	if isAPIClient(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"success":false,"error":"bot protection active — complete browser verification first"}`)
		return
	}
	if isWebSocketUpgrade(r) {
		http.Error(w, "403 Forbidden — complete browser verification first", http.StatusForbidden)
		return
	}
	serveChallenge(w, tunnel.ID, powDifficulty)
}

// serveAuthGate responds to a request that hasn't cleared the password gate.
func serveAuthGate(w http.ResponseWriter, r *http.Request) {
	if isAPIClient(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"success":false,"error":"authentication required"}`)
		return
	}
	if isWebSocketUpgrade(r) {
		http.Error(w, "401 Unauthorized — authenticate first", http.StatusUnauthorized)
		return
	}
	serveLogin(w, http.StatusUnauthorized, "", r.URL.RequestURI())
}

func (m *Middleware) handleVerify(w http.ResponseWriter, r *http.Request, tunnel *db.Tunnel) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !checkPoW(r.FormValue("nonce"), r.FormValue("solution"), powDifficulty) {
		http.Error(w, "invalid proof of work", http.StatusForbidden)
		return
	}
	ttl := ttlForTunnel(tunnel)
	m.setSessionCookie(w, botCookieName, "bot", tunnel.ID, "", ttl)
	recordSession(tunnel.ID, r, ttl)
	http.Redirect(w, r, safeRedirect(r.FormValue("redirect"), "/bot-verify"), http.StatusSeeOther)
}

func (m *Middleware) handleAuthVerify(w http.ResponseWriter, r *http.Request, tunnel *db.Tunnel) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	redirect := r.FormValue("redirect")
	ip := clientIP(r)

	if m.authThrottled(ip) {
		serveLogin(w, http.StatusTooManyRequests, "Too many attempts. Wait a minute and try again.", redirect)
		return
	}
	// Constant-time-ish bcrypt compare. An unset hash never matches.
	if tunnel.AuthPasswordHash == "" ||
		bcrypt.CompareHashAndPassword([]byte(tunnel.AuthPasswordHash), []byte(r.FormValue("password"))) != nil {
		m.recordAuthFail(ip)
		serveLogin(w, http.StatusUnauthorized, "Incorrect password.", redirect)
		return
	}
	m.clearAuthFail(ip)

	ttl := authTTLForTunnel(tunnel)
	m.setSessionCookie(w, authCookieName, "auth", tunnel.ID, tunnel.AuthPasswordHash, ttl)
	recordSession(tunnel.ID, r, ttl)
	http.Redirect(w, r, safeRedirect(redirect, "/auth-verify"), http.StatusSeeOther)
}

// setSessionCookie issues a purpose-scoped HMAC token and sets it as a cookie.
func (m *Middleware) setSessionCookie(w http.ResponseWriter, name, purpose, tunnelID, bind string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    m.issueToken(purpose, tunnelID, bind, ttl),
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	})
}

// recordSession writes an audit row for an issued gate session (best-effort).
func recordSession(tunnelID string, r *http.Request, ttl time.Duration) {
	go func() {
		_ = db.CreateBotSession(&db.BotSession{
			ID:        randomHex(8),
			TunnelID:  tunnelID,
			IP:        clientIP(r),
			UserAgent: r.UserAgent(),
			IssuedAt:  time.Now(),
			ExpiresAt: time.Now().Add(ttl),
		})
	}()
}

// safeRedirect restricts the post-gate redirect to same-site, path-relative
// targets. Rejects absolute URLs (https://evil.com), protocol-relative
// (//evil.com), and back-references — otherwise it's an open redirect on the
// tunnel's subdomain.
func safeRedirect(redirect, verifyPath string) string {
	if redirect == "" || redirect == verifyPath || !strings.HasPrefix(redirect, "/") || strings.HasPrefix(redirect, "//") || strings.Contains(redirect, "..") {
		return "/"
	}
	return redirect
}

// ---------------------------------------------------------------------------
// Forwarding
// ---------------------------------------------------------------------------

// ratholeUpstreamHost picks the host to dial for a tunnel's rathole port.
// Private tunnels — which is ALWAYS the case for bot-protected tunnels, since
// bot protection coerces private — bind rathole to 127.0.0.1, so they must be
// reached via localhost. Only public tunnels bind to bind_ip and need
// bind_ip:port here. Mirrors buildTunnelCaddyBlock's upstream selection;
// getting this wrong sends post-challenge traffic to an address the tunnel
// isn't listening on → connection refused → 502.
func ratholeUpstreamHost(private bool, bindIP string) string {
	if !private && bindIP != "" {
		return bindIP
	}
	return "localhost"
}

func (m *Middleware) forward(w http.ResponseWriter, r *http.Request, tunnel *db.Tunnel) {
	bindIP := ""
	if settings, err := db.GetSettings(); err == nil {
		bindIP = settings.BindIP
	}
	ratholeHost := ratholeUpstreamHost(tunnel.Private, bindIP)
	// Mirrors buildTunnelCaddyBlock's tlsSkipVerify && !noTLS condition. Gated
	// tunnels (bot protection and/or password auth) route through here
	// instead of Caddy's own reverse_proxy directive — which is where a
	// self-signed-upstream tunnel normally gets its tls_insecure_skip_verify
	// transport (local_caddyfile.go:93-95 documents that this path doesn't
	// apply once gating is on). Without this, TLS-skip-verify was silently
	// dropped the moment gating was enabled: gates would pass, then the final
	// hop to the upstream broke because this proxy always spoke plain HTTP to
	// a TLS-only backend — Proxmox-style backends that issue their own
	// http-to-https redirect on a bare HTTP hit turn that into an infinite
	// redirect loop, right after the password gate succeeds.
	scheme := "http"
	if tunnel.TLSSkipVerify && !tunnel.NoTLS {
		scheme = "https"
	}
	target, _ := url.Parse(fmt.Sprintf("%s://%s:%d", scheme, ratholeHost, tunnel.RatholePort))
	rp := httputil.NewSingleHostReverseProxy(target)
	if scheme == "https" {
		rp.Transport = &http.Transport{
			// #nosec G402 -- operator opted into this for a known self-signed
			// upstream (e.g. Proxmox); mirrors Caddy's tls_insecure_skip_verify.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}
	rp.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		// Preserve original Host so origin services see the right hostname.
	}
	rp.ServeHTTP(w, r)
}

// ---------------------------------------------------------------------------
// Cookie helpers
// ---------------------------------------------------------------------------

// issueToken mints a purpose-scoped signed token: purpose:tunnelID:exp:hmac.
// The purpose ("bot" / "auth") prevents a cookie from one gate satisfying
// another even though both use the same key and tunnel ID.
//
// bind is an extra secret folded into the HMAC input but never written to the
// token — for the auth gate it's the current password hash, so rotating the
// password changes the signature and invalidates every outstanding cookie.
// Pass "" for gates that don't bind (bot).
func (m *Middleware) issueToken(purpose, tunnelID, bind string, ttl time.Duration) string {
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	exp := time.Now().Add(ttl).Unix()
	payload := fmt.Sprintf("%s:%s:%d", purpose, tunnelID, exp)
	return payload + ":" + hmacSign(m.hmacKey, payload+"|"+bind)
}

func (m *Middleware) validateToken(token, purpose, tunnelID, bind string) bool {
	lastColon := strings.LastIndex(token, ":")
	if lastColon < 0 {
		return false
	}
	mac := token[lastColon+1:]
	payload := token[:lastColon]
	if !hmac.Equal([]byte(mac), []byte(hmacSign(m.hmacKey, payload+"|"+bind))) {
		return false
	}
	parts := strings.SplitN(payload, ":", 3) // purpose : tunnelID : exp
	if len(parts) != 3 || parts[0] != purpose || parts[1] != tunnelID {
		return false
	}
	var exp int64
	if _, err := fmt.Sscan(parts[2], &exp); err != nil {
		return false
	}
	return time.Now().Unix() < exp
}

func (m *Middleware) hasValidCookie(r *http.Request, cookieName, purpose, tunnelID, bind string) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return m.validateToken(c.Value, purpose, tunnelID, bind)
}

func hmacSign(key []byte, data string) string {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// ---------------------------------------------------------------------------
// PoW
// ---------------------------------------------------------------------------

func checkPoW(nonce, solution string, difficulty int) bool {
	if nonce == "" || solution == "" {
		return false
	}
	if _, err := strconv.ParseInt(solution, 10, 64); err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(nonce + ":" + solution))
	return strings.HasPrefix(hex.EncodeToString(sum[:]), strings.Repeat("0", difficulty))
}

// ---------------------------------------------------------------------------
// Tunnel resolution
// ---------------------------------------------------------------------------

func resolveTunnel(host string) *db.Tunnel {
	h := host
	if stripped, _, err := net.SplitHostPort(host); err == nil {
		h = stripped
	}
	parts := strings.SplitN(h, ".", 2)
	if len(parts) < 2 {
		return nil
	}
	t, err := db.GetTunnelBySubdomain(parts[0])
	if err != nil {
		return nil
	}
	return t
}

// ---------------------------------------------------------------------------
// Request helpers
// ---------------------------------------------------------------------------

func isAPIClient(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func clientIP(r *http.Request) string {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host == "" {
		host = r.RemoteAddr
	}
	// Trust X-Forwarded-For only from our local Caddy (loopback peer): Caddy
	// appends the real client as the LAST entry, while earlier entries are
	// client-supplied and forgeable. A direct (non-loopback) connection uses
	// RemoteAddr and ignores XFF — otherwise the IP allowlist is trivially
	// bypassed by spoofing XFF.
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
				return last
			}
		}
	}
	return host
}

func isIPAllowed(allowListJSON, clientIPStr string) bool {
	if allowListJSON == "" || allowListJSON == "[]" {
		return false
	}
	var cidrs []string
	if err := json.Unmarshal([]byte(allowListJSON), &cidrs); err != nil {
		return false
	}
	ip := net.ParseIP(clientIPStr)
	if ip == nil {
		return false
	}
	for _, entry := range cidrs {
		if _, network, err := net.ParseCIDR(entry); err == nil {
			if network.Contains(ip) {
				return true
			}
		} else if net.ParseIP(entry).Equal(ip) {
			return true
		}
	}
	return false
}

func ttlForTunnel(t *db.Tunnel) time.Duration {
	if t.BotProtectionTTL > 0 {
		return time.Duration(t.BotProtectionTTL) * time.Second
	}
	return defaultSessionTTL
}

func authTTLForTunnel(t *db.Tunnel) time.Duration {
	if t.AuthTTL > 0 {
		return time.Duration(t.AuthTTL) * time.Second
	}
	return defaultSessionTTL
}

// ---------------------------------------------------------------------------
// Password-gate brute-force throttle (per source IP, sliding window)
// ---------------------------------------------------------------------------

func (m *Middleware) authThrottled(ip string) bool {
	m.authMu.Lock()
	defer m.authMu.Unlock()
	st := m.authFail[ip]
	if st == nil || time.Now().After(st.reset) {
		return false
	}
	return st.count >= authMaxFails
}

func (m *Middleware) recordAuthFail(ip string) {
	m.authMu.Lock()
	defer m.authMu.Unlock()
	now := time.Now()
	// Opportunistic GC: entries were only ever deleted on a successful login,
	// so distributed failed attempts (each IP failing once) grew the map
	// forever. Sweep expired windows at most once per window.
	if now.After(m.authSweep) {
		for k, v := range m.authFail {
			if now.After(v.reset) {
				delete(m.authFail, k)
			}
		}
		m.authSweep = now.Add(authFailWindow)
	}
	st := m.authFail[ip]
	if st == nil || now.After(st.reset) {
		st = &authFailState{reset: now.Add(authFailWindow)}
		m.authFail[ip] = st
	}
	st.count++
}

func (m *Middleware) clearAuthFail(ip string) {
	m.authMu.Lock()
	defer m.authMu.Unlock()
	delete(m.authFail, ip)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failure in randomHex: %v", err))
	}
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// Challenge page
// ---------------------------------------------------------------------------

func serveChallenge(w http.ResponseWriter, tunnelID string, difficulty int) {
	nonce := strconv.FormatInt(time.Now().UnixMilli(), 10)
	zeroPrefix := strings.Repeat("0", difficulty)

	page := `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Verifying your browser…</title>
  <style>
    *{box-sizing:border-box;margin:0;padding:0}
    body{font-family:system-ui,-apple-system,sans-serif;background:#f8fafc;
         display:flex;align-items:center;justify-content:center;min-height:100vh}
    .card{background:#fff;border-radius:12px;box-shadow:0 4px 24px rgba(0,0,0,.08);
          padding:2.5rem;text-align:center;width:100%;max-width:360px}
    h1{font-size:1.2rem;color:#1e293b;margin-bottom:.5rem}
    p{color:#64748b;font-size:.875rem;margin-bottom:1.5rem}
    .spinner{width:36px;height:36px;border:3px solid #e2e8f0;border-top-color:#3b82f6;
             border-radius:50%;animation:spin .7s linear infinite;margin:0 auto 1.25rem}
    @keyframes spin{to{transform:rotate(360deg)}}
  </style>
</head>
<body>
  <div class="card">
    <div class="spinner"></div>
    <h1>Verifying your browser</h1>
    <p id="status">Running security check…</p>
  </div>
  <form id="f" method="POST" action="/bot-verify" style="display:none">
    <input type="hidden" name="nonce"    value="` + nonce + `">
    <input type="hidden" name="solution" id="sol">
    <input type="hidden" name="redirect" id="redir">
  </form>
  <script>
  (async()=>{
    const nonce="` + nonce + `",prefix="` + zeroPrefix + `",te=new TextEncoder();
    let c=0,t0=Date.now();
    while(true){
      const d=await crypto.subtle.digest("SHA-256",te.encode(nonce+":"+c));
      const h=Array.from(new Uint8Array(d)).map(b=>b.toString(16).padStart(2,"0")).join("");
      if(h.startsWith(prefix)){
        document.getElementById("sol").value=c;
        document.getElementById("redir").value=location.pathname+location.search;
        document.getElementById("status").textContent="Done ("+(Date.now()-t0)+"ms). Redirecting…";
        document.getElementById("f").submit();return;
      }
      c++;
      if(c%50000===0)await new Promise(r=>setTimeout(r,0));
    }
  })();
  </script>
</body>
</html>`

	_ = tunnelID // tunnel resolved from Host header, not embedded in page
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprint(w, page)
}

// ---------------------------------------------------------------------------
// Login page (password gate)
// ---------------------------------------------------------------------------

func serveLogin(w http.ResponseWriter, status int, errMsg, redirect string) {
	// Sanitize + HTML-escape the redirect before embedding it in the form value,
	// so a crafted return path can't break out of the attribute or inject markup.
	redirect = html.EscapeString(safeRedirect(redirect, "/auth-verify"))
	errBlock := ""
	if errMsg != "" {
		errBlock = `<p class="err">` + html.EscapeString(errMsg) + `</p>`
	}
	page := `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Password required</title>
  <style>
    *{box-sizing:border-box;margin:0;padding:0}
    body{font-family:system-ui,-apple-system,sans-serif;background:#f8fafc;
         display:flex;align-items:center;justify-content:center;min-height:100vh}
    .card{background:#fff;border-radius:12px;box-shadow:0 4px 24px rgba(0,0,0,.08);
          padding:2.5rem;text-align:center;width:100%;max-width:360px}
    h1{font-size:1.2rem;color:#1e293b;margin-bottom:.4rem}
    p{color:#64748b;font-size:.875rem;margin-bottom:1.25rem}
    .err{color:#dc2626;font-size:.85rem;margin-bottom:1rem}
    input{width:100%;padding:.65rem .75rem;border:1px solid #cbd5e1;border-radius:8px;
          font-size:.9rem;margin-bottom:.9rem}
    input:focus{outline:none;border-color:#3b82f6;box-shadow:0 0 0 2px #bfdbfe}
    button{width:100%;padding:.65rem;background:#3b82f6;color:#fff;border:0;border-radius:8px;
           font-size:.9rem;font-weight:600;cursor:pointer}
    button:hover{background:#2563eb}
  </style>
</head>
<body>
  <div class="card">
    <h1>Password required</h1>
    <p>This site is protected. Enter the password to continue.</p>
    ` + errBlock + `
    <form method="POST" action="/auth-verify">
      <input type="hidden" name="redirect" value="` + redirect + `">
      <input type="password" name="password" placeholder="Password" autofocus autocomplete="current-password">
      <button type="submit">Continue</button>
    </form>
  </div>
</body>
</html>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprint(w, page)
}
