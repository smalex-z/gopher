// Package proxy implements the L7 bot-protection middleware. Instead of running
// a separate listener, it wraps the main Gopher http.Handler. Requests whose
// Host header resolves to a bot-protected tunnel subdomain are intercepted;
// all other requests fall through to the normal Gopher handler unchanged.
package proxy

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/db"
)

const (
	cookieName        = "gopher_bot_pass"
	defaultSessionTTL = 24 * time.Hour
	// powDifficulty is the number of leading zero hex chars required in the
	// SHA-256 hash. 5 zeros = 20 bits of work ≈ 1–3 s on a modern browser.
	powDifficulty = 5
)

// Middleware holds the ephemeral HMAC signing key. Create once at startup with
// NewMiddleware, then call Wrap to get the http.Handler.
type Middleware struct {
	hmacKey []byte
}

// NewMiddleware generates a fresh HMAC key and returns the middleware.
// Restarting the process invalidates outstanding cookies (by design).
func NewMiddleware() (*Middleware, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("proxy: generate HMAC key: %w", err)
	}
	return &Middleware{hmacKey: key}, nil
}

// Wrap returns an http.Handler that intercepts bot-protected tunnel requests
// and passes everything else to next.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tunnel := resolveTunnel(r.Host)
		if tunnel == nil || !tunnel.BotProtectionEnabled {
			next.ServeHTTP(w, r)
			return
		}

		// PoW solution submission — same domain, path /bot-verify.
		if r.URL.Path == "/bot-verify" {
			m.handleVerify(w, r, tunnel)
			return
		}

		m.handleProxy(w, r, tunnel)
	})
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func (m *Middleware) handleProxy(w http.ResponseWriter, r *http.Request, tunnel *db.Tunnel) {
	ip := clientIP(r)

	// IP allowlist: bypass everything.
	if isIPAllowed(tunnel.BotProtectionAllowIP, ip) {
		m.forward(w, r, tunnel)
		return
	}

	// Valid cookie: forward unconditionally — this covers both normal page
	// requests and browser-side fetch/XHR calls (which send Accept:
	// application/json). Once the browser has passed the challenge, all of
	// its requests work regardless of Accept header.
	if m.hasCookie(r, tunnel) {
		m.forward(w, r, tunnel)
		return
	}

	// No valid cookie from here on. Decide how to respond based on client type.

	// API/non-browser clients can't complete an HTML challenge; return JSON.
	if isAPIClient(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"success":false,"error":"bot protection active — complete browser verification first"}`)
		return
	}

	// WebSocket upgrades can't display a challenge page either.
	if isWebSocketUpgrade(r) {
		http.Error(w, "403 Forbidden — complete browser verification first", http.StatusForbidden)
		return
	}

	serveChallenge(w, tunnel.ID, powDifficulty)
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

	nonce := r.FormValue("nonce")
	solution := r.FormValue("solution")
	redirect := r.FormValue("redirect")

	if !checkPoW(nonce, solution, powDifficulty) {
		http.Error(w, "invalid proof of work", http.StatusForbidden)
		return
	}

	ttl := ttlForTunnel(tunnel)
	token := m.issueToken(tunnel.ID, ttl)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	})

	go func() {
		_ = db.CreateBotSession(&db.BotSession{
			ID:        randomHex(8),
			TunnelID:  tunnel.ID,
			IP:        clientIP(r),
			UserAgent: r.UserAgent(),
			IssuedAt:  time.Now(),
			ExpiresAt: time.Now().Add(ttl),
		})
	}()

	if redirect == "" || redirect == "/bot-verify" {
		redirect = "/"
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// Forwarding
// ---------------------------------------------------------------------------

func (m *Middleware) forward(w http.ResponseWriter, r *http.Request, tunnel *db.Tunnel) {
	// Bot-protected tunnel ports bind to bind_ip when set (same as other public
	// tunnel ports), so we must proxy to bind_ip:port, not localhost:port.
	ratholeHost := "localhost"
	if settings, err := db.GetSettings(); err == nil && settings.BindIP != "" {
		ratholeHost = settings.BindIP
	}
	target, _ := url.Parse(fmt.Sprintf("http://%s:%d", ratholeHost, tunnel.RatholePort))
	rp := httputil.NewSingleHostReverseProxy(target)
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

func (m *Middleware) issueToken(tunnelID string, ttl time.Duration) string {
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	exp := time.Now().Add(ttl).Unix()
	payload := fmt.Sprintf("%s:%d", tunnelID, exp)
	return payload + ":" + hmacSign(m.hmacKey, payload)
}

func (m *Middleware) validateToken(token, tunnelID string) bool {
	lastColon := strings.LastIndex(token, ":")
	if lastColon < 0 {
		return false
	}
	mac := token[lastColon+1:]
	payload := token[:lastColon]
	if !hmac.Equal([]byte(mac), []byte(hmacSign(m.hmacKey, payload))) {
		return false
	}
	secondColon := strings.LastIndex(payload, ":")
	if secondColon < 0 {
		return false
	}
	tid, expStr := payload[:secondColon], payload[secondColon+1:]
	if tid != tunnelID {
		return false
	}
	var exp int64
	if _, err := fmt.Sscan(expStr, &exp); err != nil {
		return false
	}
	return time.Now().Unix() < exp
}

func (m *Middleware) hasCookie(r *http.Request, tunnel *db.Tunnel) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return m.validateToken(c.Value, tunnel.ID)
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
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
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

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
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
