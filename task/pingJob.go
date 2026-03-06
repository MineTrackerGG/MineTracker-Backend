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
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	influxQueueSize  = 500
	bulkFlushSize    = 300
	dbWriteQueueSize = 1000

	// maxConcurrentPings caps simultaneous active ping calls.
	// Each call allocates a TCP connection + bufio buffer; keeping a hard cap
	// prevents a latency spike on slow servers from stacking up goroutines.
	maxConcurrentPings = 20
)

type dbWriteOp struct {
	server data.Server
}

var dbWriteQueue = make(chan dbWriteOp, dbWriteQueueSize)

// pingLimit is a counting semaphore that limits concurrent TCP ping calls.
var pingLimit = make(chan struct{}, maxConcurrentPings)

var (
	serverCacheMu  sync.RWMutex
	serverCacheMap = make(map[string]data.Server, 128)
)

// GetAllServers returns a snapshot of every active server's live state.
func GetAllServers() []data.Server {
	serverCacheMu.RLock()
	defer serverCacheMu.RUnlock()
	result := make([]data.Server, 0, len(serverCacheMap))
	for _, s := range serverCacheMap {
		if s.Active && s.Online {
			result = append(result, s)
		}
	}
	return result
}

// isServerActive reports whether a server is marked active in the cache.
// Servers not yet cached (first encounter) are treated as active by default.
func isServerActive(ip string) bool {
	serverCacheMu.RLock()
	defer serverCacheMu.RUnlock()
	s, ok := serverCacheMap[ip]
	if !ok {
		return true
	}
	return s.Active
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
	var wg sync.WaitGroup

	// Spread goroutine startup evenly across one full tick interval so that
	// pings never all fire simultaneously. Without this, all 63 goroutines
	// wake at t=0, 1, 2, ... causing a burst of allocations that triggers
	// continuous GC (bufio.Reader, json.Decoder, maps per ping × 63).
	var stagger time.Duration
	if n := len(j.servers); n > 1 {
		stagger = time.Second / time.Duration(n)
	}

	for i, server := range j.servers {
		wg.Add(1)
		go func(srv data.PingableServer, idx int) {
			defer wg.Done()
			if idx > 0 {
				select {
				case <-time.After(time.Duration(idx) * stagger):
				case <-ctx.Done():
					return
				}
			}
			j.runServerLoop(ctx, srv)
		}(server, i)
	}

	<-ctx.Done()
	util.Logger.Info().Msg("Stopped data ping job.")
	wg.Wait()
}

func (j *PingJob) runServerLoop(ctx context.Context, server data.PingableServer) {
	notifyChan := websocket.GlobalHub.RegisterServerNotify(server.IP)
	defer websocket.GlobalHub.UnregisterServerNotify(server.IP)

	pinger := newPooledPinger()

	var customInterval time.Duration
	if server.Interval > 0 {
		customInterval = time.Duration(server.Interval) * time.Second
	}

	getCurrentInterval := func() time.Duration {
		if customInterval > 0 {
			return customInterval
		}

		if websocket.GlobalHub.IsSubscribed(server.IP) {
			return 1 * time.Second
		}
		return 10 * time.Second
	}

	ticker := time.NewTicker(getCurrentInterval())
	defer ticker.Stop()

	if isServerActive(server.IP) {
		j.pingServer(server, pinger)
	}

	lastInterval := getCurrentInterval()

	for {
		select {
		case <-ticker.C:
			if isServerActive(server.IP) {
				j.pingServer(server, pinger)
			}

			newInterval := getCurrentInterval()
			if newInterval != lastInterval {
				ticker.Reset(newInterval)
				lastInterval = newInterval
			}

		case _, ok := <-notifyChan:
			if !ok {
				return
			}
			newInterval := getCurrentInterval()
			if newInterval != lastInterval {
				ticker.Reset(newInterval)
				lastInterval = newInterval

				if newInterval < lastInterval && isServerActive(server.IP) {
					j.pingServer(server, pinger)
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

	// One-time migration: ensure all existing documents have the active field.
	_, _ = collection.UpdateMany(
		ctx,
		bson.M{"active": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"active": true}},
	)

	cursor, err := collection.Find(ctx, bson.M{})
	if err != nil {
		return err
	}
	defer cursor.Close(ctx)

	var servers []data.Server
	if err := cursor.All(ctx, &servers); err != nil {
		return err
	}

	serverCacheMu.Lock()
	defer serverCacheMu.Unlock()
	for _, server := range servers {
		serverCacheMap[server.IP] = server
	}

	return nil
}

// StartActiveStatusSync periodically refreshes the active flag for each server
// from MongoDB so that admin changes take effect without a backend restart.
func StartActiveStatusSync(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				refreshActiveStatus(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
}

func refreshActiveStatus(ctx context.Context) {
	collection := database.MongoClient.
		Database("minetracker").
		Collection("servers")

	type activeEntry struct {
		IP     string `bson:"ip"`
		Active bool   `bson:"active"`
	}

	cursor, err := collection.Find(
		ctx,
		bson.M{},
		options.Find().SetProjection(bson.M{"ip": 1, "active": 1}),
	)
	if err != nil {
		util.Logger.Warn().Err(err).Msg("Failed to refresh active status from MongoDB")
		return
	}
	defer cursor.Close(ctx)

	var entries []activeEntry
	if err := cursor.All(ctx, &entries); err != nil {
		util.Logger.Warn().Err(err).Msg("Failed to decode active status from MongoDB")
		return
	}

	serverCacheMu.Lock()
	defer serverCacheMu.Unlock()
	for _, e := range entries {
		if s, ok := serverCacheMap[e.IP]; ok {
			s.Active = e.Active
			serverCacheMap[e.IP] = s
		}
	}
}

func StartInfluxWriter(ctx context.Context) {
	writeApi := database.InfluxClient.
		WriteAPI(database.GetInfluxOrg(), database.GetInfluxBucket())

	go func() {
		defer writeApi.Flush()

		for {
			select {
			case point, ok := <-influxQueue:
				if !ok {
					return
				}
				writeApi.WritePoint(point)

			case <-ctx.Done():
				for {
					select {
					case point, ok := <-influxQueue:
						if !ok {
							return
						}
						writeApi.WritePoint(point)
					default:
						return
					}
				}
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
			case op, ok := <-dbWriteQueue:
				if !ok {
					flush()
					return
				}
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
				for {
					select {
					case op, ok := <-dbWriteQueue:
						if !ok {
							return
						}
						model := mongo.NewUpdateOneModel().
							SetFilter(bson.M{"ip": op.server.IP}).
							SetUpdate(bson.M{"$set": op.server}).
							SetUpsert(true)
						bulkOps = append(bulkOps, model)
						if len(bulkOps) >= bulkFlushSize {
							flush()
						}
					default:
						flush()
						return
					}
				}
			}
		}
	}()
}

func (j *PingJob) pingServer(server data.PingableServer, pinger serverPinger) {
	host, port := parseAddress(server.IP)

	// Acquire concurrency slot before opening a TCP connection.
	// This prevents all 63 goroutines from hammering the allocator simultaneously.
	pingLimit <- struct{}{}
	resp, err := pinger.ping(
		host,
		portOrDefault(port, 25565),
		2*time.Second,
	)
	<-pingLimit

	if err != nil {
		serverCacheMu.Lock()
		existing, ok := serverCacheMap[server.IP]
		if ok {
			existing.Online = false
			serverCacheMap[server.IP] = existing
		}
		serverCacheMu.Unlock()

		if ok {
			select {
			case dbWriteQueue <- dbWriteOp{server: existing}:
			default:
			}
		}
		return
	}

	pc := resp.PlayerCount

	websocket.GlobalHub.SendToServer(server.IP, map[string]interface{}{
		"type": "data_point_rt",
		"data": data.ServerDataPoint{
			Timestamp:   time.Now().Unix(),
			PlayerCount: pc,
			Ip:          server.IP,
			Name:        server.Name,
		},
	})

	serverCacheMu.RLock()
	existing, found := serverCacheMap[server.IP]
	serverCacheMu.RUnlock()

	existing.Name = server.Name
	existing.IP = server.IP
	existing.Type = server.Type
	existing.Online = true
	if !found {
		existing.Active = true
	}

	if pc > 0 {
		existing.PlayerCount = pc
	}
	if pc > existing.Peak {
		existing.Peak = pc
	}
	if resp.Favicon != "" {
		existing.Icon = resp.Favicon
	}

	serverCacheMu.Lock()
	serverCacheMap[server.IP] = existing
	serverCacheMu.Unlock()

	select {
	case dbWriteQueue <- dbWriteOp{server: existing}:
	default:
	}

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
