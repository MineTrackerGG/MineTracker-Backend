package database

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var MongoClient *mongo.Client

func ConnectMongo(uri string) (*mongo.Client, context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(context.Background())
	clientOptions := options.Client().ApplyURI(uri)
	client, err := mongo.Connect(clientOptions)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}

	err = client.Ping(ctx, nil)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}

	return client, ctx, cancel, nil
}
