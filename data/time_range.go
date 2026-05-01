package data

import (
	"MineTracker/database"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
)

var allowedTimeRanges = map[string]string{
	"1h":  "10s",
	"6h":  "1m",
	"12h": "2m",
	"24h": "4m",
	"7d":  "30m",
	"30d": "2h",
	"1y":  "1d",
}

// ValidateTimeRange returns the canonical time range and the exact aggregation step.
func ValidateTimeRange(timeRange string) (string, string, error) {
	normalized := strings.TrimSpace(timeRange)
	step, ok := allowedTimeRanges[normalized]
	if !ok {
		return "", "", fmt.Errorf("unsupported time range: %s", timeRange)
	}
	return normalized, step, nil
}

// QueryHistoricalDataPoints queries the historical series for a validated time range.
// The returned step is the exact aggregation step used in the query.
func QueryHistoricalDataPoints(ip string, timeRange string) ([]ServerDataPoint, string, error) {
	canonicalRange, step, err := ValidateTimeRange(timeRange)
	if err != nil {
		return nil, "", err
	}

	query, _, resolvedStep, err := BuildInfluxQueryFromParams(QueryParams{
		Start:         "-" + canonicalRange,
		Step:          step,
		ServerFilter:  ip,
		MaxDataPoints: 5000,
		MinDataPoints: 1,
		UseAdaptive:   false,
	})
	if err != nil {
		return nil, "", err
	}

	queryApi := database.InfluxClient.QueryAPI(os.Getenv("INFLUXDB_ORG"))
	result, err := queryApi.Query(context.Background(), query)
	if err != nil {
		return nil, "", fmt.Errorf("query execution failed: %w", err)
	}
	defer func() { _ = result.Close() }()

	points := make([]ServerDataPoint, 0, 512)
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
			points = append(points, point)
		}
	}

	if result.Err() != nil {
		return nil, "", fmt.Errorf("result error: %w", result.Err())
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

	return points, resolvedStep, nil
}
