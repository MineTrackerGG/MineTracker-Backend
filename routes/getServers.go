package routes

import (
	"MineTracker/data"
	"MineTracker/database"
	"context"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func RegisterGetServers(r *gin.Engine) {
	r.GET("/api/servers", func(c *gin.Context) {
		collection := database.MongoClient.Database("minetracker").Collection("servers")
		cursor, err := collection.Find(context.Background(), bson.M{})
		if err != nil {
			c.JSON(500, gin.H{"error": "Failed to retrieve servers"})
			return
		}
		var servers []data.Server
		if err := cursor.All(context.Background(), &servers); err != nil {
			c.JSON(500, gin.H{"error": "Failed to parse servers"})
			return
		}
		c.JSON(200, servers)
		cursor.Close(context.Background())
	})
}
