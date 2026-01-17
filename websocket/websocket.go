package websocket

import (
	"MineTracker/util"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type Hub struct {
	clients map[*websocket.Conn]bool
	writeMu map[*websocket.Conn]*sync.Mutex
	mu      sync.RWMutex
}

var GlobalHub = &Hub{
	clients: make(map[*websocket.Conn]bool),
	writeMu: make(map[*websocket.Conn]*sync.Mutex),
}

func (h *Hub) Register(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[conn] = true
	h.writeMu[conn] = &sync.Mutex{}
}

func (h *Hub) Unregister(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, conn)
	delete(h.writeMu, conn)
}

func (h *Hub) WriteJSON(conn *websocket.Conn, v interface{}) error {
	h.mu.RLock()
	m := h.writeMu[conn]
	h.mu.RUnlock()

	if m == nil {
		return fmt.Errorf("connection not registered")
	}

	m.Lock()
	defer m.Unlock()
	return conn.WriteJSON(v)
}

func (h *Hub) WriteMessage(conn *websocket.Conn, messageType int, data []byte) error {
	h.mu.RLock()
	m := h.writeMu[conn]
	h.mu.RUnlock()

	if m == nil {
		return fmt.Errorf("connection not registered")
	}

	m.Lock()
	defer m.Unlock()
	return conn.WriteMessage(messageType, data)
}

func (h *Hub) Broadcast(message interface{}) {
	h.mu.RLock()
	conns := make([]*websocket.Conn, 0, len(h.clients))
	for c := range h.clients {
		conns = append(conns, c)
	}
	h.mu.RUnlock()

	for _, conn := range conns {
		if err := h.WriteJSON(conn, message); err != nil {
			isExpectedClose := websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) ||
				errors.Is(err, websocket.ErrCloseSent) || errors.Is(err, websocket.ErrCloseSent) || errors.Is(err, net.ErrClosed)

			if isExpectedClose {
				h.Unregister(conn)
				if cerr := conn.Close(); cerr != nil {
					if !(websocket.IsCloseError(cerr, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) ||
						errors.Is(cerr, websocket.ErrCloseSent) || errors.Is(cerr, websocket.ErrCloseSent) || errors.Is(cerr, net.ErrClosed)) {
						util.Logger.Error().Err(cerr).Msg("Error closing client connection")
					}
				}
				continue
			}
			h.Unregister(conn)
			if cerr := conn.Close(); cerr != nil && !errors.Is(cerr, net.ErrClosed) {
				util.Logger.Error().Err(cerr).Msg("Error closing client connection")
			}
		}
	}
}

func HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		util.Logger.Error().Err(err).Msg("Error while upgrading to websocket")
		return
	}

	GlobalHub.Register(conn)

	defer func(conn *websocket.Conn) {
		GlobalHub.Unregister(conn)
		if err := conn.Close(); err != nil {
			if !(websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) || errors.Is(err, net.ErrClosed)) {
				util.Logger.Error().Err(err).Msg("Error while closing connection")
			}
		}
	}(conn)

	for {
		messageType, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) || errors.Is(err, net.ErrClosed) {
				// Expected close
			} else {
				util.Logger.Error().Err(err).Msg("Error while reading")
			}
			break
		}
		util.Logger.Info().Str("type", string(rune(messageType))).Msg("Got message")

		if err = GlobalHub.WriteMessage(conn, messageType, message); err != nil {
			util.Logger.Error().Err(err).Msg("Error while writing")
			break
		}
	}
}
