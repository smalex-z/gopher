package handlers

import (
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/smalex-z/gopher/internal/service"
)

// StatusWSHandler streams machine/tunnel status-change events to the
// dashboard so badges update on push instead of waiting for the next poll.
// Routed inside the auth middleware group — unlike the install-log WS there
// is no during-setup variant, so no unauthenticated path exists.
type StatusWSHandler struct {
	hub *service.StatusHub
}

func NewStatusWSHandler(hub *service.StatusHub) *StatusWSHandler {
	return &StatusWSHandler{hub: hub}
}

func (h *StatusWSHandler) WebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("status WebSocket upgrade error: %v", err)
		return
	}
	defer conn.Close()

	ch := h.hub.Subscribe()
	defer h.hub.Unsubscribe(ch)

	// Same liveness protocol as the logs WS: ping every wsPingInterval, drop
	// the connection when a pong hasn't arrived within wsPongTimeout.
	_ = conn.SetReadDeadline(time.Now().Add(wsPongTimeout))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(wsPongTimeout))
		return nil
	})

	done := make(chan struct{})
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
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
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
