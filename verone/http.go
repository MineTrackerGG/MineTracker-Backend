package verone

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func tokenMatches(header, secret string) bool {
	if len(secret) < 32 {
		return false
	}
	a, b := sha256.Sum256([]byte(header)), sha256.Sum256([]byte("Bearer "+secret))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func Router(s *Service, readToken, adminToken string) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	_ = r.SetTrustedProxies(nil)
	var mu sync.Mutex
	var started time.Time
	var requests int
	r.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(403)
			return
		}
		if c.Request.URL.Path == "/healthz" {
			c.Next()
			return
		}
		header := c.GetHeader("Authorization")
		admin := strings.HasPrefix(c.Request.URL.Path, "/v1/admin/")
		if !(tokenMatches(header, adminToken) || (!admin && tokenMatches(header, readToken))) {
			c.AbortWithStatus(401)
			return
		}
		mu.Lock()
		if time.Since(started) > time.Minute {
			started = time.Now()
			requests = 0
		}
		requests++
		allowed := requests <= 600
		mu.Unlock()
		if !allowed {
			c.Header("Retry-After", "60")
			c.AbortWithStatus(429)
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
		c.Next()
	})
	r.GET("/healthz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()
		if s.Store.Health(ctx) != nil {
			c.Status(503)
			return
		}
		c.Status(204)
	})
	r.PUT("/v1/admin/servers/:id", func(c *gin.Context) {
		var cfg Config
		if err := c.ShouldBindJSON(&cfg); err != nil {
			c.JSON(400, gin.H{"error": "invalid_configuration"})
			return
		}
		if parsed, err := uuid.Parse(cfg.ID); err != nil || parsed.String() != cfg.ID || cfg.ID != c.Param("id") || cfg.Revision < 1 || cfg.Interval < 30 || cfg.Interval > 3600 || !ValidEndpoint(cfg.Java, false) || !ValidEndpoint(cfg.Bedrock, true) || (cfg.Enabled && (cfg.Java == nil && cfg.Bedrock == nil || cfg.Deleted)) {
			c.JSON(400, gin.H{"error": "invalid_configuration"})
			return
		}
		s.mutation.Lock()
		defer s.mutation.Unlock()
		ctx, cancel := dbContext(c.Request.Context())
		defer cancel()
		configs, err := s.Store.Configs(ctx)
		if err != nil {
			c.Status(503)
			return
		}
		exists := false
		for _, old := range configs {
			if old.ID == cfg.ID {
				exists = true
			}
		}
		if !exists && len(configs) >= 1000 {
			c.JSON(409, gin.H{"error": "capacity_reached"})
			return
		}
		if err := s.Store.Put(ctx, cfg); err != nil {
			if errors.Is(err, ErrConflict) {
				c.Status(409)
			} else {
				c.Status(503)
			}
			return
		}
		c.JSON(200, gin.H{"id": cfg.ID, "revision": cfg.Revision})
	})
	r.GET("/v1/servers", func(c *gin.Context) {
		ctx, cancel := dbContext(c.Request.Context())
		defer cancel()
		configs, err := s.Store.Configs(ctx)
		if err != nil {
			c.Status(503)
			return
		}
		snapshots, err := s.Store.Snapshots(ctx)
		if err != nil {
			c.Status(503)
			return
		}
		byID := map[string]Snapshot{}
		for _, p := range snapshots {
			byID[p.ID] = p
		}
		items := []Snapshot{}
		for _, cfg := range configs {
			if !cfg.Deleted {
				items = append(items, Present(cfg, byID[cfg.ID]))
			}
		}
		c.JSON(200, items)
	})
	r.GET("/v1/servers/:id/history", func(c *gin.Context) {
		id, period := c.Param("id"), c.DefaultQuery("period", "24h")
		if _, err := uuid.Parse(id); err != nil || (period != "24h" && period != "7d" && period != "30d") {
			c.Status(400)
			return
		}
		ctx, cancel := dbContext(c.Request.Context())
		defer cancel()
		configs, err := s.Store.Configs(ctx)
		if err != nil {
			c.Status(503)
			return
		}
		found := false
		for _, cfg := range configs {
			if cfg.ID == id && !cfg.Deleted {
				found = true
			}
		}
		if !found {
			c.Status(404)
			return
		}
		points, err := s.Store.History(ctx, id, period)
		if err != nil {
			c.Status(503)
			return
		}
		c.JSON(200, gin.H{"points": points, "period": period, "aggregation": "maximum"})
	})
	return r
}
