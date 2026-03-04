package routes

import (
	"MineTracker/util"
	"net/http"

	"github.com/gin-gonic/gin"
)

func RegisterGetVersionRoute(r *gin.Engine) {
	r.GET("/api/version", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"version": util.CurrentVersion(),
		})
	})
}
