package task

import (
	"MineTracker/data"
	"MineTracker/database"
	"MineTracker/util"
	"MineTracker/websocket"
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

const (
	influxQueueSize = 500
	bulkFlushSize   = 100
)

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
	next := time.Now()

	for {
		next = next.Add(j.interval)

		j.run()

		sleep := time.Until(next)
		if sleep < 0 {
			continue
		}

		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			util.Logger.Info().Msg("Stopped data ping job.")
			return
		}
	}
}

var influxQueue = make(chan *write.Point, influxQueueSize)
var droppedInfluxPoints uint64

func StartInfluxWriter(ctx context.Context) {
	writeApi := database.InfluxClient.
		WriteAPI(database.GetInfluxOrg(), database.GetInfluxBucket())

	go func() {
		defer writeApi.Flush()

		for {
			select {
			case point := <-influxQueue:
				writeApi.WritePoint(point)

			case <-ctx.Done():
				return
			}
		}
	}()
}

func calcWorkerCount(avgPing time.Duration) int {
	switch {
	case avgPing < 200*time.Millisecond:
		return 6
	case avgPing < 500*time.Millisecond:
		return 4
	default:
		return 2
	}
}

func (j *PingJob) run() {
	pinger := mcping.NewPinger()

	jobs := make(chan data.PingableServer, len(j.servers))
	results := make(chan data.Server, len(j.servers))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var avgPing = 300 * time.Millisecond
	workerCount := calcWorkerCount(avgPing)

	var wg sync.WaitGroup
	var pingMu sync.Mutex

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			collection := database.MongoClient.
				Database("minetracker").
				Collection("servers")

			var bulkOps []mongo.WriteModel

			for server := range jobs {
				start := time.Now()

				host, port := parseAddress(server.IP)
				resp, err := pinger.PingWithTimeout(
					host,
					portOrDefault(port, 25565),
					time.Second,
				)
				if err != nil {
					continue
				}

				websocket.GlobalHub.SendToServer(server.IP, map[string]interface{}{
					"type": "data_point_rt",
					"data": data.ServerDataPoint{
						Timestamp:   time.Now().Unix(),
						PlayerCount: resp.PlayerCount.Online,
						Ip:          server.IP,
						Name:        server.Name,
					},
				})

				pingTime := time.Since(start)

				// EWMA
				pingMu.Lock()
				avgPing = (avgPing*4 + pingTime) / 5
				pingMu.Unlock()

				var existing data.Server
				_ = collection.FindOne(ctx, bson.M{"ip": server.IP}).Decode(&existing)

				existing.Name = server.Name
				existing.IP = server.IP
				existing.Type = server.Type
				existing.Online = true

				pc := resp.PlayerCount.Online
				if pc > 0 {
					existing.PlayerCount = pc
				}
				if pc > existing.Peak {
					existing.Peak = pc
				}
				if resp.Favicon != "" {
					existing.Icon = resp.Favicon
				}

				model := mongo.NewUpdateOneModel().
					SetFilter(bson.M{"ip": server.IP}).
					SetUpdate(bson.M{"$set": existing}).
					SetUpsert(true)

				bulkOps = append(bulkOps, model)

				if len(bulkOps) >= bulkFlushSize {
					_, _ = collection.BulkWrite(
						ctx,
						bulkOps,
						options.BulkWrite().SetOrdered(false),
					)
					bulkOps = bulkOps[:0]
				}

				point := write.NewPoint(
					"server_data",
					map[string]string{
						"ip":   server.IP,
						"type": server.Type,
						"name": server.Name,
					},
					map[string]interface{}{
						"player_count": existing.PlayerCount,
					},
					time.Now(),
				)

				select {
				case influxQueue <- point:
				default:
					atomic.AddUint64(&droppedInfluxPoints, 1)
				}

				results <- existing
			}

			if len(bulkOps) > 0 {
				_, _ = collection.BulkWrite(
					ctx,
					bulkOps,
					options.BulkWrite().SetOrdered(false),
				)
			}
		}()
	}

	for _, s := range j.servers {
		jobs <- s
	}
	close(jobs)

	go func() {
		wg.Wait()
		close(results)
	}()

	// WS Batch
	var wsBatch []data.ServerDataPoint
	now := time.Now().Unix()

	for s := range results {
		wsBatch = append(wsBatch, data.ServerDataPoint{
			Timestamp:   now,
			PlayerCount: s.PlayerCount,
			Ip:          s.IP,
			Name:        s.Name,
		})
	}

	if len(wsBatch) > 0 {
		websocket.GlobalHub.Broadcast(map[string]interface{}{
			"type": "data_point_batch",
			"data": wsBatch,
		})
	}

	cursor, err := database.MongoClient.
		Database("minetracker").
		Collection("servers").
		Find(ctx, bson.M{})
	if err != nil {
		return
	}
	defer func(cursor *mongo.Cursor, ctx context.Context) {
		_ = cursor.Close(ctx)
	}(cursor, ctx)

	var all []data.Server
	if err := cursor.All(ctx, &all); err != nil {
		return
	}

	websocket.GlobalHub.Broadcast(map[string]interface{}{
		"type":    "servers_update",
		"servers": all,
	})
}
