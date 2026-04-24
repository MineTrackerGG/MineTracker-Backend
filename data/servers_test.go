package data

import "testing"

func TestMergeServerDataPointsKeepsExtremesAndOrder(t *testing.T) {
	base := []ServerDataPoint{
		{Timestamp: 20, PlayerCount: 10, Ip: "1.2.3.4", Name: "srv"},
		{Timestamp: 40, PlayerCount: 15, Ip: "1.2.3.4", Name: "srv"},
		{Timestamp: 60, PlayerCount: 12, Ip: "1.2.3.4", Name: "srv"},
	}

	extremes := []ServerDataPoint{
		{Timestamp: 10, PlayerCount: 5, Ip: "1.2.3.4", Name: "srv"},
		{Timestamp: 40, PlayerCount: 15, Ip: "1.2.3.4", Name: "srv"},
		{Timestamp: 50, PlayerCount: 20, Ip: "1.2.3.4", Name: "srv"},
	}

	merged := mergeServerDataPoints(base, extremes)

	if len(merged) != 5 {
		t.Fatalf("expected 5 merged points, got %d", len(merged))
	}

	expected := []ServerDataPoint{
		{Timestamp: 10, PlayerCount: 5, Ip: "1.2.3.4", Name: "srv"},
		{Timestamp: 20, PlayerCount: 10, Ip: "1.2.3.4", Name: "srv"},
		{Timestamp: 40, PlayerCount: 15, Ip: "1.2.3.4", Name: "srv"},
		{Timestamp: 50, PlayerCount: 20, Ip: "1.2.3.4", Name: "srv"},
		{Timestamp: 60, PlayerCount: 12, Ip: "1.2.3.4", Name: "srv"},
	}

	for i, want := range expected {
		got := merged[i]
		if got != want {
			t.Fatalf("point %d mismatch: got %+v, want %+v", i, got, want)
		}
	}
}

func TestQueryPointBudgetScalesWithRange(t *testing.T) {
	tests := []struct {
		name    string
		start   string
		wantMax int
		wantMin int
	}{
		{name: "year", start: "-1y", wantMax: 120, wantMin: 10},
		{name: "month", start: "-1M", wantMax: 240, wantMin: 10},
		{name: "week", start: "-7d", wantMax: 180, wantMin: 10},
		{name: "short", start: "-6h", wantMax: 500, wantMin: 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMax, gotMin := QueryPointBudget(tt.start)
			if gotMax != tt.wantMax || gotMin != tt.wantMin {
				t.Fatalf("QueryPointBudget(%q) = (%d, %d), want (%d, %d)", tt.start, gotMax, gotMin, tt.wantMax, tt.wantMin)
			}
		})
	}
}

func TestDownsampleServerDataPointsPreservesExtremesAndBudget(t *testing.T) {
	points := []ServerDataPoint{
		{Timestamp: 1, PlayerCount: 10, Ip: "1.2.3.4", Name: "srv"},
		{Timestamp: 2, PlayerCount: 4, Ip: "1.2.3.4", Name: "srv"},
		{Timestamp: 3, PlayerCount: 9, Ip: "1.2.3.4", Name: "srv"},
		{Timestamp: 4, PlayerCount: 18, Ip: "1.2.3.4", Name: "srv"},
		{Timestamp: 5, PlayerCount: 7, Ip: "1.2.3.4", Name: "srv"},
		{Timestamp: 6, PlayerCount: 15, Ip: "1.2.3.4", Name: "srv"},
		{Timestamp: 7, PlayerCount: 2, Ip: "1.2.3.4", Name: "srv"},
		{Timestamp: 8, PlayerCount: 11, Ip: "1.2.3.4", Name: "srv"},
	}

	downsampled := downsampleServerDataPoints(points, 4)
	if len(downsampled) > 4 {
		t.Fatalf("expected at most 4 points, got %d", len(downsampled))
	}

	wants := map[int64]int{
		1: 10,
		4: 18,
		7: 2,
		8: 11,
	}

	for _, point := range downsampled {
		if expected, ok := wants[point.Timestamp]; ok {
			if point.PlayerCount != expected {
				t.Fatalf("timestamp %d has player_count %d, want %d", point.Timestamp, point.PlayerCount, expected)
			}
			delete(wants, point.Timestamp)
		}
	}

	if len(wants) != 0 {
		t.Fatalf("missing expected anchor points: %#v", wants)
	}
}
