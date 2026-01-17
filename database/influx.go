package database

import (
	"context"
	"fmt"
	"os"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
)

var InfluxClient influxdb2.Client

func GetInfluxOrg() string {
	return os.Getenv("INFLUXDB_ORG")
}

func GetInfluxBucket() string {
	return os.Getenv("INFLUXDB_BUCKET")
}

func ConnectInflux() error {
	token := os.Getenv("INFLUXDB_TOKEN")
	influxUrl := os.Getenv("INFLUXDB_URL")

	client := influxdb2.NewClient(influxUrl, token)

	ctx := context.Background()
	health, err := client.Health(ctx)
	if err != nil {
		return fmt.Errorf("failed to connect to InfluxDB: %w", err)
	}

	if health.Status != "pass" {
		return fmt.Errorf("InfluxDB health check failed: %s", health.Status)
	}

	InfluxClient = client
	return nil
}
