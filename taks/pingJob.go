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
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

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

func NewServerJob(interval time.Duration, servers []data.PingableServer) *PingJob {
	return &PingJob{
		interval: interval,
		servers:  servers,
	}
}

func (j *PingJob) StartServerJob(ctx context.Context) {
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

func (j *PingJob) run() {
	pinger := mcping.NewPinger()

	jobs := make(chan data.PingableServer, len(j.servers))
	results := make(chan data.Server, len(j.servers))
	failedIps := make([]string, 0)

	workerCount := 10

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute*5)
	defer cancel()

	// Start workers
	for i := 0; i < workerCount; i++ {
		go func() {
			writeApi := database.InfluxClient.WriteAPI(database.GetInfluxOrg(), database.GetInfluxBucket())
			defer writeApi.Flush()

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
						ctx,
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
					ctx,
					bson.M{"ip": server.IP},
					bson.M{"$set": existingServer},
					opts,
				)

				if err != nil {
					util.Logger.Error().Err(err).Msg("Failed to update server data in MongoDB")
					continue
				}

				tags := map[string]string{
					"ip":   server.IP,
					"type": server.Type,
					"name": server.Name,
				}

				fields := map[string]interface{}{
					"player_count": existingServer.PlayerCount,
				}

				point := write.NewPoint("server_data", tags, fields, time.Now())

				websocket.GlobalHub.Broadcast(map[string]interface{}{
					"type": "data_point_add",
					"data": data.ServerDataPoint{
						Timestamp:   point.Time().Unix(),
						PlayerCount: existingServer.PlayerCount,
						Ip:          server.IP,
						Name:        server.Name,
					},
				})

				writeApi.WritePoint(point)

				results <- existingServer
			}
		}()
	}

	go func() {
		for _, server := range j.servers {
			jobs <- server
		}
		close(jobs)
	}()

	go func() {
		time.Sleep(time.Second * 30)
		close(results)
	}()

	var allResults []data.Server
	for result := range results {
		allResults = append(allResults, result)
	}

	cursor, err := database.MongoClient.
		Database("minetracker").
		Collection("servers").
		Find(ctx, bson.M{})

	if err != nil {
		util.Logger.Error().Err(err).Msg("Failed to fetch servers for returning all servers")
		return
	}
	defer func(cursor *mongo.Cursor, ctx context.Context) {
		err := cursor.Close(ctx)
		if err != nil {
			util.Logger.Error().Err(err).Msg("Failed to close MongoDB cursor")
		}
	}(cursor, ctx)

	var allServers []data.Server
	if err := cursor.All(ctx, &allServers); err != nil {
		util.Logger.Error().Err(err).Msg("Failed to decode servers for returning all servers")
		return
	}

	websocket.GlobalHub.Broadcast(
		map[string]interface{}{
			"type":    "servers_update",
			"servers": allServers,
		},
	)
}
