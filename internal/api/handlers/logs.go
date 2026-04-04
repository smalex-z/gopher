package handlers

import (
	"log"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/db"
	"github.com/smalex-z/gopher/internal/service"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
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

	for msg := range ch {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
			break
		}
	}
}
