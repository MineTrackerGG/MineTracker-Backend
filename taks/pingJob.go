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
	influxQueueSize  = 500
	bulkFlushSize    = 300
	dbWriteQueueSize = 1000
)

type dbWriteOp struct {
	server data.Server
}

var dbWriteQueue = make(chan dbWriteOp, dbWriteQueueSize)
var serverCache sync.Map

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
	var wg sync.WaitGroup

	// Start individual goroutine for each server
	for _, server := range j.servers {
		wg.Add(1)
		go func(srv data.PingableServer) {
			defer wg.Done()
			j.runServerLoop(ctx, srv)
		}(server)
	}

	<-ctx.Done()
	util.Logger.Info().Msg("Stopped data ping job.")
	wg.Wait()
}

func (j *PingJob) runServerLoop(ctx context.Context, server data.PingableServer) {
	// Register for subscription change notifications
	notifyChan := websocket.GlobalHub.RegisterServerNotify(server.IP)

	// Custom interval from config (optional)
	var customInterval time.Duration
	if server.Interval > 0 {
		customInterval = time.Duration(server.Interval) * time.Second
	}

	// Adaptive ticker - starts at 10 seconds, adjusts based on subscriptions
	getCurrentInterval := func() time.Duration {
		if customInterval > 0 {
			return customInterval
		}

		// Check if anyone is subscribed to this server
		if websocket.GlobalHub.IsSubscribed(server.IP) {
			return 1 * time.Second // Fast updates for subscribed servers
		}
		return 10 * time.Second // Slow updates for unsubscribed servers
	}

	ticker := time.NewTicker(getCurrentInterval())
	defer ticker.Stop()

	// Ping immediately on start
	j.pingServer(server)

	lastInterval := getCurrentInterval()

	for {
		select {
		case <-ticker.C:
			j.pingServer(server)

			// Check if interval needs adjustment after ping
			newInterval := getCurrentInterval()
			if newInterval != lastInterval {
				ticker.Reset(newInterval)
				lastInterval = newInterval
			}

		case <-notifyChan:
			// Subscription status changed - immediately adjust interval
			newInterval := getCurrentInterval()
			if newInterval != lastInterval {
				ticker.Reset(newInterval)
				lastInterval = newInterval

				// Ping immediately on subscription to give instant data
				if newInterval < lastInterval {
					j.pingServer(server)
				}
			}

		case <-ctx.Done():
			return
		}
	}
}

var influxQueue = make(chan *write.Point, influxQueueSize)
var droppedInfluxPoints uint64

func LoadServerCache(ctx context.Context) error {
	collection := database.MongoClient.
		Database("minetracker").
		Collection("servers")

	cursor, err := collection.Find(ctx, bson.M{})
	if err != nil {
		return err
	}
	defer cursor.Close(ctx)

	var servers []data.Server
	if err := cursor.All(ctx, &servers); err != nil {
		return err
	}

	for _, server := range servers {
		serverCache.Store(server.IP, server)
	}

	return nil
}

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

func StartDBWriter(ctx context.Context) {
	collection := database.MongoClient.
		Database("minetracker").
		Collection("servers")

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		var bulkOps []mongo.WriteModel

		flush := func() {
			if len(bulkOps) > 0 {
				_, _ = collection.BulkWrite(
					ctx,
					bulkOps,
					options.BulkWrite().SetOrdered(false),
				)
				bulkOps = bulkOps[:0]
			}
		}

		for {
			select {
			case op := <-dbWriteQueue:
				model := mongo.NewUpdateOneModel().
					SetFilter(bson.M{"ip": op.server.IP}).
					SetUpdate(bson.M{"$set": op.server}).
					SetUpsert(true)
				bulkOps = append(bulkOps, model)

				if len(bulkOps) >= bulkFlushSize {
					flush()
				}

			case <-ticker.C:
				flush()

			case <-ctx.Done():
				flush()
				return
			}
		}
	}()
}

func (j *PingJob) pingServer(server data.PingableServer) {
	pinger := mcping.NewPinger()

	host, port := parseAddress(server.IP)
	resp, err := pinger.PingWithTimeout(
		host,
		portOrDefault(port, 25565),
		2*time.Second,
	)
	if err != nil {
		// Server offline, still update cache
		if cached, ok := serverCache.Load(server.IP); ok {
			existing := cached.(data.Server)
			existing.Online = false
			serverCache.Store(server.IP, existing)

			select {
			case dbWriteQueue <- dbWriteOp{server: existing}:
			default:
			}
		}
		return
	}

	pc := resp.PlayerCount.Online

	// INSTANT WebSocket update - happens immediately when ping completes
	websocket.GlobalHub.SendToServer(server.IP, map[string]interface{}{
		"type": "data_point_rt",
		"data": data.ServerDataPoint{
			Timestamp:   time.Now().Unix(),
			PlayerCount: pc,
			Ip:          server.IP,
			Name:        server.Name,
		},
	})

	// Get cached server data or create new
	var existing data.Server
	if cached, ok := serverCache.Load(server.IP); ok {
		existing = cached.(data.Server)
	}

	existing.Name = server.Name
	existing.IP = server.IP
	existing.Type = server.Type
	existing.Online = true

	if pc > 0 {
		existing.PlayerCount = pc
	}
	if pc > existing.Peak {
		existing.Peak = pc
	}
	if resp.Favicon != "" {
		existing.Icon = resp.Favicon
	}

	// Update cache
	serverCache.Store(server.IP, existing)

	// Queue async DB write (non-blocking)
	select {
	case dbWriteQueue <- dbWriteOp{server: existing}:
	default:
		// Queue full, skip this write
	}

	// Queue InfluxDB write (non-blocking)
	point := write.NewPoint(
		"server_data",
		map[string]string{
			"ip":   server.IP,
			"type": server.Type,
			"name": server.Name,
		},
		map[string]interface{}{
			"player_count": pc,
		},
		time.Now(),
	)

	select {
	case influxQueue <- point:
	default:
		atomic.AddUint64(&droppedInfluxPoints, 1)
	}
}
