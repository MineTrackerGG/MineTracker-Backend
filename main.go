package main

import (
	"MineTracker/data"
	"MineTracker/database"
	"MineTracker/routes"
	"MineTracker/task"
	"MineTracker/util"
	"MineTracker/websocket"
	"context"
	"expvar"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

const serversConfigPath = "servers.json"

func main() {
	_ = godotenv.Load()

	database.ConnectMongo(os.Getenv("MONGO_URI"))

	ctx, serverJobCancel := context.WithCancel(context.Background())

	util.Logger.Info().Msg("Connected to MongoDB!")

	err := database.ConnectInflux()
	if err != nil {
		util.Logger.Fatal().Err(err).Msg("Failed to connect to InfluxDB")
		panic(err)
	}

	util.Logger.Info().Msg("Connected to InfluxDB!")

	Servers, err := data.LoadServers(serversConfigPath)

	if err != nil {
		util.Logger.Fatal().Err(err).Msg("Failed to load servers.json")
	}

	pingJob := task.NewServerJob(0, Servers)

	task.StartInfluxWriter(ctx)
	task.StartDBWriter(ctx)
	task.StartActiveStatusSync(ctx)

	go pingJob.StartServerJob(ctx)
	go watchServerConfig(ctx, pingJob)

	err = task.LoadServerCache(ctx)
	if err != nil {
		util.Logger.Warn().Err(err).Msg("Failed to load server cache from MongoDB")
	}
	task.ApplyServerConfig(Servers)

	err = data.InitCache()
	if err != nil {
		util.Logger.Fatal().Err(err).Msg("Failed to initialize server cache")
		return
	}

	util.Logger.Info().Msg("Loaded " + strconv.Itoa(len(Servers)) + " servers from " + serversConfigPath)

	go func() {
		if os.Getenv("DEPLOYMENT_MODE") == "production" || os.Getenv("DEPLOYMENT_MODE") == "release" {
			gin.SetMode(gin.ReleaseMode)
		} else {
			gin.SetMode(gin.DebugMode)
		}

		r := gin.Default()

		r.Use(cors.New(cors.Config{
			AllowOrigins:     []string{os.Getenv("FRONTEND_URL")},
			AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
			AllowHeaders:     []string{"Origin", "Content-Type", "Accept"},
			ExposeHeaders:    []string{"Content-Length"},
			AllowCredentials: true,
			MaxAge:           12 * time.Hour,
		}))

		routes.RegisterGetDatedDataRoute(r)
		routes.RegisterGetBulkDatedDataRoute(r)
		routes.RegisterGetServers(r)
		routes.RegisterGetVersionRoute(r)
		r.GET("/debug/vars", gin.WrapH(expvar.Handler()))

		r.GET("/ws", func(c *gin.Context) {
			websocket.HandleWebSocket(c.Writer, c.Request)
		})

		util.Logger.Info().Msg("Started HTTP and WebSocket server on :" + os.Getenv("HTTP_PORT"))
		if err := r.Run(":" + os.Getenv("HTTP_PORT")); err != nil {
			util.Logger.Fatal().Err(err).Msg("Server crashed")
		}
	}()

	if os.Getenv("PROFILING_ENABLED") == "true" {
		go func() {
			util.Logger.Info().Msg("pprof listening on :" + os.Getenv("PROFILING_PORT"))
			if err := http.ListenAndServe(os.Getenv("PROFILING_HOST")+":"+os.Getenv("PROFILING_PORT"), nil); err != nil {
				util.Logger.Error().Err(err).Msg("pprof server failed")
			}
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()
	err = database.MongoClient.Disconnect(ctx)
	if err != nil {
		util.Logger.Error().Err(err).Msg("Failed to disconnect MongoDB client")
		return
	}
	serverJobCancel()
	util.Logger.Info().Msg("Shutting down MineTracker...")
}

func watchServerConfig(ctx context.Context, pingJob *task.PingJob) {
	var lastModNano atomic.Int64

	stat, err := os.Stat(serversConfigPath)
	if err == nil {
		lastModNano.Store(stat.ModTime().UnixNano())
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			stat, err := os.Stat(serversConfigPath)
			if err != nil {
				util.Logger.Warn().Err(err).Msg("Failed to stat servers config")
				continue
			}

			modNano := stat.ModTime().UnixNano()
			if modNano == lastModNano.Load() {
				continue
			}

			servers, err := data.LoadServers(serversConfigPath)
			if err != nil {
				util.Logger.Warn().Err(err).Msg("Failed to reload servers config")
				continue
			}

			pingJob.UpdateServers(servers)
			task.ApplyServerConfig(servers)
			lastModNano.Store(modNano)
			util.Logger.Info().Msg("Reloaded servers config")

		case <-ctx.Done():
			return
		}
	}
}
