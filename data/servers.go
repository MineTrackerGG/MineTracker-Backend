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
	renderMaxDataPoints, minDataPoints := QueryPointBudget(duration)
	queryMaxDataPoints := renderMaxDataPoints
	if durationLongerThanADay(duration) {
		queryMaxDataPoints = renderMaxDataPoints * 4
	}

	query, _, step, err := BuildInfluxQueryFromParams(QueryParams{
		Start:         duration,
		ServerFilter:  ip,
		MaxDataPoints: queryMaxDataPoints,
		MinDataPoints: minDataPoints,
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

		playerCount, ok := recordValueToInt(record.Value())
		if !ok {
			continue
		}

		dataPoint := ServerDataPoint{
			Timestamp:   record.Time().Unix(),
			PlayerCount: playerCount,
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

	points := mergeServerDataPoints(dataPoints, extremes)
	points = downsampleServerDataPoints(points, renderMaxDataPoints)

	return points, step, nil
}

// QueryDataPointsWithStep returns the full aggregated series for a fixed step without additional downsampling.
// This is used for websocket initial_data payloads where the frontend should receive the exact series
// that matches the chosen chart range.
func QueryDataPointsWithStep(ip string, duration string, step string) ([]ServerDataPoint, string, error) {
	queryApi := database.InfluxClient.QueryAPI(os.Getenv("INFLUXDB_ORG"))

	query, err := BuildInfluxQuery(duration, step, ip)
	if err != nil {
		return nil, step, fmt.Errorf("failed to build query: %w", err)
	}

	result, err := queryApi.Query(context.Background(), query)
	if err != nil {
		return nil, step, fmt.Errorf("query execution failed: %w", err)
	}
	defer func() { _ = result.Close() }()

	var dataPoints []ServerDataPoint
	for result.Next() {
		record := result.Record()
		if record == nil {
			continue
		}

		playerCount, ok := recordValueToInt(record.Value())
		if !ok {
			continue
		}

		point := ServerDataPoint{
			Timestamp:   record.Time().Unix(),
			PlayerCount: playerCount,
			Ip:          record.ValueByKey("ip").(string),
			Name:        record.ValueByKey("name").(string),
		}

		if ip == "" || point.Ip == ip {
			dataPoints = append(dataPoints, point)
		}
	}

	if result.Err() != nil {
		return nil, step, fmt.Errorf("result error: %w", result.Err())
	}

	sort.Slice(dataPoints, func(i, j int) bool {
		if dataPoints[i].Timestamp != dataPoints[j].Timestamp {
			return dataPoints[i].Timestamp < dataPoints[j].Timestamp
		}
		if dataPoints[i].PlayerCount != dataPoints[j].PlayerCount {
			return dataPoints[i].PlayerCount < dataPoints[j].PlayerCount
		}
		if dataPoints[i].Ip != dataPoints[j].Ip {
			return dataPoints[i].Ip < dataPoints[j].Ip
		}
		return dataPoints[i].Name < dataPoints[j].Name
	})

	return dataPoints, step, nil
}

func durationLongerThanADay(duration string) bool {
	rangeInMinutes, err := timeToMinutes(duration)
	if err != nil {
		return false
	}

	return math.Abs(rangeInMinutes) >= 1440
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

		playerCount, ok := recordValueToInt(record.Value())
		if !ok {
			continue
		}

		dataPoint := ServerDataPoint{
			Timestamp:   record.Time().Unix(),
			PlayerCount: playerCount,
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

		playerCount, ok := recordValueToInt(record.Value())
		if !ok {
			continue
		}

		dataPoint := ServerDataPoint{
			Timestamp:   record.Time().Unix(),
			PlayerCount: playerCount,
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

func downsampleServerDataPoints(points []ServerDataPoint, maxPoints int) []ServerDataPoint {
	if maxPoints <= 0 || len(points) <= maxPoints {
		return points
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

	bucketCount := maxPoints / 4
	if bucketCount < 1 {
		bucketCount = 1
	}

	target := make([]ServerDataPoint, 0, maxPoints)
	seen := make(map[string]struct{}, maxPoints)
	bucketSize := float64(len(points)) / float64(bucketCount)

	appendPoint := func(point ServerDataPoint) {
		key := fmt.Sprintf("%d|%d|%s|%s", point.Timestamp, point.PlayerCount, point.Ip, point.Name)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		target = append(target, point)
	}

	for bucket := 0; bucket < bucketCount; bucket++ {
		start := int(math.Floor(float64(bucket) * bucketSize))
		end := int(math.Floor(float64(bucket+1) * bucketSize))
		if bucket == bucketCount-1 {
			end = len(points)
		}
		if start < 0 {
			start = 0
		}
		if end > len(points) {
			end = len(points)
		}
		if start >= end {
			continue
		}

		bucketPoints := points[start:end]
		first := bucketPoints[0]
		last := bucketPoints[len(bucketPoints)-1]
		minPoint := first
		maxPoint := first

		for _, point := range bucketPoints[1:] {
			if point.PlayerCount < minPoint.PlayerCount || (point.PlayerCount == minPoint.PlayerCount && point.Timestamp < minPoint.Timestamp) {
				minPoint = point
			}
			if point.PlayerCount > maxPoint.PlayerCount || (point.PlayerCount == maxPoint.PlayerCount && point.Timestamp < maxPoint.Timestamp) {
				maxPoint = point
			}
		}

		bucketSelection := []ServerDataPoint{first, minPoint, maxPoint, last}
		for _, point := range bucketSelection {
			appendPoint(point)
		}
	}

	sort.Slice(target, func(i, j int) bool {
		if target[i].Timestamp != target[j].Timestamp {
			return target[i].Timestamp < target[j].Timestamp
		}
		if target[i].PlayerCount != target[j].PlayerCount {
			return target[i].PlayerCount < target[j].PlayerCount
		}
		if target[i].Ip != target[j].Ip {
			return target[i].Ip < target[j].Ip
		}
		return target[i].Name < target[j].Name
	})

	if len(target) > maxPoints {
		return target[:maxPoints]
	}

	return target
}

func recordValueToInt(value interface{}) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int8:
		return int(v), true
	case int16:
		return int(v), true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case uint:
		return int(v), true
	case uint8:
		return int(v), true
	case uint16:
		return int(v), true
	case uint32:
		return int(v), true
	case uint64:
		return int(v), true
	case float32:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}
