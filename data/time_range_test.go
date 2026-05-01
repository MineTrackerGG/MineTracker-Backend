package data

import "testing"

func TestValidateTimeRange(t *testing.T) {
	tests := []struct {
		name      string
		timeRange string
		wantStep  string
		wantErr   bool
	}{
		{name: "valid 1h", timeRange: "1h", wantStep: "10s"},
		{name: "valid 7d", timeRange: "7d", wantStep: "30m"},
		{name: "invalid", timeRange: "168h", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, step, err := ValidateTimeRange(tt.timeRange)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if step != tt.wantStep {
				t.Fatalf("step = %q, want %q", step, tt.wantStep)
			}
		})
	}
}
