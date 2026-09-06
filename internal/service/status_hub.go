package service

import (
	"encoding/json"
	"sync"
)

// StatusEvent is the wire format pushed to dashboard WebSocket subscribers
// when a machine or tunnel status transitions (or a row is created/deleted).
// The frontend only uses Kind to decide which query cache to invalidate —
// ID/Status ride along for debugging and future targeted cache patching.
type StatusEvent struct {
	Kind   string `json:"kind"` // "machine" | "tunnel"
	ID     string `json:"id"`
	Status string `json:"status"`
}

// StatusHub fans status events out to dashboard WebSocket connections.
// Same pub/sub shape as LogHub, but carries pre-marshaled JSON and has no
// sentinel/op semantics: events are fire-and-forget, and a slow subscriber
// dropping one is fine — the frontend's fallback polling covers gaps.
type StatusHub struct {
	mu          sync.RWMutex
	subscribers map[chan []byte]struct{}
}

func NewStatusHub() *StatusHub {
	return &StatusHub{subscribers: make(map[chan []byte]struct{})}
}

func (h *StatusHub) Subscribe() chan []byte {
	ch := make(chan []byte, 16)
	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *StatusHub) Unsubscribe(ch chan []byte) {
	// Close under the write lock — mutually exclusive with Publish's sends
	// under the read lock, preventing send-on-closed-channel panics.
	h.mu.Lock()
	delete(h.subscribers, ch)
	close(ch)
	h.mu.Unlock()
}

// Publish broadcasts one event to every subscriber. Non-blocking sends: a
// subscriber with a full buffer misses the event rather than stalling the
// status writer (monitor/health goroutines call this synchronously).
func (h *StatusHub) Publish(kind, id, status string) {
	msg, err := json.Marshal(StatusEvent{Kind: kind, ID: id, Status: status})
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subscribers {
		select {
		case ch <- msg:
		default:
		}
	}
}
