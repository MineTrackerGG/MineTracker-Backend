package verone

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
)

const chartCSV = `#datatype,string,long,dateTime:RFC3339,long,long,long
#group,false,false,false,false,false,false
#default,points,,,,,
,result,table,_time,players,responding,samples
,,0,2026-10-30T12:00:00Z,10,2,2
,,0,2026-10-30T13:00:00Z,4,1,2

`

func statsCSV(mean string, min, max, samples string) string {
	return `#datatype,string,long,boolean,double,long,long,long
#group,false,false,false,false,false,false,false
#default,stats,,,,,,
,result,table,stats,mean,minimum,maximum,samples
,,1,true,` + mean + `,` + min + `,` + max + `,` + samples + "\n\n"
}
func TestHistoryStatsRawResponseAndWindow(t *testing.T) {
	for _, tc := range []struct {
		name, csv      string
		samples        int64
		mean, min, max float64
	}{
		{"raw differs from chart maxima", chartCSV + statsCSV("5.333333333333333", "2", "10", "3"), 3, 16.0 / 3, 2, 10},
		{"all failed", strings.NewReplacer("10,2,2", ",0,2", "4,1,2", ",0,2").Replace(chartCSV), 0, 0, 0, 0},
		{"zero real", statsCSV("0", "0", "0", "1"), 1, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var body struct {
					Query string `json:"query"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("query body")
				}
				if !strings.Contains(body.Query, `base |> filter(fn: (r) => r._field == "players") |> group(columns: [])`) {
					t.Error("stats not computed from raw players")
				}
				w.Header().Set("Content-Type", "text/csv")
				w.Write([]byte(tc.csv))
			}))
			defer server.Close()
			client := influxdb2.NewClient(server.URL, "")
			defer client.Close()
			store := &DatabaseStore{Influx: client}
			result, err := store.History(context.Background(), testID, "7d")
			if err != nil {
				t.Fatal(err)
			}
			if requests != 1 {
				t.Fatal("stats and points used separate requests")
			}
			if result.Stats.Samples != tc.samples {
				t.Fatal(result.Stats)
			}
			if tc.samples == 0 {
				if result.Stats.Mean != nil || result.Stats.Min != nil || result.Stats.Max != nil {
					t.Fatal("no samples became zero")
				}
				return
			}
			if result.Stats.Mean == nil || math.Abs(*result.Stats.Mean-tc.mean) > 1e-10 || result.Stats.Min == nil || float64(*result.Stats.Min) != tc.min || result.Stats.Max == nil || float64(*result.Stats.Max) != tc.max {
				t.Fatalf("stats %+v", result.Stats)
			}
			if tc.samples == 3 {
				chartMean := float64(*result.Points[0].Players+*result.Points[1].Players) / 2
				if *result.Stats.Mean == chartMean {
					t.Fatal("mean used chart maxima")
				}
			}
		})
	}
	stop := time.Date(2026, 10, 31, 14, 30, 0, 123, time.UTC)
	for _, tc := range []struct {
		period   string
		duration time.Duration
		step     string
	}{{"24h", 24 * time.Hour, "5m"}, {"7d", 7 * 24 * time.Hour, "1h"}, {"30d", 30 * 24 * time.Hour, "3h"}} {
		query := historyQuery("bucket", testID, tc.period, stop)
		if strings.Count(query, "|> range(") != 1 || !strings.Contains(query, stop.Format(time.RFC3339Nano)) || !strings.Contains(query, stop.Add(-tc.duration).Format(time.RFC3339Nano)) || !strings.Contains(query, "every: "+tc.step) {
			t.Fatal("window mismatch", query)
		}
	}
}
func TestHistoryStatsHTTPAdditiveEmpty(t *testing.T) {
	store := &memoryStore{configs: []Config{config()}}
	r := Router(New(store), readSecret, adminSecret)
	response := request(r, "GET", "/v1/servers/"+testID+"/history?period=7d", readSecret, nil)
	var body struct {
		Points      []Point      `json:"points"`
		Period      string       `json:"period"`
		Aggregation string       `json:"aggregation"`
		Stats       HistoryStats `json:"stats"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Points == nil || body.Period != "7d" || body.Aggregation != "maximum" || body.Stats.Samples != 0 || body.Stats.Mean != nil || !strings.Contains(response.Body.String(), `"stats":`) {
		t.Fatal(response.Code, response.Body)
	}
}
