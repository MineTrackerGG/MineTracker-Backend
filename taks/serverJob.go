package task

import (
	"MineTracker/data"
	"MineTracker/database"
	"MineTracker/util"
	"MineTracker/websocket"
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/influxdata/influxdb-client-go/v2/api/write"
	"github.com/iverly/go-mcping/mcping"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Job struct {
	interval time.Duration
	servers  []data.PingableServer
}

type PingResult struct {
	success     bool
	playerCount int
}

func parseAddress(addr string) (host string, port *uint16) {
	parts := strings.Split(addr, ":")
	host = parts[0]

	if len(parts) == 2 {
		p, err := strconv.ParseUint(parts[1], 10, 16)
		if err == nil {
			parsed := uint16(p)
			port = &parsed
		}
	}

	return
}

func portOrDefault(port *uint16, def uint16) uint16 {
	if port == nil {
		return def
	}
	return *port
}

func NewServerJob(interval time.Duration, servers []data.PingableServer) *Job {
	return &Job{
		interval: interval,
		servers:  servers,
	}
}

func (j *Job) Start(ctx context.Context) {
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	j.run()

	for {
		select {
		case <-ticker.C:
			j.run()
		case <-ctx.Done():
			util.Logger.Info().Msg("Stopped data ping job.")
			return
		}
	}
}

func (j *Job) run() {
	pinger := mcping.NewPinger()

	jobs := make(chan data.PingableServer)
	failedIps := make([]string, 0)

	workerCount := 10

	var results []data.Server

	// Start workers
	for i := 0; i < workerCount; i++ {
		go func() {
			writeApi := database.InfluxClient.WriteAPI(database.GetInfluxOrg(), database.GetInfluxBucket())

			for server := range jobs {

				host, port := parseAddress(server.IP)

				resp, err := pinger.PingWithTimeout(
					host,
					portOrDefault(port, 25565),
					time.Second*1,
				)

				if err != nil {
					failedIps = append(failedIps, server.IP)
					continue
				}

				collection := database.MongoClient.
					Database("minetracker").
					Collection("servers")

				var existingServer data.Server

				err = collection.
					FindOne(
						context.Background(),
						bson.M{"ip": server.IP},
					).
					Decode(&existingServer)

				if err != nil {
					existingServer = data.Server{
						Name:        server.Name,
						IP:          server.IP,
						Type:        server.Type,
						Online:      true,
						PlayerCount: resp.PlayerCount.Online,
						Peak:        resp.PlayerCount.Online,
						Icon:        resp.Favicon,
					}
				}

				existingServer.Online = true
				playerCount := resp.PlayerCount.Online
				if playerCount > 0 {
					existingServer.PlayerCount = playerCount
				}

				if resp.Favicon != existingServer.Icon {
					existingServer.Icon = resp.Favicon
				}

				if resp.PlayerCount.Online > existingServer.Peak {
					existingServer.Peak = resp.PlayerCount.Online
				}

				opts := options.UpdateOne().SetUpsert(true)

				_, err = collection.UpdateOne(
					context.Background(),
					bson.M{
						"ip": server.IP,
					},
					bson.M{
						"$set": existingServer,
					},
					opts,
				)

				tags := map[string]string{
					"ip":   server.IP,
					"type": server.Type,
					"name": server.Name,
				}

				fields := map[string]interface{}{
					"player_count": existingServer.PlayerCount,
					"peak":         existingServer.Peak,
				}

				point := write.NewPoint("server_data", tags, fields, time.Now())

				writeApi.WritePoint(point)

				results = append(results, existingServer)

				if err != nil {
					util.Logger.Error().Err(err).Msg("Failed to update server data in MongoDB")
				}
			}

			writeApi.Flush()

			websocket.GlobalHub.Broadcast(
				map[string]interface{}{
					"type":    "servers_update",
					"servers": results,
				},
			)
		}()
	}

	// Feed jobs
	go func() {
		for _, server := range j.servers {
			jobs <- server
		}
		close(jobs)
	}()
}
