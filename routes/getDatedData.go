package routes

import (
	"MineTracker/data"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

func RegisterGetDatedDataRoute(r *gin.Engine) {
	r.GET("/api/:server/:time", func(c *gin.Context) {
		server := c.Param("server")
		time := c.Param("time")

		dataPoints, step, err := data.QueryDataPoints(server, fmt.Sprintf("-%s", time))

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		if dataPoints == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "No data found for server"})
			return
		}

		c.JSON(200, gin.H{
			"data": dataPoints,
			"step": step,
		})
	})
}
