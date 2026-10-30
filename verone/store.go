package verone

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var ErrConflict = errors.New("revision conflict")

type Store interface {
	Configs(context.Context) ([]Config, error)
	Put(context.Context, Config) error
	Snapshots(context.Context) ([]Snapshot, error)
	Record(context.Context, Snapshot) error
	History(context.Context, string, string) ([]Point, error)
	Health(context.Context) error
}

type DatabaseStore struct {
	Mongo  *mongo.Database
	Influx influxdb2.Client
}

func (s *DatabaseStore) Configs(ctx context.Context) ([]Config, error) {
	cur, err := s.Mongo.Collection("verone_configs").Find(ctx, bson.M{}, options.Find().SetLimit(1001))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	items := []Config{}
	err = cur.All(ctx, &items)
	return items, err
}

func (s *DatabaseStore) Put(ctx context.Context, cfg Config) error {
	col := s.Mongo.Collection("verone_configs")
	// _id's unique index arbitrates concurrent upserts and rejects stale revisions.
	_, err := col.UpdateOne(ctx, bson.M{"_id": cfg.ID, "revision": bson.M{"$lt": cfg.Revision}}, bson.M{"$set": cfg}, options.UpdateOne().SetUpsert(true))
	if mongo.IsDuplicateKeyError(err) {
		var existing Config
		if e := col.FindOne(ctx, bson.M{"_id": cfg.ID}).Decode(&existing); e != nil {
			return e
		}
		if reflect.DeepEqual(existing, cfg) {
			return nil
		}
		return ErrConflict
	}
	return err
}

func (s *DatabaseStore) Snapshots(ctx context.Context) ([]Snapshot, error) {
	cur, err := s.Mongo.Collection("verone_snapshots").Find(ctx, bson.M{}, options.Find().SetLimit(1001))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	items := []Snapshot{}
	err = cur.All(ctx, &items)
	return items, err
}

func (s *DatabaseStore) Record(ctx context.Context, p Snapshot) error {
	_, err := s.Mongo.Collection("verone_snapshots").ReplaceOne(ctx, bson.M{"_id": p.ID}, p, options.Replace().SetUpsert(true))
	if err != nil {
		return err
	}
	fields := map[string]interface{}{"samples": int64(1), "responding": int64(0)}
	if p.Players != nil {
		fields["players"] = int64(*p.Players)
		fields["responding"] = int64(1)
	}
	point := influxdb2.NewPoint("verone_samples", map[string]string{"server_id": p.ID}, fields, p.UpdatedAt)
	return s.Influx.WriteAPIBlocking(os.Getenv("INFLUXDB_ORG"), os.Getenv("INFLUXDB_BUCKET")).WritePoint(ctx, point)
}

func (s *DatabaseStore) History(ctx context.Context, id, period string) ([]Point, error) {
	duration, step := "-24h", "5m"
	switch period {
	case "7d":
		duration, step = "-7d", "1h"
	case "30d":
		duration, step = "-30d", "3h"
	}
	base := fmt.Sprintf(`from(bucket: %s) |> range(start: %s) |> filter(fn: (r) => r._measurement == "verone_samples" and r.server_id == %s)`, strconv.Quote(os.Getenv("INFLUXDB_BUCKET")), duration, strconv.Quote(id))
	query := fmt.Sprintf(`base = %s
p = base |> filter(fn: (r) => r._field == "players") |> aggregateWindow(every: %s, fn: max, createEmpty: false)
s = base |> filter(fn: (r) => r._field == "samples" or r._field == "responding") |> aggregateWindow(every: %s, fn: sum, createEmpty: false)
union(tables: [p,s]) |> group(columns: ["server_id"]) |> pivot(rowKey: ["_time"], columnKey: ["_field"], valueColumn: "_value") |> sort(columns: ["_time"]) |> limit(n: 1000)`, base, step, step)
	res, err := s.Influx.QueryAPI(os.Getenv("INFLUXDB_ORG")).Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer res.Close()
	points := []Point{}
	for res.Next() {
		r := res.Record()
		p := Point{Timestamp: r.Time().Unix()}
		if v, ok := r.ValueByKey("players").(int64); ok {
			n := int(v)
			p.Players = &n
		}
		if v, ok := r.ValueByKey("samples").(int64); ok {
			p.Samples = v
		}
		if v, ok := r.ValueByKey("responding").(int64); ok {
			p.Responding = v
		}
		points = append(points, p)
	}
	return points, res.Err()
}

func (s *DatabaseStore) Health(ctx context.Context) error {
	if err := s.Mongo.Client().Ping(ctx, nil); err != nil {
		return err
	}
	h, err := s.Influx.Health(ctx)
	if err != nil {
		return err
	}
	if h.Status != "pass" {
		return errors.New("influx unavailable")
	}
	return nil
}

func dbContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 10*time.Second)
}
