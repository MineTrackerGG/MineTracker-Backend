package verone

import (
	"context"
	"github.com/google/uuid"
	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These checks require explicitly isolated databases; they never use the deployed database name.
func TestIsolatedDatabases(t *testing.T) {
	uri := os.Getenv("VERONE_TEST_MONGO_URI")
	url := os.Getenv("VERONE_TEST_INFLUX_URL")
	if uri == "" || url == "" {
		t.Skip("isolated database URLs not supplied")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	db := client.Database("verone_minecraft_test")
	defer db.Drop(ctx)
	influx := influxdb2.NewClient(url, os.Getenv("INFLUXDB_TOKEN"))
	defer influx.Close()
	store := &DatabaseStore{Mongo: db, Influx: influx}
	if err := store.Health(ctx); err != nil {
		t.Fatal(err)
	}
	c := config()
	c.ID = uuid.NewString()
	if err := store.Put(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, c); err != nil {
		t.Fatalf("idempotence: %v", err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			next := c
			next.Revision = 2
			next.Interval = 30 + i
			err := store.Put(ctx, next)
			if err == nil {
				wins.Add(1)
			} else if err != ErrConflict {
				t.Errorf("put: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("concurrent winners=%d", wins.Load())
	}
	configs, err := store.Configs(ctx)
	if err != nil || len(configs) != 1 || configs[0].Revision != 2 {
		t.Fatalf("configs: %v %v", configs, err)
	}
	n := 17
	now := time.Now().UTC().Add(-time.Minute)
	if err := store.Record(ctx, Snapshot{ID: c.ID, Revision: 2, Status: "online", Players: &n, Peak: &n, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(ctx, Snapshot{ID: c.ID, Revision: 2, Status: "no_response", UpdatedAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	points, err := store.History(ctx, c.ID, "24h")
	if err != nil {
		t.Fatal(err)
	}
	var samples, responding int64
	found := false
	for _, p := range points {
		samples += p.Samples
		responding += p.Responding
		if p.Players != nil && *p.Players == 17 {
			found = true
		}
	}
	if samples != 2 || responding != 1 || !found {
		t.Fatalf("history=%+v", points)
	}
	c.Revision = 3
	c.Enabled = false
	if err := store.Put(ctx, c); err != nil {
		t.Fatal(err)
	}
	points, err = store.History(ctx, c.ID, "24h")
	if err != nil || len(points) == 0 {
		t.Fatalf("history lost on disable: %v %v", points, err)
	}
}

func TestLivePublicProbes(t *testing.T) {
	if os.Getenv("VERONE_TEST_LIVE") != "1" {
		t.Skip("live probes not enabled")
	}
	for _, tc := range []struct {
		name string
		e    Endpoint
		b    bool
	}{
		{"java", Endpoint{Host: "mc.hypixel.net", Port: 25565, SRV: true}, false},
		{"bedrock", Endpoint{Host: "geo.hivebedrock.network", Port: 19132}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Probe(context.Background(), tc.e, tc.b)
			t.Logf("%+v", r)
			if r.Status != "online" {
				t.Fatalf("public probe did not respond: %s", r.Status)
			}
			t.Logf("real players=%d max=%v latencyMs=%d", *r.Players, r.MaxPlayers, *r.LatencyMS)
		})
	}
}
