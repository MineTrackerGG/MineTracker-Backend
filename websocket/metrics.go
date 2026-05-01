package websocket

import (
	"expvar"
	"time"
)

var (
	metricInitialDataLatencyMs = expvar.NewInt("ws_initial_data_latency_ms")
	metricLiveFanoutMs         = expvar.NewInt("ws_live_fanout_ms")
	metricSubscriptionCount    = expvar.NewInt("ws_subscription_count")
	metricDroppedEvents        = expvar.NewInt("ws_dropped_events_total")
	metricOutOfOrderEvents     = expvar.NewInt("ws_out_of_order_events_total")
	metricEventsSentTotal      = expvar.NewMap("ws_events_sent_total")
)

func recordInitialDataLatency(duration time.Duration) {
	metricInitialDataLatencyMs.Set(duration.Milliseconds())
}

func recordLiveFanout(duration time.Duration) {
	metricLiveFanoutMs.Set(duration.Milliseconds())
}

func recordSubscriptionAdded() {
	metricSubscriptionCount.Add(1)
}

func recordSubscriptionRemoved() {
	metricSubscriptionCount.Add(-1)
}

func recordDroppedEvent() {
	metricDroppedEvents.Add(1)
}

func recordOutOfOrderEvent() {
	metricOutOfOrderEvents.Add(1)
}

func recordEventSent(eventType string) {
	metricEventsSentTotal.Add(eventType, 1)
}
