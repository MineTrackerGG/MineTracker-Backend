package database

import (
	"MineTracker/util"
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var MongoClient *mongo.Client

func ConnectMongo(uri string) {
	ctx := context.Background()
	serverAPI := options.ServerAPI(options.ServerAPIVersion1)
	clientOptions := options.Client().ApplyURI(uri).
		SetServerAPIOptions(serverAPI).
		SetMaxPoolSize(100).
		SetMinPoolSize(10).
		SetMaxConnIdleTime(30 * time.Second).
		SetServerSelectionTimeout(5 * time.Second).
		SetConnectTimeout(10 * time.Second)
	client, err := mongo.Connect(clientOptions)
	if err != nil {
		return
	}

	if client == nil {
		util.Logger.Fatal().Msg("MongoDB client is nil")
		return
	}

	err = client.Ping(ctx, nil)
	if err != nil {
		util.Logger.Fatal().Err(err).Msg("Failed to connect to MongoDB")
		return
	}

	MongoClient = client
}
