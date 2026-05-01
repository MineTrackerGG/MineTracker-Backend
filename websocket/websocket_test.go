package websocket

import (
	"MineTracker/data"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type wsTestEvent struct {
	Type            string          `json:"type"`
	IP              string          `json:"ip"`
	SubscriptionID  string          `json:"subscription_id"`
	Sequence        uint64          `json:"sequence"`
	ServerTime      int64           `json:"server_time"`
	ProtocolVersion int             `json:"protocol_version"`
	EventID         string          `json:"event_id"`
	TimeRange       string          `json:"time_range"`
	Step            string          `json:"step"`
	IsCorrection    bool            `json:"is_correction"`
	Error           string          `json:"error"`
	Data            json.RawMessage `json:"data"`
}

func withTestHub(t *testing.T) func() {
	t.Helper()
	original := GlobalHub
	GlobalHub = &Hub{
		clients:             make(map[*websocket.Conn]*connectionState),
		subscriptionsByIP:   make(map[string]map[string]*subscriptionState),
		subscriptionsByConn: make(map[*websocket.Conn]map[string]*subscriptionState),
		defaultTimeRange:    make(map[*websocket.Conn]string),
		subNotify:           make(map[string]chan bool),
	}
	return func() { GlobalHub = original }
}

func withQueryStub(t *testing.T, fn func(ip, timeRange string) ([]data.ServerDataPoint, string, error)) func() {
	t.Helper()
	original := queryHistoricalSeries
	queryHistoricalSeries = fn
	return func() { queryHistoricalSeries = original }
}

func dialWS(t *testing.T, server *httptest.Server) *websocket.Conn {
	t.Helper()
	url := "ws" + server.URL[len("http"):]
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	return conn
}

func readEvent(t *testing.T, conn *websocket.Conn) wsTestEvent {
	t.Helper()
	var event wsTestEvent
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatalf("read event: %v", err)
	}
	return event
}

func TestSubscribeSendsInitialDataBeforeLiveEvents(t *testing.T) {
	restoreHub := withTestHub(t)
	defer restoreHub()
	restoreQuery := withQueryStub(t, func(ip, timeRange string) ([]data.ServerDataPoint, string, error) {
		time.Sleep(100 * time.Millisecond)
		return []data.ServerDataPoint{
			{Timestamp: 10, PlayerCount: 3, Ip: ip, Name: "srv"},
			{Timestamp: 20, PlayerCount: 5, Ip: ip, Name: "srv"},
		}, "30m", nil
	})
	defer restoreQuery()

	server := httptest.NewServer(http.HandlerFunc(HandleWebSocket))
	defer server.Close()

	conn := dialWS(t, server)
	defer conn.Close()

	if err := conn.WriteJSON(WSMessage{Type: "subscribe_server", IP: "1.2.3.4", TimeRange: "7d"}); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}

	ack := readEvent(t, conn)
	if ack.Type != "subscription_ack" {
		t.Fatalf("expected subscription_ack first, got %q", ack.Type)
	}

	GlobalHub.SendRawDataPoint("1.2.3.4", data.ServerDataPoint{Timestamp: 21, PlayerCount: 7, Ip: "1.2.3.4", Name: "srv"})

	initial := readEvent(t, conn)
	if initial.Type != "initial_data" {
		t.Fatalf("expected initial_data second, got %q", initial.Type)
	}
	if initial.Sequence <= ack.Sequence {
		t.Fatalf("initial sequence %d must be greater than ack sequence %d", initial.Sequence, ack.Sequence)
	}
	if initial.SubscriptionID == "" || initial.SubscriptionID != ack.SubscriptionID {
		t.Fatal("subscription_id must be present and stable")
	}
	if initial.TimeRange != "7d" || initial.Step != "30m" {
		t.Fatalf("unexpected time range/step: %q %q", initial.TimeRange, initial.Step)
	}

	var points []data.ServerDataPoint
	if err := json.Unmarshal(initial.Data, &points); err != nil {
		t.Fatalf("unmarshal initial data: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("expected 2 initial points, got %d", len(points))
	}
	if points[0].Timestamp > points[1].Timestamp {
		t.Fatal("initial_data.data must be sorted by timestamp")
	}

	live := readEvent(t, conn)
	if live.Type != "data_point_rt" {
		t.Fatalf("expected live event third, got %q", live.Type)
	}
	if live.Sequence <= initial.Sequence {
		t.Fatalf("live sequence %d must be greater than initial sequence %d", live.Sequence, initial.Sequence)
	}
	if live.SubscriptionID != ack.SubscriptionID {
		t.Fatal("live event must stay on the same subscription_id")
	}
	if live.IsCorrection {
		t.Fatal("expected live event to be a normal append, not a correction")
	}

	var livePoint data.ServerDataPoint
	if err := json.Unmarshal(live.Data, &livePoint); err != nil {
		t.Fatalf("unmarshal live point: %v", err)
	}
	if livePoint.Timestamp != 21 {
		t.Fatalf("unexpected live timestamp %d", livePoint.Timestamp)
	}
}

func TestResubscribeReplacesOldSubscription(t *testing.T) {
	restoreHub := withTestHub(t)
	defer restoreHub()
	restoreQuery := withQueryStub(t, func(ip, timeRange string) ([]data.ServerDataPoint, string, error) {
		return []data.ServerDataPoint{{Timestamp: 1, PlayerCount: 1, Ip: ip, Name: timeRange}}, "4m", nil
	})
	defer restoreQuery()

	server := httptest.NewServer(http.HandlerFunc(HandleWebSocket))
	defer server.Close()

	conn := dialWS(t, server)
	defer conn.Close()

	if err := conn.WriteJSON(WSMessage{Type: "subscribe_server", IP: "1.2.3.4", TimeRange: "7d"}); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}
	_ = readEvent(t, conn)
	firstInitial := readEvent(t, conn)

	if err := conn.WriteJSON(WSMessage{Type: "subscribe_server", IP: "1.2.3.4", TimeRange: "24h"}); err != nil {
		t.Fatalf("write resubscribe: %v", err)
	}
	secondAck := readEvent(t, conn)
	secondInitial := readEvent(t, conn)

	if firstInitial.SubscriptionID == secondAck.SubscriptionID {
		t.Fatal("expected a new subscription_id after re-subscribe")
	}
	if secondInitial.TimeRange != "24h" {
		t.Fatalf("expected second initial_data for new range, got %q", secondInitial.TimeRange)
	}

	GlobalHub.SendRawDataPoint("1.2.3.4", data.ServerDataPoint{Timestamp: 2, PlayerCount: 3, Ip: "1.2.3.4", Name: "24h"})
	live := readEvent(t, conn)
	if live.SubscriptionID != secondAck.SubscriptionID {
		t.Fatal("live event must use the replacement subscription_id")
	}
}

func TestUnsubscribeStopsFurtherDelivery(t *testing.T) {
	restoreHub := withTestHub(t)
	defer restoreHub()
	restoreQuery := withQueryStub(t, func(ip, timeRange string) ([]data.ServerDataPoint, string, error) {
		return []data.ServerDataPoint{{Timestamp: 1, PlayerCount: 1, Ip: ip, Name: "srv"}}, "4m", nil
	})
	defer restoreQuery()

	server := httptest.NewServer(http.HandlerFunc(HandleWebSocket))
	defer server.Close()

	conn := dialWS(t, server)
	defer conn.Close()

	if err := conn.WriteJSON(WSMessage{Type: "subscribe_server", IP: "1.2.3.4", TimeRange: "7d"}); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}
	_ = readEvent(t, conn)
	_ = readEvent(t, conn)

	if err := conn.WriteJSON(WSMessage{Type: "unsubscribe_server", IP: "1.2.3.4"}); err != nil {
		t.Fatalf("write unsubscribe: %v", err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for GlobalHub.IsSubscribed("1.2.3.4") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	GlobalHub.SendRawDataPoint("1.2.3.4", data.ServerDataPoint{Timestamp: 2, PlayerCount: 9, Ip: "1.2.3.4", Name: "srv"})
	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	var event wsTestEvent
	if err := conn.ReadJSON(&event); err == nil {
		t.Fatalf("expected no further events after unsubscribe, got %+v", event)
	}
}
