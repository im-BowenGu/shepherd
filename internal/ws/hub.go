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
