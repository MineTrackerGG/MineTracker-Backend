package main

import (
	"MineTracker/database"
	"MineTracker/util"
	"MineTracker/verone"
	"context"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

func main() {
	readToken, adminToken := os.Getenv("MONITOR_READ_TOKEN"), os.Getenv("MONITOR_ADMIN_TOKEN")
	if len(readToken) < 32 || len(adminToken) < 32 || readToken == adminToken {
		util.Logger.Fatal().Msg("Set distinct monitor tokens of at least 32 characters")
	}
	if os.Getenv("MONGO_URI") == "" || os.Getenv("INFLUXDB_BUCKET") == "" {
		util.Logger.Fatal().Msg("Database configuration missing")
	}
	gin.SetMode(gin.ReleaseMode)
	database.ConnectMongo(os.Getenv("MONGO_URI"))
	if database.MongoClient == nil {
		util.Logger.Fatal().Msg("MongoDB unavailable")
	}
	if err := database.ConnectInflux(); err != nil {
		util.Logger.Fatal().Msg("InfluxDB unavailable")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s := verone.New(&verone.DatabaseStore{Mongo: database.MongoClient.Database("minetracker"), Influx: database.InfluxClient})
	var hostingDone chan struct{}
	if panelURL, token := os.Getenv("PTERODACTYL_URL"), os.Getenv("PTERODACTYL_APPLICATION_TOKEN"); panelURL != "" || token != "" {
		source, err := verone.NewPterodactyl(panelURL, token)
		if err != nil {
			util.Logger.Fatal().Msg("Invalid Pterodactyl configuration")
		}
		interval := 300
		if value := os.Getenv("PTERODACTYL_SYNC_INTERVAL"); value != "" {
			interval, err = strconv.Atoi(value)
			if err != nil || interval < 60 || interval > 3600 {
				util.Logger.Fatal().Msg("Invalid inventory sync interval")
			}
		}
		s.Hosting = &verone.Hosting{Service: s, Repository: &verone.MongoHostingRepository{DB: database.MongoClient.Database("minetracker")}, Source: source, Panel: panelURL, Interval: time.Duration(interval) * time.Second}
		hostingDone = make(chan struct{})
		go func() { defer close(hostingDone); s.Hosting.Run(ctx) }()
	}
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	port := os.Getenv("HTTP_PORT")
	if port == "" {
		port = "8080"
	}
	server := &http.Server{Addr: ":" + port, Handler: verone.Router(s, readToken, adminToken), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			util.Logger.Error().Msg("monitor_http_failed")
			stop()
		}
	}()
	util.Logger.Info().Msg("verone_monitor_started")
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
	<-done
	if hostingDone != nil {
		<-hostingDone
	}
	database.InfluxClient.Close()
	_ = database.MongoClient.Disconnect(shutdown)
}
