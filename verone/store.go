package verone

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	History(context.Context, string, string) (History, error)
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

func historyQuery(bucket, id, period string, stop time.Time) string {
	duration, step := 24*time.Hour, "5m"
	switch period {
	case "7d":
		duration, step = 7*24*time.Hour, "1h"
	case "30d":
		duration, step = 30*24*time.Hour, "3h"
	}
	base := fmt.Sprintf(`from(bucket: %s) |> range(start: time(v: %s), stop: time(v: %s)) |> filter(fn: (r) => r._measurement == "verone_samples" and r.server_id == %s)`, strconv.Quote(bucket), strconv.Quote(stop.Add(-duration).Format(time.RFC3339Nano)), strconv.Quote(stop.Format(time.RFC3339Nano)), strconv.Quote(id))
	return fmt.Sprintf(`base = %s
p = base |> filter(fn: (r) => r._field == "players") |> aggregateWindow(every: %s, fn: max, createEmpty: false)
s = base |> filter(fn: (r) => r._field == "samples" or r._field == "responding") |> aggregateWindow(every: %s, fn: sum, createEmpty: false)
union(tables: [p,s]) |> group(columns: ["server_id"]) |> pivot(rowKey: ["_time"], columnKey: ["_field"], valueColumn: "_value") |> sort(columns: ["_time"]) |> limit(n: 1000) |> yield(name: "points")
base |> filter(fn: (r) => r._field == "players") |> group(columns: [])
 |> reduce(identity: {total: 0.0, samples: 0, minimum: 0, maximum: 0}, fn: (r, accumulator) => ({
 total: accumulator.total + float(v: r._value), samples: accumulator.samples + 1,
 minimum: if accumulator.samples == 0 or r._value < accumulator.minimum then r._value else accumulator.minimum,
 maximum: if accumulator.samples == 0 or r._value > accumulator.maximum then r._value else accumulator.maximum
 }))
 |> filter(fn: (r) => r.samples > 0)
 |> map(fn: (r) => ({stats: true, mean: r.total / float(v: r.samples), minimum: r.minimum, maximum: r.maximum, samples: r.samples})) |> yield(name: "stats")`, base, step, step)
}

func (s *DatabaseStore) History(ctx context.Context, id, period string) (History, error) {
	history := History{Points: []Point{}}
	query := historyQuery(os.Getenv("INFLUXDB_BUCKET"), id, period, time.Now().UTC())
	res, err := s.Influx.QueryAPI(os.Getenv("INFLUXDB_ORG")).Query(ctx, query)
	if err != nil {
		return history, err
	}
	defer res.Close()
	for res.Next() {
		r := res.Record()
		if stats, _ := r.ValueByKey("stats").(bool); stats {
			mean, mok := r.ValueByKey("mean").(float64)
			min, minok := r.ValueByKey("minimum").(int64)
			max, maxok := r.ValueByKey("maximum").(int64)
			samples, sok := r.ValueByKey("samples").(int64)
			if !mok || !minok || !maxok || !sok || samples < 1 || math.IsNaN(mean) || math.IsInf(mean, 0) {
				return history, errors.New("invalid history statistics")
			}
			history.Stats = HistoryStats{Mean: &mean, Min: &min, Max: &max, Samples: samples}
			continue
		}
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
		history.Points = append(history.Points, p)
	}
	return history, res.Err()
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
