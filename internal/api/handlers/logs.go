package handlers

import (
"log"
"net/http"

"github.com/gorilla/websocket"
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
