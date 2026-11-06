package ws

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const MaxWebSocketClients = 50

type Hub struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]struct{}
}

// HasClients reports whether any websocket client is connected. The camera
// streamer uses this to skip the expensive JPEG decode/resize/encode cycle
// when nobody is watching - on a Pi Zero 2 W that work competes with the
// vision pipeline for the same four cores.
func (h *Hub) HasClients() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients) > 0
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[*websocket.Conn]struct{}),
	}
}

func (h *Hub) Add(conn *websocket.Conn) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.clients) >= MaxWebSocketClients {
		return ErrTooManyClients
	}
	h.clients[conn] = struct{}{}
	return nil
}

var ErrTooManyClients = &errTooManyClients{}

type errTooManyClients struct{}

func (e *errTooManyClients) Error() string {
	return "too many websocket clients"
}

func (h *Hub) Remove(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, conn)
}

func (h *Hub) Broadcast(message []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for conn := range h.clients {
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, message); err != nil {
			conn.Close()
			delete(h.clients, conn)
		}
	}
}
