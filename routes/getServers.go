package routes

import (
	"MineTracker/task"

	"github.com/gin-gonic/gin"
)

func RegisterGetServers(r *gin.Engine) {
	r.GET("/api/servers", func(c *gin.Context) {
		c.JSON(200, task.GetAllServers())
	})
}
