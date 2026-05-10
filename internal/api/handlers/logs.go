package handlers

import (
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/db"
	"github.com/smalex-z/gopher/internal/service"
)

// WebSocket liveness parameters. The pong timeout is intentionally larger
// than the ping interval so a single missed ping doesn't tear down the
// connection on a transiently slow link, but a wedged client (frozen tab,
// network partition) is detected within ~60s instead of accumulating a
// goroutine forever.
const (
	wsWriteTimeout = 10 * time.Second
	wsPingInterval = 30 * time.Second
	wsPongTimeout  = 60 * time.Second
)

// checkWSOrigin restricts cross-origin WebSocket upgrades to the dashboard's
// own host or the configured public Domain. The default `return true` upgrader
// would accept any browser origin — which is unsafe for two reasons:
//
//  1. During setup the WS is unauthenticated; any visited page could subscribe
//     and read install logs that include freshly-generated SSH keys + agent
//     tokens.
//  2. After setup, browsers attach the session cookie to cross-origin WS
//     handshakes (CORS credentials policy doesn't apply to the WS upgrade),
//     so AuthMiddleware would happily authorise a malicious page.
//
// Callers without an Origin header (curl, wscat, server-to-server) are
// allowed — origin enforcement is purely about browser-initiated requests.
func checkWSOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	if settings, err := db.GetSettings(); err == nil && settings.Domain != "" {
		if strings.EqualFold(u.Host, settings.Domain) {
			return true
		}
	}
	return false
}

var upgrader = websocket.Upgrader{
	CheckOrigin: checkWSOrigin,
}

type LogsHandler struct {
	hub *service.LogHub
}

func NewLogsHandler(hub *service.LogHub) *LogsHandler {
	return &LogsHandler{hub: hub}
}

func (h *LogsHandler) WebSocket(w http.ResponseWriter, r *http.Request) {
	h.serveWebSocket(w, r)
}

// WebSocketDuringSetup allows websocket log streaming for setup flows where
// auth cookies may not be available yet: Step 2 (local install) and Step 3
// (firewall configuration, which runs after LocalSetupDone is set).
func (h *LogsHandler) WebSocketDuringSetup(w http.ResponseWriter, r *http.Request) {
	settings, err := db.GetSettings()
	if err != nil {
		response.InternalError(w, "failed to load settings")
		return
	}
	if settings.LocalSetupDone && settings.FirewallMode != "" {
		response.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	h.serveWebSocket(w, r)
}

func (h *LogsHandler) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}
	defer conn.Close()

	ch := h.hub.Subscribe()
	defer h.hub.Unsubscribe(ch)

	// Initial read deadline; each pong from the client extends it. If no
	// pong arrives, ReadMessage returns an error and the read goroutine exits,
	// which closes done and unblocks the writer.
	_ = conn.SetReadDeadline(time.Now().Add(wsPongTimeout))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(wsPongTimeout))
		return nil
	})

	done := make(chan struct{})
	// Reader goroutine — discards client messages but consumes pongs and
	// detects close/timeout. WebSocket frames are only processed from the
	// read side, so without this the pong handler never fires.
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	pinger := time.NewTicker(wsPingInterval)
	defer pinger.Stop()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
				return
			}
		case <-pinger.C:
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}
