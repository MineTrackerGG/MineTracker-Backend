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
