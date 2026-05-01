package websocket

import (
	"MineTracker/data"
	"MineTracker/util"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	protocolVersion     = 1
	connectionQueueSize = 256
	writeWait           = 10 * time.Second
	pongWait            = 60 * time.Second
	pingPeriod          = 25 * time.Second
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		return origin == os.Getenv("FRONTEND_URL") || origin == ""
	},
}

var queryHistoricalSeries = data.QueryHistoricalDataPoints

var errConnectionClosed = errors.New("connection closed")

type Hub struct {
	mu                  sync.RWMutex
	clients             map[*websocket.Conn]*connectionState
	subscriptionsByIP   map[string]map[string]*subscriptionState
	subscriptionsByConn map[*websocket.Conn]map[string]*subscriptionState
	defaultTimeRange    map[*websocket.Conn]string
	subNotify           map[string]chan bool
}

var GlobalHub = &Hub{
	clients:             make(map[*websocket.Conn]*connectionState),
	subscriptionsByIP:   make(map[string]map[string]*subscriptionState),
	subscriptionsByConn: make(map[*websocket.Conn]map[string]*subscriptionState),
	defaultTimeRange:    make(map[*websocket.Conn]string),
	subNotify:           make(map[string]chan bool),
}

type connectionState struct {
	conn   *websocket.Conn
	mu     sync.Mutex
	cond   *sync.Cond
	queue  []queuedMessage
	closed bool
}

type queuedMessage struct {
	payload  interface{}
	critical bool
}

func newConnectionState(conn *websocket.Conn) *connectionState {
	state := &connectionState{conn: conn}
	state.cond = sync.NewCond(&state.mu)
	return state
}

type subscriptionState struct {
	hub              *Hub
	connState        *connectionState
	conn             *websocket.Conn
	ip               string
	protocolVersion  int
	subscriptionID   string
	timeRange        string
	step             string
	sequence         uint64
	serverTime       int64
	lastTimestamp    int64
	initialSent      bool
	closed           bool
	bufferedLiveData []data.ServerDataPoint
	mu               sync.Mutex
}

type WSMessage struct {
	Type            string `json:"type"`
	IP              string `json:"ip,omitempty"`
	TimeRange       string `json:"time_range,omitempty"`
	ProtocolVersion *int   `json:"protocol_version,omitempty"`
}

type protocolEnvelope struct {
	Type            string `json:"type"`
	IP              string `json:"ip,omitempty"`
	SubscriptionID  string `json:"subscription_id,omitempty"`
	Sequence        uint64 `json:"sequence"`
	ServerTime      int64  `json:"server_time"`
	ProtocolVersion int    `json:"protocol_version"`
	EventID         string `json:"event_id,omitempty"`
}

type subscriptionAckMessage struct {
	protocolEnvelope
	TimeRange string `json:"time_range,omitempty"`
	Step      string `json:"step,omitempty"`
}

type subscriptionErrorMessage struct {
	protocolEnvelope
	Code      string `json:"code,omitempty"`
	Error     string `json:"error"`
	TimeRange string `json:"time_range,omitempty"`
}

type initialDataMessage struct {
	protocolEnvelope
	TimeRange string                 `json:"time_range,omitempty"`
	Step      string                 `json:"step"`
	Data      []data.ServerDataPoint `json:"data"`
	IsFinal   bool                   `json:"is_final,omitempty"`
}

type realtimeDataMessage struct {
	protocolEnvelope
	Data         data.ServerDataPoint `json:"data"`
	Step         string               `json:"step,omitempty"`
	IsCorrection bool                 `json:"is_correction,omitempty"`
}

func (h *Hub) Register(conn *websocket.Conn) {
	state := newConnectionState(conn)
	state.start()

	h.mu.Lock()
	h.clients[conn] = state
	h.mu.Unlock()
}

func (h *Hub) Unregister(conn *websocket.Conn) {
	h.mu.Lock()
	state := h.clients[conn]
	delete(h.clients, conn)
	delete(h.defaultTimeRange, conn)

	connSubs := h.subscriptionsByConn[conn]
	delete(h.subscriptionsByConn, conn)
	for ip, subs := range h.subscriptionsByIP {
		for id, sub := range subs {
			if sub.conn == conn {
				delete(subs, id)
				sub.closeLocked()
				recordSubscriptionRemoved()
				if len(subs) == 0 {
					delete(h.subscriptionsByIP, ip)
					h.signalSubscribersChanged(ip, false)
				}
			}
		}
	}
	h.mu.Unlock()

	if connSubs != nil {
		for _, sub := range connSubs {
			sub.close()
		}
	}
	if state != nil {
		state.close()
	}
}

func (h *Hub) signalSubscribersChanged(ip string, hasSubscribers bool) {
	if h.subNotify[ip] == nil {
		return
	}
	select {
	case h.subNotify[ip] <- hasSubscribers:
	default:
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
	return len(h.subscriptionsByIP[ip]) > 0
}

func (h *Hub) GetSubscribedIPs() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	ips := make([]string, 0, len(h.subscriptionsByIP))
	for ip, subs := range h.subscriptionsByIP {
		if len(subs) > 0 {
			ips = append(ips, ip)
		}
	}
	return ips
}

func (h *Hub) SetTimeRange(conn *websocket.Conn, timeRange string) {
	h.mu.Lock()
	h.defaultTimeRange[conn] = timeRange
	h.mu.Unlock()
}

func (h *Hub) GetTimeRange(conn *websocket.Conn) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.defaultTimeRange[conn]
}

func (h *Hub) Subscribe(conn *websocket.Conn, ip string) error {
	return h.subscribe(conn, ip, h.GetTimeRange(conn))
}

func (h *Hub) SubscribeWithTimeRange(conn *websocket.Conn, ip string, timeRange string) error {
	return h.subscribe(conn, ip, timeRange)
}

func (h *Hub) subscribe(conn *websocket.Conn, ip string, timeRange string) error {
	subscriptionID := uuid.NewString()
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return h.sendSubscriptionError(conn, "subscription_error", "invalid_ip", "ip is required", subscriptionID, "", "")
	}

	if timeRange != "" {
		if _, _, err := data.ValidateTimeRange(timeRange); err != nil {
			return h.sendSubscriptionError(conn, "subscription_error", "invalid_time_range", err.Error(), subscriptionID, ip, timeRange)
		}
	}

	h.mu.Lock()
	if h.clients[conn] == nil {
		h.mu.Unlock()
		return fmt.Errorf("connection not registered")
	}
	if h.subscriptionsByConn[conn] == nil {
		h.subscriptionsByConn[conn] = make(map[string]*subscriptionState)
	}

	if existing := h.subscriptionsByConn[conn][ip]; existing != nil {
		existing.closeLocked()
		delete(h.subscriptionsByConn[conn], ip)
		if subs := h.subscriptionsByIP[ip]; subs != nil {
			delete(subs, existing.subscriptionID)
			if len(subs) == 0 {
				delete(h.subscriptionsByIP, ip)
				h.signalSubscribersChanged(ip, false)
			}
		}
	}

	connState := h.clients[conn]
	sub := &subscriptionState{
		hub:             h,
		connState:       connState,
		conn:            conn,
		ip:              ip,
		protocolVersion: protocolVersion,
		subscriptionID:  subscriptionID,
		timeRange:       timeRange,
	}

	h.subscriptionsByConn[conn][ip] = sub
	if h.subscriptionsByIP[ip] == nil {
		h.subscriptionsByIP[ip] = make(map[string]*subscriptionState)
	}
	h.subscriptionsByIP[ip][subscriptionID] = sub
	recordSubscriptionAdded()
	h.signalSubscribersChanged(ip, true)
	h.mu.Unlock()

	ack := sub.newAckMessage()
	if err := connState.enqueue(queuedMessage{payload: ack, critical: true}); err != nil {
		h.removeSubscription(sub)
		return err
	}
	recordEventSent("subscription_ack")
	logEvent(ack.IP, ack.SubscriptionID, ack.Sequence, timeRange, ack.Step)

	if timeRange == "" {
		sub.markInitialCompleteWithoutHistory()
		return nil
	}

	go h.loadInitialData(sub)
	return nil
}

func (h *Hub) loadInitialData(sub *subscriptionState) {
	started := time.Now()
	points, step, err := queryHistoricalSeries(sub.ip, sub.timeRange)
	if err != nil {
		_ = h.sendSubscriptionError(sub.conn, "subscription_error", "initial_data_failed", err.Error(), sub.subscriptionID, sub.ip, sub.timeRange)
		h.removeSubscription(sub)
		return
	}

	sort.Slice(points, func(i, j int) bool {
		if points[i].Timestamp != points[j].Timestamp {
			return points[i].Timestamp < points[j].Timestamp
		}
		if points[i].PlayerCount != points[j].PlayerCount {
			return points[i].PlayerCount < points[j].PlayerCount
		}
		if points[i].Ip != points[j].Ip {
			return points[i].Ip < points[j].Ip
		}
		return points[i].Name < points[j].Name
	})

	sub.mu.Lock()
	if sub.closed {
		sub.mu.Unlock()
		return
	}
	sequence := sub.nextSequenceLocked()
	serverTime := sub.nextServerTimeLocked()
	if len(points) > 0 {
		sub.lastTimestamp = points[len(points)-1].Timestamp
	}
	buffered := append([]data.ServerDataPoint(nil), sub.bufferedLiveData...)
	sub.bufferedLiveData = nil
	msg := initialDataMessage{
		protocolEnvelope: protocolEnvelope{
			Type:            "initial_data",
			IP:              sub.ip,
			SubscriptionID:  sub.subscriptionID,
			Sequence:        sequence,
			ServerTime:      serverTime,
			ProtocolVersion: sub.protocolVersion,
			EventID:         eventID(sub.ip, serverTime, sequence),
		},
		TimeRange: sub.timeRange,
		Step:      step,
		Data:      points,
		IsFinal:   true,
	}
	sub.step = step
	sub.mu.Unlock()

	if err := sub.connState.enqueue(queuedMessage{payload: msg, critical: true}); err != nil {
		h.removeSubscription(sub)
		return
	}

	sub.mu.Lock()
	if !sub.closed {
		sub.initialSent = true
	}
	sub.mu.Unlock()

	recordEventSent("initial_data")
	recordInitialDataLatency(time.Since(started))
	logEvent(sub.ip, sub.subscriptionID, msg.Sequence, sub.timeRange, step)

	for _, point := range buffered {
		h.publishLivePoint(sub, point)
	}
}

func (h *Hub) removeSubscription(sub *subscriptionState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if sub.closed {
		return
	}
	sub.closeLocked()

	if connSubs := h.subscriptionsByConn[sub.conn]; connSubs != nil {
		delete(connSubs, sub.ip)
		if len(connSubs) == 0 {
			delete(h.subscriptionsByConn, sub.conn)
		}
	}
	if subs := h.subscriptionsByIP[sub.ip]; subs != nil {
		delete(subs, sub.subscriptionID)
		if len(subs) == 0 {
			delete(h.subscriptionsByIP, sub.ip)
			h.signalSubscribersChanged(sub.ip, false)
		}
	}
	recordSubscriptionRemoved()
}

func (h *Hub) Unsubscribe(conn *websocket.Conn, ip string) {
	h.mu.RLock()
	sub := h.subscriptionsByConn[conn][ip]
	h.mu.RUnlock()
	if sub != nil {
		h.removeSubscription(sub)
	}
}

func (h *Hub) connectionState(conn *websocket.Conn) *connectionState {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.clients[conn]
}

func (h *Hub) sendSubscriptionError(conn *websocket.Conn, eventType, code, message, subscriptionID, ip, timeRange string) error {
	state := h.connectionState(conn)
	if state == nil {
		return fmt.Errorf("connection not registered")
	}
	msg := subscriptionErrorMessage{
		protocolEnvelope: protocolEnvelope{
			Type:            eventType,
			IP:              ip,
			SubscriptionID:  subscriptionID,
			Sequence:        0,
			ServerTime:      time.Now().UnixMilli(),
			ProtocolVersion: protocolVersion,
		},
		Code:      code,
		Error:     message,
		TimeRange: timeRange,
	}
	return state.enqueue(queuedMessage{payload: msg, critical: true})
}

func (h *Hub) publishLivePoint(sub *subscriptionState, point data.ServerDataPoint) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.closed {
		return
	}
	if !sub.initialSent {
		sub.bufferedLiveData = append(sub.bufferedLiveData, point)
		return
	}
	if point.Timestamp < sub.lastTimestamp {
		recordOutOfOrderEvent()
		util.Logger.Warn().Str("ip", sub.ip).Str("subscription_id", sub.subscriptionID).Int64("timestamp", point.Timestamp).
			Msg("Dropped out-of-order websocket event")
		return
	}

	sequence := sub.nextSequenceLocked()
	serverTime := sub.nextServerTimeLocked()
	isCorrection := point.Timestamp == sub.lastTimestamp
	if point.Timestamp > sub.lastTimestamp {
		sub.lastTimestamp = point.Timestamp
	}

	msg := realtimeDataMessage{
		protocolEnvelope: protocolEnvelope{
			Type:            "data_point_rt",
			IP:              sub.ip,
			SubscriptionID:  sub.subscriptionID,
			Sequence:        sequence,
			ServerTime:      serverTime,
			ProtocolVersion: sub.protocolVersion,
			EventID:         eventID(sub.ip, serverTime, sequence),
		},
		Data:         point,
		Step:         sub.step,
		IsCorrection: isCorrection,
	}

	if err := sub.connState.enqueue(queuedMessage{payload: msg, critical: false}); err != nil {
		sub.closed = true
		go sub.connState.close()
		return
	}
	recordEventSent("data_point_rt")
	logEvent(sub.ip, sub.subscriptionID, sequence, sub.timeRange, sub.step)
}

func (sub *subscriptionState) newAckMessage() subscriptionAckMessage {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	sequence := sub.nextSequenceLocked()
	serverTime := sub.nextServerTimeLocked()
	return subscriptionAckMessage{
		protocolEnvelope: protocolEnvelope{
			Type:            "subscription_ack",
			IP:              sub.ip,
			SubscriptionID:  sub.subscriptionID,
			Sequence:        sequence,
			ServerTime:      serverTime,
			ProtocolVersion: sub.protocolVersion,
			EventID:         eventID(sub.ip, serverTime, sequence),
		},
		TimeRange: sub.timeRange,
	}
}

func (sub *subscriptionState) markInitialCompleteWithoutHistory() {
	sub.mu.Lock()
	sub.initialSent = true
	sub.mu.Unlock()
}

func (sub *subscriptionState) nextSequenceLocked() uint64 {
	sub.sequence++
	return sub.sequence
}

func (sub *subscriptionState) nextServerTimeLocked() int64 {
	serverTime := time.Now().UnixMilli()
	if serverTime <= sub.serverTime {
		serverTime = sub.serverTime + 1
	}
	sub.serverTime = serverTime
	return serverTime
}

func (sub *subscriptionState) close() {
	sub.mu.Lock()
	sub.closeLocked()
	sub.mu.Unlock()
}

func (sub *subscriptionState) closeLocked() {
	sub.closed = true
}

func eventID(ip string, serverTime int64, sequence uint64) string {
	return fmt.Sprintf("%s:%d:%d", ip, serverTime, sequence)
}

func logEvent(ip, subscriptionID string, sequence uint64, timeRange, step string) {
	util.Logger.Info().
		Str("ip", ip).
		Str("subscription_id", subscriptionID).
		Uint64("sequence", sequence).
		Str("time_range", timeRange).
		Str("step", step).
		Msg("ws event sent")
}

func (cs *connectionState) start() {
	cs.cond = sync.NewCond(&cs.mu)
	go cs.writeLoop()
	go cs.pingLoop()
}

func (cs *connectionState) close() {
	cs.mu.Lock()
	cs.closed = true
	cs.cond.Broadcast()
	cs.mu.Unlock()
}

func (cs *connectionState) enqueue(msg queuedMessage) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.closed {
		return errConnectionClosed
	}

	if len(cs.queue) >= connectionQueueSize {
		if !msg.critical {
			if !cs.dropOldestNonCriticalLocked() {
				recordDroppedEvent()
				return nil
			}
		} else if !cs.dropOldestNonCriticalLocked() && len(cs.queue) > 0 {
			cs.queue = cs.queue[1:]
			recordDroppedEvent()
		}
	}

	cs.queue = append(cs.queue, msg)
	cs.cond.Signal()
	return nil
}

func (cs *connectionState) dropOldestNonCriticalLocked() bool {
	for i, msg := range cs.queue {
		if !msg.critical {
			copy(cs.queue[i:], cs.queue[i+1:])
			cs.queue = cs.queue[:len(cs.queue)-1]
			recordDroppedEvent()
			return true
		}
	}
	return false
}

func (cs *connectionState) writeLoop() {
	for {
		cs.mu.Lock()
		for len(cs.queue) == 0 && !cs.closed {
			cs.cond.Wait()
		}
		if len(cs.queue) == 0 && cs.closed {
			cs.mu.Unlock()
			return
		}
		msg := cs.queue[0]
		cs.queue = cs.queue[1:]
		cs.mu.Unlock()

		if err := cs.conn.WriteJSON(msg.payload); err != nil {
			return
		}
	}
}

func (cs *connectionState) pingLoop() {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	for {
		cs.mu.Lock()
		closed := cs.closed
		cs.mu.Unlock()
		if closed {
			return
		}

		<-ticker.C
		deadline := time.Now().Add(writeWait)
		if err := cs.conn.WriteControl(websocket.PingMessage, []byte("ping"), deadline); err != nil {
			return
		}
	}
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

	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var msg WSMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}

		if msg.ProtocolVersion != nil && *msg.ProtocolVersion != protocolVersion {
			_ = GlobalHub.sendSubscriptionError(conn, "subscription_error", "unsupported_protocol_version", fmt.Sprintf("protocol version %d is not supported", *msg.ProtocolVersion), uuid.NewString(), msg.IP, msg.TimeRange)
			continue
		}

		switch msg.Type {
		case "subscribe_server":
			if msg.TimeRange != "" {
				if err := GlobalHub.SubscribeWithTimeRange(conn, msg.IP, msg.TimeRange); err != nil {
					util.Logger.Warn().Err(err).Str("ip", msg.IP).Str("timeRange", msg.TimeRange).Msg("subscribe_server failed")
				}
			} else {
				if err := GlobalHub.Subscribe(conn, msg.IP); err != nil {
					util.Logger.Warn().Err(err).Str("ip", msg.IP).Msg("legacy subscribe_server failed")
				}
			}

		case "unsubscribe_server":
			GlobalHub.Unsubscribe(conn, msg.IP)

		case "set_time_range":
			if msg.TimeRange != "" {
				GlobalHub.SetTimeRange(conn, msg.TimeRange)
			}
		}
	}
}

// SendRawDataPoint sends raw real-time data point to all subscribers of a server.
// The frontend can render or replace the current point without client-side smoothing.
func (h *Hub) SendRawDataPoint(ip string, dataPoint data.ServerDataPoint) {
	h.mu.RLock()
	subs := h.subscriptionsByIP[ip]
	targets := make([]*subscriptionState, 0, len(subs))
	for _, sub := range subs {
		targets = append(targets, sub)
	}
	h.mu.RUnlock()

	if len(targets) == 0 {
		return
	}

	start := time.Now()
	for _, sub := range targets {
		h.publishLivePoint(sub, dataPoint)
	}
	recordLiveFanout(time.Since(start))
}

// SendAggregatedDataPoint is kept for backward compatibility and now forwards raw live data.
func (h *Hub) SendAggregatedDataPoint(ip string, dataPoint data.ServerDataPoint) {
	h.SendRawDataPoint(ip, dataPoint)
}

func (h *Hub) SendToServer(ip string, message interface{}) {
	h.mu.RLock()
	subs := h.subscriptionsByIP[ip]
	targets := make([]*subscriptionState, 0, len(subs))
	for _, sub := range subs {
		targets = append(targets, sub)
	}
	h.mu.RUnlock()

	for _, sub := range targets {
		_ = sub.connState.enqueue(queuedMessage{payload: message, critical: false})
	}
}

func (h *Hub) Broadcast(message interface{}) {
	h.mu.RLock()
	targets := make([]*connectionState, 0, len(h.clients))
	for _, state := range h.clients {
		targets = append(targets, state)
	}
	h.mu.RUnlock()

	for _, state := range targets {
		_ = state.enqueue(queuedMessage{payload: message, critical: false})
	}
}
