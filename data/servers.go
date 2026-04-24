package data

import (
	"MineTracker/database"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
)

type PingableServer struct {
	Name     string `json:"name"`
	IP       string `json:"ip"`
	Type     string `json:"type"`
	Interval int    `json:"interval,omitempty"`
}

type Server struct {
	Name        string `json:"name"`
	IP          string `json:"ip"`
	Icon        string `json:"icon,omitempty"`
	Type        string `json:"type"`
	Online      bool   `json:"online"`
	PlayerCount int    `json:"player_count"`
	Peak        int    `json:"peak"`
	Active      bool   `json:"active"`
}

type ExtendedServer struct {
	PingableServer
	Icon    string `json:"icon,omitempty"`
	Online  bool   `json:"online"`
	Current int    `json:"current_players"`
	Peak    int    `json:"peak_players"`
	Mean    int    `json:"mean_players"`
	Lowest  int    `json:"lowest_players"`
}

type ServerDataPoint struct {
	Timestamp   int64  `json:"timestamp"`
	PlayerCount int    `json:"player_count"`
	Ip          string `json:"ip"`
	Name        string `json:"name"`
}

func LoadServers(path string) ([]PingableServer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var servers []PingableServer
	if err := json.Unmarshal(data, &servers); err != nil {
		return nil, err
	}

	return servers, nil
}

func QueryDataPoints(ip string, duration string) ([]ServerDataPoint, string, error) {
	queryApi := database.InfluxClient.QueryAPI(os.Getenv("INFLUXDB_ORG"))

	query, _, step, err := BuildInfluxQueryFromParams(QueryParams{
		Start:         duration,
		ServerFilter:  ip,
		MaxDataPoints: 500,
		MinDataPoints: 10,
		UseAdaptive:   false,
	})

	if err != nil {
		return nil, "0m", fmt.Errorf("failed to build query: %w", err)
	}

	result, err := queryApi.Query(context.Background(), query)

	if err != nil {
		return nil, "0m", fmt.Errorf("query execution failed: %w", err)
	}

	var dataPoints []ServerDataPoint

	for result.Next() {
		record := result.Record()
		if record == nil {
			continue
		}

		dataPoint := ServerDataPoint{
			Timestamp:   record.Time().Unix(),
			PlayerCount: int(math.Round(record.Value().(float64))),
			Ip:          record.ValueByKey("ip").(string),
			Name:        record.ValueByKey("name").(string),
		}

		if ip == "" || dataPoint.Ip == ip {
			dataPoints = append(dataPoints, dataPoint)
		}
	}

	if result.Err() != nil {
		return nil, "0m", fmt.Errorf("result error: %w", result.Err())
	}

	_ = result.Close()

	extremes, err := queryExtremeDataPoints(ip, duration)
	if err != nil {
		return nil, "0m", err
	}

	return mergeServerDataPoints(dataPoints, extremes), step, nil
}

func queryExtremeDataPoints(ip string, duration string) ([]ServerDataPoint, error) {
	queryApi := database.InfluxClient.QueryAPI(os.Getenv("INFLUXDB_ORG"))

	baseQuery := fmt.Sprintf(`from(bucket: "minetracker_data")
  |> range(start:  %s)
  |> filter(fn: (r) => r["_measurement"] == "server_data")
  |> filter(fn: (r) => r["_field"] == "player_count")`, duration)

	if ip != "" {
		baseQuery += fmt.Sprintf(`
  |> filter(fn:  (r) => r["ip"] == "%s")`, ip)
	}

	maxResult, err := queryApi.Query(context.Background(), baseQuery+`
  |> top(n: 1, columns: ["_value"])`)
	if err != nil {
		return nil, fmt.Errorf("query execution failed for max point: %w", err)
	}
	defer func() { _ = maxResult.Close() }()

	minResult, err := queryApi.Query(context.Background(), baseQuery+`
  |> bottom(n: 1, columns: ["_value"])`)
	if err != nil {
		return nil, fmt.Errorf("query execution failed for min point: %w", err)
	}
	defer func() { _ = minResult.Close() }()

	var dataPoints []ServerDataPoint

	for maxResult.Next() {
		record := maxResult.Record()
		if record == nil {
			continue
		}

		dataPoint := ServerDataPoint{
			Timestamp:   record.Time().Unix(),
			PlayerCount: int(math.Round(record.Value().(float64))),
			Ip:          record.ValueByKey("ip").(string),
			Name:        record.ValueByKey("name").(string),
		}

		if ip == "" || dataPoint.Ip == ip {
			dataPoints = append(dataPoints, dataPoint)
		}
	}

	if maxResult.Err() != nil {
		return nil, fmt.Errorf("result error: %w", maxResult.Err())
	}

	for minResult.Next() {
		record := minResult.Record()
		if record == nil {
			continue
		}

		dataPoint := ServerDataPoint{
			Timestamp:   record.Time().Unix(),
			PlayerCount: int(math.Round(record.Value().(float64))),
			Ip:          record.ValueByKey("ip").(string),
			Name:        record.ValueByKey("name").(string),
		}

		if ip == "" || dataPoint.Ip == ip {
			dataPoints = append(dataPoints, dataPoint)
		}
	}

	if minResult.Err() != nil {
		return nil, fmt.Errorf("result error: %w", minResult.Err())
	}

	return dataPoints, nil
}

func mergeServerDataPoints(base []ServerDataPoint, extras []ServerDataPoint) []ServerDataPoint {
	merged := make([]ServerDataPoint, 0, len(base)+len(extras))
	seen := make(map[string]struct{}, len(base)+len(extras))

	addPoint := func(point ServerDataPoint) {
		key := fmt.Sprintf("%d|%d|%s|%s", point.Timestamp, point.PlayerCount, point.Ip, point.Name)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		merged = append(merged, point)
	}

	for _, point := range base {
		addPoint(point)
	}
	for _, point := range extras {
		addPoint(point)
	}

	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Timestamp != merged[j].Timestamp {
			return merged[i].Timestamp < merged[j].Timestamp
		}
		if merged[i].PlayerCount != merged[j].PlayerCount {
			return merged[i].PlayerCount < merged[j].PlayerCount
		}
		if merged[i].Ip != merged[j].Ip {
			return merged[i].Ip < merged[j].Ip
		}
		return merged[i].Name < merged[j].Name
	})

	return merged
}
