package websocket

import (
	"MineTracker/util"
	"log"
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
	mu      sync.RWMutex
}

var GlobalHub = &Hub{
	clients: make(map[*websocket.Conn]bool),
}

func (h *Hub) Register(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[conn] = true
}

func (h *Hub) Unregister(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, conn)
}

func (h *Hub) Broadcast(message interface{}) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for conn := range h.clients {
		err := conn.WriteJSON(message)
		if err != nil {
			util.Logger.Error().Err(err).Msg("Error broadcasting to client")
		}
	}
}

func HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		util.Logger.Fatal().Err(err).Msg("Error while upgrading to websocket")
		return
	}

	GlobalHub.Register(conn)

	defer func(conn *websocket.Conn) {
		GlobalHub.Unregister(conn)
		err := conn.Close()
		if err != nil {
			util.Logger.Fatal().Err(err).Msg("Error while closing connection")
		}
	}(conn)

	for {
		messageType, message, err := conn.ReadMessage()
		if err != nil {
			util.Logger.Error().Err(err).Msg("Error while reading")
			break
		}
		util.Logger.Info().Str("type", string(rune(messageType))).Msg("Got message")

		err = conn.WriteMessage(messageType, message)
		if err != nil {
			log.Printf("Error while writing: %v", err)
			util.Logger.Error().Err(err).Msg("Error while writing")
			break
		}
	}
}
