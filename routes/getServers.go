package routes

import (
	"MineTracker/data"

	"github.com/gin-gonic/gin"
)

func RegisterGetServers(r *gin.Engine) {
	r.GET("/api/servers", func(c *gin.Context) {
		c.JSON(200, &data.Servers)
	})
}
