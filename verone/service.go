package verone

import (
	"MineTracker/util"
	"context"
	"math"
	"sort"
	"sync"
	"time"
)

type Service struct {
	Store     Store
	mutation  sync.Mutex
	mu        sync.Mutex
	last      map[string]time.Time
	active    map[string]int64
	revisions map[string]int64
	Probe     func(context.Context, Endpoint, bool) Reading
}

func New(store Store) *Service {
	return &Service{Store: store, last: map[string]time.Time{}, active: map[string]int64{}, revisions: map[string]int64{}, Probe: Probe}
}

func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		s.schedule(ctx, &wg)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) schedule(ctx context.Context, wg *sync.WaitGroup) {
	query, cancel := dbContext(ctx)
	configs, err := s.Store.Configs(query)
	cancel()
	if err != nil {
		util.Logger.Error().Msg("monitor_config_read_failed")
		return
	}
	s.mu.Lock()
	sort.SliceStable(configs, func(i, j int) bool { return s.last[configs[i].ID].Before(s.last[configs[j].ID]) })
	s.mu.Unlock()
	for _, cfg := range configs {
		if !cfg.Enabled || cfg.Deleted {
			continue
		}
		s.mu.Lock()
		_, busy := s.active[cfg.ID]
		if busy || len(s.active) >= 8 || (s.revisions[cfg.ID] == cfg.Revision && time.Since(s.last[cfg.ID]) < time.Duration(cfg.Interval)*time.Second) {
			s.mu.Unlock()
			continue
		}
		s.active[cfg.ID] = cfg.Revision
		s.last[cfg.ID] = time.Now()
		s.revisions[cfg.ID] = cfg.Revision
		s.mu.Unlock()
		wg.Add(1)
		go func(c Config) {
			defer wg.Done()
			defer func() { s.mu.Lock(); delete(s.active, c.ID); s.mu.Unlock() }()
			s.collect(ctx, c)
		}(cfg)
	}
}

func (s *Service) collect(ctx context.Context, c Config) {
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	p := Snapshot{ID: c.ID, Revision: c.Revision, Status: "unknown"}
	if c.Java != nil {
		r := s.Probe(probeCtx, *c.Java, false)
		p.Java = &r
	}
	if c.Bedrock != nil {
		r := s.Probe(probeCtx, *c.Bedrock, true)
		p.Bedrock = &r
	}
	for _, r := range []*Reading{p.Java, p.Bedrock} {
		if r == nil {
			continue
		}
		if r.Status == "no_response" {
			p.Status = "no_response"
		}
		if r.Status == "online" {
			p.Status = r.Status
			p.Players = r.Players
			p.MaxPlayers = r.MaxPlayers
			p.LatencyMS = r.LatencyMS
			break
		}
	}
	p.UpdatedAt = time.Now().UTC()
	s.mutation.Lock()
	defer s.mutation.Unlock()
	dbctx, stop := dbContext(ctx)
	defer stop()
	configs, err := s.Store.Configs(dbctx)
	if err != nil {
		return
	}
	current := false
	for _, cfg := range configs {
		if cfg.ID == c.ID && cfg.Revision == c.Revision && cfg.Enabled && !cfg.Deleted {
			current = true
			break
		}
	}
	if !current {
		return
	}
	previous, err := s.Store.Snapshots(dbctx)
	if err != nil {
		return
	}
	for _, old := range previous {
		if old.ID != c.ID {
			continue
		}
		p.Peak = old.Peak
		p.Popularity = old.Popularity
		if p.Players != nil && old.Popularity != nil {
			weight := 1 - math.Exp(-p.UpdatedAt.Sub(old.UpdatedAt).Seconds()/86400)
			v := *old.Popularity + (float64(*p.Players)-*old.Popularity)*weight
			p.Popularity = &v
		}
	}
	if p.Players != nil {
		if p.Peak == nil || *p.Players > *p.Peak {
			v := *p.Players
			p.Peak = &v
		}
		if p.Popularity == nil {
			v := float64(*p.Players)
			p.Popularity = &v
		}
	}
	if err := s.Store.Record(dbctx, p); err != nil {
		util.Logger.Error().Str("server_id", c.ID).Msg("monitor_record_failed")
	}
}

func Present(cfg Config, p Snapshot) Snapshot {
	if !cfg.Enabled || cfg.Deleted {
		return Snapshot{ID: cfg.ID, Revision: cfg.Revision, Status: "disabled", UpdatedAt: time.Now().UTC()}
	}
	if p.Revision != cfg.Revision || p.UpdatedAt.IsZero() {
		return Snapshot{ID: cfg.ID, Revision: cfg.Revision, Status: "unknown", UpdatedAt: time.Now().UTC()}
	}
	if time.Since(p.UpdatedAt) > time.Duration(cfg.Interval*3)*time.Second {
		p.Status = "stale"
		p.Players = nil
		p.MaxPlayers = nil
		p.LatencyMS = nil
		p.Java = nil
		p.Bedrock = nil
	}
	return p
}
