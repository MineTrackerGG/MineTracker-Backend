package verone

import (
	"MineTracker/util"
	"context"
	"errors"
	"net/netip"
	"reflect"
	"regexp"
	"sort"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type HostingQuery struct {
	Java    *Endpoint `json:"java" bson:"java"`
	Bedrock *Endpoint `json:"bedrock" bson:"bedrock"`
}
type Participation struct {
	Revision    int64        `json:"revision" bson:"revision"`
	Role        string       `json:"role" bson:"role"`
	Group       *string      `json:"group" bson:"group"`
	Mode        string       `json:"mode" bson:"mode"`
	Authority   bool         `json:"authority" bson:"authority"`
	Enabled     bool         `json:"enabled" bson:"enabled"`
	EditorialID *string      `json:"editorialId" bson:"editorialId"`
	Query       HostingQuery `json:"query" bson:"query"`
}
type HostingRow struct {
	ID            string `json:"id" bson:"id"`
	MonitorID     string `json:"monitorId" bson:"monitorId"`
	Name          string `json:"name" bson:"name"`
	Participation `bson:",inline"`
	Present       bool         `json:"present" bson:"present"`
	Suspended     bool         `json:"suspended" bson:"suspended"`
	Allocations   []Allocation `json:"allocations" bson:"allocations"`
	Snapshot      *Snapshot    `json:"snapshot" bson:"-"`
}
type HostingState struct {
	Panel    string       `bson:"panel"`
	Rows     []HostingRow `bson:"rows"`
	SyncedAt *time.Time   `bson:"syncedAt"`
}
type HostingRepository interface {
	Load(context.Context) (HostingState, error)
	Save(context.Context, HostingState) error
}
type MongoHostingRepository struct{ DB *mongo.Database }

func (r *MongoHostingRepository) Load(ctx context.Context) (HostingState, error) {
	var state HostingState
	err := r.DB.Collection("verone_hosting").FindOne(ctx, bson.M{"_id": "inventory"}).Decode(&state)
	if errors.Is(err, mongo.ErrNoDocuments) {
		err = nil
	}
	return state, err
}
func (r *MongoHostingRepository) Save(ctx context.Context, state HostingState) error {
	_, err := r.DB.Collection("verone_hosting").UpdateOne(ctx, bson.M{"_id": "inventory"}, bson.M{"$set": state}, options.UpdateOne().SetUpsert(true))
	return err
}

type Hosting struct {
	Service    *Service
	Repository HostingRepository
	Source     InventorySource
	Panel      string
	Interval   time.Duration
}

func (h *Hosting) fresh(state HostingState) bool {
	return (state.Panel == "" || state.Panel == panelIdentity(h.Panel)) && state.SyncedAt != nil && time.Since(*state.SyncedAt) <= 3*h.Interval && time.Until(*state.SyncedAt) < time.Minute
}
func (h *Hosting) Run(ctx context.Context) {
	ticker := time.NewTicker(h.Interval)
	defer ticker.Stop()
	for {
		if err := h.Sync(ctx); err != nil {
			util.Logger.Error().Msg("hosting_inventory_sync_failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (h *Hosting) Sync(ctx context.Context) error {
	fetchCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	inventory, err := h.Source.Fetch(fetchCtx)
	if err != nil {
		return err
	}
	h.Service.mutation.Lock()
	defer h.Service.mutation.Unlock()
	dbctx, stop := dbContext(ctx)
	defer stop()
	state, err := h.Repository.Load(dbctx)
	if err != nil {
		return err
	}
	if state.Panel != "" && state.Panel != panelIdentity(h.Panel) {
		return errors.New("inventory panel mismatch")
	}
	state.Panel = panelIdentity(h.Panel)
	byID := map[string]InventoryServer{}
	for _, item := range inventory {
		if _, ok := byID[item.ID]; ok {
			return errors.New("duplicate inventory UUID")
		}
		byID[item.ID] = item
	}
	for i := range state.Rows {
		row := &state.Rows[i]
		old := *row
		item, ok := byID[row.ID]
		row.Present = ok
		if ok {
			row.Name = item.Name
			row.Suspended = item.Suspended
			row.Allocations = item.Allocations
			delete(byID, row.ID)
		}
		if !reflect.DeepEqual(old, *row) {
			row.Revision++
		}
	}
	for _, item := range byID {
		state.Rows = append(state.Rows, HostingRow{ID: item.ID, MonitorID: uuid.NewSHA1(uuid.NameSpaceURL, []byte(panelIdentity(h.Panel)+":"+item.ID)).String(), Name: item.Name, Present: true, Suspended: item.Suspended, Allocations: item.Allocations, Participation: Participation{Revision: 1, Role: "pending", Enabled: true, Mode: "backends", Query: HostingQuery{}}})
	}
	configs, err := h.Service.Store.Configs(dbctx)
	if err != nil {
		return err
	}
	for _, row := range state.Rows {
		for _, cfg := range configs {
			if cfg.ID == row.MonitorID {
				return errors.New("monitor UUID collision")
			}
		}
	}
	if len(state.Rows)+len(configs) > 1000 {
		return errors.New("hosting capacity reached")
	}
	sort.Slice(state.Rows, func(i, j int) bool { return state.Rows[i].ID < state.Rows[j].ID })
	now := time.Now().UTC()
	state.SyncedAt = &now
	return h.Repository.Save(dbctx, state)
}

func canonicalHostingHost(host string) string {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap().String()
	}
	return host
}
func canonicalHostingQuery(q HostingQuery) HostingQuery {
	for _, e := range []**Endpoint{&q.Java, &q.Bedrock} {
		if *e != nil {
			copy := **e
			copy.Host = canonicalHostingHost(copy.Host)
			*e = &copy
		}
	}
	return q
}

func queryAllowed(row HostingRow, q HostingQuery) bool {
	for _, e := range []*Endpoint{q.Java, q.Bedrock} {
		if e == nil {
			continue
		}
		found := false
		for _, a := range row.Allocations {
			if canonicalHostingHost(e.Host) == canonicalHostingHost(a.Host) && e.Port == a.Port && !e.SRV {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return ValidEndpoint(q.Java, false) && ValidEndpoint(q.Bedrock, true)
}

var hostingGroup = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func validParticipation(p Participation) bool {
	if p.Revision < 1 || p.Mode != "backends" && p.Mode != "proxy" {
		return false
	}
	switch p.Role {
	case "standalone", "backend", "proxy", "excluded", "pending":
	default:
		return false
	}
	if p.Group != nil && !hostingGroup.MatchString(*p.Group) {
		return false
	}
	if (p.Role == "backend" || p.Role == "proxy") && p.Group == nil {
		return false
	}
	if p.Role == "standalone" && p.Group != nil {
		return false
	}
	if p.Authority && (p.Role != "proxy" || p.Mode != "proxy") {
		return false
	}
	if p.EditorialID != nil {
		u, e := uuid.Parse(*p.EditorialID)
		if e != nil || u.String() != *p.EditorialID {
			return false
		}
	}
	return !p.Enabled || p.Role == "excluded" || p.Role == "pending" || p.Query.Java != nil || p.Query.Bedrock != nil
}
func (h *Hosting) Update(ctx context.Context, id string, p Participation) (HostingRow, error) {
	p.Query = canonicalHostingQuery(p.Query)
	if !validParticipation(p) {
		return HostingRow{}, ErrInvalidHosting
	}
	h.Service.mutation.Lock()
	defer h.Service.mutation.Unlock()
	state, err := h.Repository.Load(ctx)
	if err != nil {
		return HostingRow{}, err
	}
	for i, row := range state.Rows {
		if row.ID != id {
			continue
		}
		if p.Revision == row.Revision && reflect.DeepEqual(row.Participation, p) {
			return row, nil
		}
		if p.Revision <= row.Revision {
			return row, ErrConflict
		}
		if !queryAllowed(row, p.Query) {
			return row, ErrInvalidHosting
		}
		if p.EditorialID != nil {
			configs, err := h.Service.Store.Configs(ctx)
			if err != nil {
				return row, err
			}
			found := false
			for _, cfg := range configs {
				if cfg.ID == *p.EditorialID && !cfg.Deleted && reflect.DeepEqual(p.Query, canonicalHostingQuery(HostingQuery{Java: cfg.Java, Bedrock: cfg.Bedrock})) {
					found = true
				}
			}
			if !found {
				return row, ErrInvalidHosting
			}
			for _, other := range state.Rows {
				if other.ID != id && other.EditorialID != nil && *other.EditorialID == *p.EditorialID {
					return row, ErrConflict
				}
			}
		}
		row.Participation = p
		state.Rows[i] = row
		if err := h.Repository.Save(ctx, state); err != nil {
			return HostingRow{}, err
		}
		return row, nil
	}
	return HostingRow{}, ErrHostingNotFound
}

var ErrInvalidHosting = errors.New("invalid hosting participation")
var ErrHostingNotFound = errors.New("hosting server not found")

func (h *Hosting) configs(state HostingState) []Config {
	configs := []Config{}
	for _, row := range state.Rows {
		row.Query = canonicalHostingQuery(row.Query)
		if row.EditorialID != nil {
			continue
		}
		configs = append(configs, Config{ID: row.MonitorID, Revision: row.Revision, Enabled: h.fresh(state) && row.Present && !row.Suspended && row.Enabled && row.Role != "pending" && row.Role != "excluded" && queryAllowed(row, row.Query), Interval: 60, Java: row.Query.Java, Bedrock: row.Query.Bedrock})
	}
	return configs
}
