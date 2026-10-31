package verone

import (
	"context"
	"reflect"
	"time"
)

type HostingCoverage struct {
	Eligible   int `json:"eligible"`
	Responding int `json:"responding"`
	Pending    int `json:"pending"`
}
type HostingSummary struct {
	Players   *int            `json:"players"`
	State     string          `json:"state"`
	UpdatedAt *time.Time      `json:"updatedAt"`
	Coverage  HostingCoverage `json:"coverage"`
}

func effectiveHostingConfig(row HostingRow, configs map[string]Config) (Config, bool) {
	id := row.MonitorID
	if row.EditorialID != nil {
		id = *row.EditorialID
	}
	cfg, ok := configs[id]
	return cfg, ok && cfg.Enabled && !cfg.Deleted && queryAllowed(row, row.Query) && reflect.DeepEqual(canonicalHostingQuery(row.Query), canonicalHostingQuery(HostingQuery{Java: cfg.Java, Bedrock: cfg.Bedrock}))
}
func (h *Hosting) summary(state HostingState, configs map[string]Config, snapshots map[string]Snapshot) HostingSummary {
	out := HostingSummary{State: "unavailable"}
	if !h.fresh(state) {
		return out
	}
	selected := []HostingRow{}
	groups := map[string][]HostingRow{}
	for _, row := range state.Rows {
		if !row.Present || !row.Enabled || row.Role == "excluded" {
			continue
		}
		switch row.Role {
		case "pending":
			out.Coverage.Pending++
		case "standalone":
			selected = append(selected, row)
		default:
			if row.Group == nil {
				out.Coverage.Pending++
			} else {
				groups[*row.Group] = append(groups[*row.Group], row)
			}
		}
	}
	for _, rows := range groups {
		mode := rows[0].Mode
		valid := true
		chosen := []HostingRow{}
		for _, row := range rows {
			if row.Mode != mode {
				valid = false
			}
			if mode == "backends" && row.Role == "backend" || mode == "proxy" && row.Role == "proxy" && row.Authority {
				chosen = append(chosen, row)
			}
		}
		if len(chosen) == 0 || mode == "proxy" && len(chosen) != 1 {
			valid = false
		}
		if !valid {
			out.Coverage.Pending++
			continue
		}
		selected = append(selected, chosen...)
	}
	// Reject all overlapping sources rather than making map iteration choose a winner.
	duplicates := map[string]int{}
	for _, row := range selected {
		key := row.MonitorID
		if row.EditorialID != nil {
			key = *row.EditorialID
		}
		duplicates["id:"+key]++
		endpoints := map[string]bool{}
		for _, e := range []*Endpoint{row.Query.Java, row.Query.Bedrock} {
			if e != nil {
				endpoints["endpoint:"+canonicalHostingHost(e.Host)+":"+stringPort(e.Port)] = true
			}
		}
		for endpoint := range endpoints {
			duplicates[endpoint]++
		}
	}
	total := 0
	for _, row := range selected {
		out.Coverage.Eligible++
		key := row.MonitorID
		if row.EditorialID != nil {
			key = *row.EditorialID
		}
		duplicate := duplicates["id:"+key] > 1
		for _, e := range []*Endpoint{row.Query.Java, row.Query.Bedrock} {
			if e != nil && duplicates["endpoint:"+canonicalHostingHost(e.Host)+":"+stringPort(e.Port)] > 1 {
				duplicate = true
			}
		}
		cfg, valid := effectiveHostingConfig(row, configs)
		p := snapshots[key]
		if row.Suspended || duplicate || !valid || p.Revision != cfg.Revision || p.Status != "online" || p.Players == nil || *p.Players < 0 || time.Since(p.UpdatedAt) > 180*time.Second || time.Since(p.UpdatedAt) > time.Duration(cfg.Interval*3)*time.Second || time.Until(p.UpdatedAt) > time.Minute {
			continue
		}
		total += *p.Players
		out.Coverage.Responding++
		if out.UpdatedAt == nil || p.UpdatedAt.Before(*out.UpdatedAt) {
			v := p.UpdatedAt
			out.UpdatedAt = &v
		}
	}
	if out.Coverage.Responding > 0 {
		out.Players = &total
		out.State = "partial"
		if out.Coverage.Responding == out.Coverage.Eligible && out.Coverage.Pending == 0 {
			out.State = "complete"
		}
	}
	return out
}
func (s *Service) configs(ctx context.Context) ([]Config, error) {
	configs, err := s.Store.Configs(ctx)
	if err != nil || s.Hosting == nil {
		return configs, err
	}
	state, err := s.Hosting.Repository.Load(ctx)
	if err != nil {
		return configs, nil
	}
	return append(configs, s.Hosting.configs(state)...), nil
}
