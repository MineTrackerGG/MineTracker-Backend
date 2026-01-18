package main

import (
	"MineTracker/data"
	"MineTracker/database"
	"MineTracker/routes"
	task "MineTracker/taks"
	"MineTracker/util"
	"MineTracker/websocket"
	"context"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	mongo, ctx, _, err := database.ConnectMongo(os.Getenv("MONGO_URI"))
	if err != nil {
		util.Logger.Fatal().Err(err).Msg("Failed to connect to MongoDB")
		panic(err)
	}

	ctx, serverJobCancel := context.WithCancel(context.Background())

	database.MongoClient = mongo

	// check if connected to mongodb
	util.Logger.Info().Msg("Connected to MongoDB!")

	err = database.ConnectInflux()
	if err != nil {
		util.Logger.Fatal().Err(err).Msg("Failed to connect to InfluxDB")
		panic(err)
	}

	util.Logger.Info().Msg("Connected to InfluxDB!")

	Servers, err := data.LoadServers("servers.json")

	data.Servers = Servers

	if err != nil {
		util.Logger.Fatal().Err(err).Msg("Failed to load servers.json")
	}

	pingJob := task.NewServerJob(1*time.Second, Servers)

	go pingJob.StartServerJob(ctx)

	err = data.InitCache()
	if err != nil {
		util.Logger.Fatal().Err(err).Msg("Failed to initialize server cache")
		return
	}

	util.Logger.Info().Msg("Loaded " + strconv.Itoa(int(rune(len(Servers)))) + " servers from servers.json")

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

		r.GET("/ws", func(c *gin.Context) {
			websocket.HandleWebSocket(c.Writer, c.Request)
		})

		util.Logger.Info().Msg("Started HTTP and WebSocket server on :" + os.Getenv("HTTP_PORT"))
		if err := r.Run(":" + os.Getenv("HTTP_PORT")); err != nil {
			util.Logger.Fatal().Err(err).Msg("Server crashed")
		}
	}()

	// Wait for termination signal to gracefully shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()
	serverJobCancel()
	util.Logger.Info().Msg("Shutting down MineTracker...")
}
