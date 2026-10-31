package verone

import (
	"context"
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"
)

func stringPort(p uint16) string { return strconv.Itoa(int(p)) }
func (h *Hosting) view(ctx context.Context) (HostingState, map[string]Config, map[string]Snapshot, error) {
	state, err := h.Repository.Load(ctx)
	if err != nil {
		return state, nil, nil, err
	}
	configs, err := h.Service.Store.Configs(ctx)
	if err != nil {
		return state, nil, nil, err
	}
	configs = append(configs, h.configs(state)...)
	snapshots, err := h.Service.Store.Snapshots(ctx)
	if err != nil {
		return state, nil, nil, err
	}
	cm := map[string]Config{}
	sm := map[string]Snapshot{}
	for _, cfg := range configs {
		cm[cfg.ID] = cfg
	}
	for _, p := range snapshots {
		sm[p.ID] = p
	}
	return state, cm, sm, nil
}
func hostingRoutes(r *gin.Engine, s *Service) {
	r.GET("/v1/hosting/summary", func(c *gin.Context) {
		if s.Hosting == nil {
			c.JSON(200, HostingSummary{State: "unavailable"})
			return
		}
		s.mutation.Lock()
		defer s.mutation.Unlock()
		ctx, cancel := dbContext(c.Request.Context())
		defer cancel()
		state, configs, snapshots, err := s.Hosting.view(ctx)
		if err != nil {
			c.Status(503)
			return
		}
		c.JSON(200, s.Hosting.summary(state, configs, snapshots))
	})
	r.GET("/v1/admin/hosting/servers", func(c *gin.Context) {
		if s.Hosting == nil {
			c.JSON(200, gin.H{"servers": []HostingRow{}, "syncedAt": nil, "state": "unavailable"})
			return
		}
		s.mutation.Lock()
		defer s.mutation.Unlock()
		ctx, cancel := dbContext(c.Request.Context())
		defer cancel()
		state, configs, snapshots, err := s.Hosting.view(ctx)
		if err != nil {
			c.Status(503)
			return
		}
		rows := make([]HostingRow, 0, len(state.Rows))
		for _, row := range state.Rows {
			cfg, ok := effectiveHostingConfig(row, configs)
			if ok {
				p := Present(cfg, snapshots[cfg.ID])
				row.Snapshot = &p
			}
			rows = append(rows, row)
		}
		status := "unavailable"
		if state.SyncedAt != nil {
			status = "stale"
			if s.Hosting.fresh(state) {
				status = "current"
			}
		}
		c.JSON(200, gin.H{"servers": rows, "syncedAt": state.SyncedAt, "state": status})
	})
	r.PUT("/v1/admin/hosting/servers/:id", func(c *gin.Context) {
		if s.Hosting == nil {
			c.Status(503)
			return
		}
		var p Participation
		if c.ShouldBindJSON(&p) != nil {
			c.Status(400)
			return
		}
		ctx, cancel := dbContext(c.Request.Context())
		defer cancel()
		row, err := s.Hosting.Update(ctx, c.Param("id"), p)
		switch {
		case errors.Is(err, ErrInvalidHosting):
			c.Status(400)
		case errors.Is(err, ErrConflict):
			c.Status(409)
		case errors.Is(err, ErrHostingNotFound):
			c.Status(404)
		case err != nil:
			c.Status(503)
		default:
			c.JSON(200, row)
		}
	})
}
