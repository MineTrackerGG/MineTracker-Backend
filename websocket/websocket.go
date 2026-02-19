package websocket

import (
	"MineTracker/util"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		return origin == os.Getenv("FRONTEND_URL") || origin == ""
	},
}

type Hub struct {
	clients       map[*websocket.Conn]bool
	writeMu       map[*websocket.Conn]*sync.Mutex
	subscriptions map[string]map[*websocket.Conn]bool
	subNotify     map[string]chan bool // Notify channels for subscription changes
	mu            sync.RWMutex
}

var GlobalHub = &Hub{
	clients:       make(map[*websocket.Conn]bool),
	writeMu:       make(map[*websocket.Conn]*sync.Mutex),
	subscriptions: make(map[string]map[*websocket.Conn]bool),
	subNotify:     make(map[string]chan bool),
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

	// Check each subscription and notify if this was the last subscriber
	for ip, subs := range h.subscriptions {
		if subs[conn] {
			delete(subs, conn)

			// Notify if this was last subscriber
			if len(subs) == 0 {
				delete(h.subscriptions, ip)
				if h.subNotify[ip] != nil {
					select {
					case h.subNotify[ip] <- false:
					default:
					}
				}
			}
		}
	}
}

func (h *Hub) Subscribe(conn *websocket.Conn, ip string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.subscriptions[ip] == nil {
		h.subscriptions[ip] = make(map[*websocket.Conn]bool)
	}

	wasEmpty := len(h.subscriptions[ip]) == 0
	h.subscriptions[ip][conn] = true

	// Notify server goroutine if this is first subscriber
	if wasEmpty && h.subNotify[ip] != nil {
		select {
		case h.subNotify[ip] <- true:
		default:
		}
	}
}

func (h *Hub) Unsubscribe(conn *websocket.Conn, ip string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if subs, ok := h.subscriptions[ip]; ok {
		delete(subs, conn)

		// Notify server goroutine if this was last subscriber
		if len(subs) == 0 {
			delete(h.subscriptions, ip)
			if h.subNotify[ip] != nil {
				select {
				case h.subNotify[ip] <- false:
				default:
				}
			}
		}
	}
}

func (h *Hub) RegisterServerNotify(ip string) chan bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.subNotify[ip] == nil {
		h.subNotify[ip] = make(chan bool, 10)
	}
	return h.subNotify[ip]
}

func (h *Hub) UnregisterServerNotify(ip string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if ch, ok := h.subNotify[ip]; ok {
		close(ch)
		delete(h.subNotify, ip)
	}
}

func (h *Hub) IsSubscribed(ip string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subscriptions[ip]) > 0
}

func (h *Hub) GetSubscribedIPs() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	ips := make([]string, 0, len(h.subscriptions))
	for ip, conns := range h.subscriptions {
		if len(conns) > 0 {
			ips = append(ips, ip)
		}
	}
	return ips
}

func (h *Hub) writeJSONLocked(conn *websocket.Conn, v interface{}) error {
	m := h.writeMu[conn]
	if m == nil {
		return fmt.Errorf("connection not registered")
	}
	m.Lock()
	defer m.Unlock()
	return conn.WriteJSON(v)
}

func (h *Hub) SendToServer(ip string, message interface{}) {
	h.mu.RLock()
	conns := h.subscriptions[ip]
	h.mu.RUnlock()

	for conn := range conns {
		if err := h.writeJSONLocked(conn, message); err != nil {
			h.Unregister(conn)
			_ = conn.Close()
		}
	}
}

func (h *Hub) Broadcast(message interface{}) {
	h.mu.RLock()
	conns := make([]*websocket.Conn, 0, len(h.clients))
	for c := range h.clients {
		conns = append(conns, c)
	}
	h.mu.RUnlock()

	for _, conn := range conns {
		if err := h.writeJSONLocked(conn, message); err != nil {
			h.Unregister(conn)
			_ = conn.Close()
		}
	}
}

type WSMessage struct {
	Type string `json:"type"`
	IP   string `json:"ip,omitempty"`
}

func HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		util.Logger.Error().Err(err).Msg("WS upgrade failed")
		return
	}

	GlobalHub.Register(conn)
	defer func() {
		GlobalHub.Unregister(conn)
		_ = conn.Close()
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var msg WSMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "subscribe_server":
			GlobalHub.Subscribe(conn, msg.IP)

		case "unsubscribe_server":
			GlobalHub.Unsubscribe(conn, msg.IP)
		}
	}
}
