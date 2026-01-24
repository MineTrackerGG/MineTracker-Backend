package data

import (
	"MineTracker/database"
	"context"
	"encoding/json"
	"fmt"
	"os"
)

type PingableServer struct {
	Name     string `json:"name"`
	IP       string `json:"ip"`
	Type     string `json:"type"`
	Interval int    `json:"interval,omitempty"` // Ping interval in seconds, defaults to 5
}

type Server struct {
	Name        string `json:"name"`
	IP          string `json:"ip"`
	Icon        string `json:"icon,omitempty"`
	Type        string `json:"type"`
	Online      bool   `json:"online"`
	PlayerCount int    `json:"player_count"`
	Peak        int    `json:"peak"`
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

var Servers []Server

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
		UseAdaptive:   true,
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
			PlayerCount: int(record.Value().(float64)),
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

	return dataPoints, step, nil
}
