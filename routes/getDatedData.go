package routes

import (
	"MineTracker/data"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type cacheEntry struct {
	data      interface{}
	step      string
	timestamp time.Time
}

var (
	cache      = make(map[string]cacheEntry)
	cacheMutex sync.RWMutex
	cacheTTL   = 30 * time.Second
)

func RegisterGetDatedDataRoute(r *gin.Engine) {
	r.GET("/api/:server/:time", func(c *gin.Context) {
		server := c.Param("server")
		timeParam := c.Param("time")

		cacheKey := fmt.Sprintf("%s:%s", server, timeParam)

		cacheMutex.RLock()
		if entry, found := cache[cacheKey]; found && time.Since(entry.timestamp) < cacheTTL {
			cacheMutex.RUnlock()
			c.JSON(http.StatusOK, gin.H{
				"data": entry.data,
				"step": entry.step,
			})
			return
		}
		cacheMutex.RUnlock()

		dataPoints, step, err := data.QueryDataPoints(server, fmt.Sprintf("-%s", timeParam))

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		if dataPoints == nil {
			c.JSON(http.StatusOK, gin.H{
				"data": []interface{}{},
				"step": step,
			})
			return
		}

		cacheMutex.Lock()
		cache[cacheKey] = cacheEntry{
			data:      dataPoints,
			step:      step,
			timestamp: time.Now(),
		}
		cacheMutex.Unlock()

		c.JSON(http.StatusOK, gin.H{
			"data": dataPoints,
			"step": step,
		})
	})
}
