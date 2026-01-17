package main

import (
	"MineTracker/data"
	"MineTracker/database"
	task "MineTracker/taks"
	"MineTracker/util"
	"MineTracker/websocket"
	"context"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	mongo, ctx, _, err := database.ConnectMongo(os.Getenv("MONGO_URI"))
	if err != nil {
		util.Logger.Fatal().Err(err).Msg("Failed to connect to MongoDB")
		panic(err)
	}

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

	if err != nil {
		util.Logger.Fatal().Err(err).Msg("Failed to load servers.json")
	}

	util.Logger.Info().Msg("Loaded " + strconv.Itoa(int(rune(len(Servers)))) + " servers from servers.json")

	ctx, serverJobCancel := context.WithCancel(context.Background())

	serverJob := task.NewServerJob(time.Second*1, Servers)
	go serverJob.Start(ctx)

	http.HandleFunc("/ws", websocket.HandleWebSocket)

	util.Logger.Info().Msg("Started MineTracker WebSocket server on :8080")
	util.Logger.Fatal().Err(http.ListenAndServe(":8080", nil))

	// Wait for termination signal to gracefully shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()
	serverJobCancel()
	util.Logger.Info().Msg("Shutting down MineTracker...")
}
