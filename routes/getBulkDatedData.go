package routes

import (
	"MineTracker/data"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

type serverResult struct {
	server     string
	dataPoints interface{}
	step       string
	err        error
}

func RegisterGetBulkDatedDataRoute(r *gin.Engine) {
	r.GET("/api/bulk/:servers/:time", func(c *gin.Context) {
		serversParam := c.Param("servers")
		time := c.Param("time")

		servers := strings.Split(serversParam, ",")

		var validServers []string
		for _, s := range servers {
			s = strings.TrimSpace(s)
			if s != "" {
				validServers = append(validServers, s)
			}
		}

		if len(validServers) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "No valid servers provided"})
			return
		}

		resultChan := make(chan serverResult, len(validServers))
		var wg sync.WaitGroup

		for _, server := range validServers {
			wg.Add(1)
			go func(srv string) {
				defer wg.Done()

				dataPoints, step, err := data.QueryDataPoints(srv, fmt.Sprintf("-%s", time))

				resultChan <- serverResult{
					server:     srv,
					dataPoints: dataPoints,
					step:       step,
					err:        err,
				}
			}(server)
		}

		go func() {
			wg.Wait()
			close(resultChan)
		}()

		result := make(map[string]interface{})
		var commonStep string

		for res := range resultChan {
			if res.err != nil {
				result[res.server] = gin.H{"error": res.err.Error()}
				continue
			}

			if res.dataPoints == nil {
				result[res.server] = gin.H{
					"data": []interface{}{},
				}
				continue
			}

			result[res.server] = res.dataPoints
			if commonStep == "" {
				commonStep = res.step
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"data": result,
			"step": commonStep,
		})
	})
}
