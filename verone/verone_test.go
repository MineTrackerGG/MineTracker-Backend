package verone

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"
)

type fakeDNS struct {
	records []*net.SRV
	ips     map[string][]netip.Addr
}

func (d fakeDNS) LookupSRV(context.Context, string, string, string) (string, []*net.SRV, error) {
	return "", d.records, nil
}
func (d fakeDNS) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return d.ips[host], nil
}
func TestResolveRejectsAnyUnsafeAnswer(t *testing.T) {
	for _, unsafe := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::1", "::ffff:192.168.1.1", "2002:a00:1::", "3fff::1"} {
		t.Run(unsafe, func(t *testing.T) {
			dns := fakeDNS{ips: map[string][]netip.Addr{"example.com": {netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr(unsafe)}}}
			if _, _, err := resolveWith(context.Background(), dns, Endpoint{Host: "example.com", Port: 25565}, false); err == nil {
				t.Fatal("accepted unsafe DNS answer")
			}
		})
	}
	dns := fakeDNS{records: []*net.SRV{{Target: "safe.example.", Port: 25565}, {Target: "unsafe.example.", Port: 25565}}, ips: map[string][]netip.Addr{"safe.example": {netip.MustParseAddr("8.8.8.8")}, "unsafe.example": {netip.MustParseAddr("10.0.0.1")}}}
	if _, _, err := resolveWith(context.Background(), dns, Endpoint{Host: "example.com", Port: 25565, SRV: true}, false); err == nil {
		t.Fatal("accepted unsafe secondary SRV")
	}
	dns.records = dns.records[:1]
	ip, port, err := resolveWith(context.Background(), dns, Endpoint{Host: "example.com", Port: 25565, SRV: true}, false)
	if err != nil || ip.String() != "8.8.8.8" || port != 25565 {
		t.Fatalf("resolution: %v %v %v", ip, port, err)
	}
}

func TestBedrockPong(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			request := make([]byte, 33)
			io.ReadFull(server, request)
			ad := []byte("MCPE;Test;1;1.0;12;100;")
			reply := make([]byte, 35+len(ad))
			reply[0] = 0x1c
			copy(reply[1:9], request[1:9])
			copy(reply[17:33], raknetMagic)
			binary.BigEndian.PutUint16(reply[33:35], uint16(len(ad)))
			copy(reply[35:], ad)
			if invalid {
				reply[1] ^= 1
			}
			server.Write(reply)
		}()
		client.SetDeadline(time.Now().Add(time.Second))
		players, max, _, err := pingBedrock(client)
		client.Close()
		if invalid && err == nil {
			t.Fatal("accepted forged pong")
		}
		if !invalid && (err != nil || players != 12 || max == nil || *max != 100) {
			t.Fatalf("pong: %d %v %v", players, max, err)
		}
	}
}

type memoryStore struct {
	configs   []Config
	snapshots []Snapshot
	records   int
}

func (m *memoryStore) Configs(context.Context) ([]Config, error) {
	return append([]Config(nil), m.configs...), nil
}
func (m *memoryStore) Put(_ context.Context, c Config) error {
	for i, old := range m.configs {
		if old.ID == c.ID {
			if old.Revision >= c.Revision {
				return ErrConflict
			}
			m.configs[i] = c
			return nil
		}
	}
	m.configs = append(m.configs, c)
	return nil
}
func (m *memoryStore) Snapshots(context.Context) ([]Snapshot, error) { return m.snapshots, nil }
func (m *memoryStore) Record(_ context.Context, p Snapshot) error {
	m.records++
	m.snapshots = []Snapshot{p}
	return nil
}
func (m *memoryStore) History(context.Context, string, string) (History, error) {
	return History{Points: []Point{}}, nil
}
func (m *memoryStore) Health(context.Context) error { return nil }

const testID = "ed3f4824-d289-43d7-aad3-450ce297da1e"
const readSecret = "read-token-012345678901234567890123456789"
const adminSecret = "admin-token-012345678901234567890123456789"

func config() Config {
	return Config{ID: testID, Revision: 1, Enabled: true, Interval: 30, Java: &Endpoint{Host: "example.com", Port: 25565}}
}
func request(r http.Handler, method, path, token string, body []byte) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}
func TestHTTPAuthorizationAndValidation(t *testing.T) {
	s := New(&memoryStore{})
	r := Router(s, readSecret, adminSecret)
	body, _ := json.Marshal(config())
	for _, tc := range []struct {
		method, path, token string
		body                []byte
		status              int
	}{
		{"PUT", "/v1/admin/servers/" + testID, readSecret, body, 401},
		{"GET", "/v1/servers", "", nil, 401},
		{"OPTIONS", "/v1/servers", readSecret, nil, 403},
		{"PUT", "/v1/admin/servers/" + testID, adminSecret, body, 200},
		{"PUT", "/v1/admin/servers/" + testID, adminSecret, body, 409},
		{"GET", "/v1/servers/" + testID + "/history?period=99d", readSecret, nil, 400},
		{"GET", "/debug/pprof/", adminSecret, nil, 404},
		{"GET", "/v1/servers", readSecret, nil, 200},
	} {
		w := request(r, tc.method, tc.path, tc.token, tc.body)
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
	}
}
func TestDisableDuringProbeDiscardsSample(t *testing.T) {
	c := config()
	store := &memoryStore{configs: []Config{c}}
	s := New(store)
	entered, release := make(chan struct{}), make(chan struct{})
	s.Probe = func(context.Context, Endpoint, bool) Reading {
		close(entered)
		<-release
		n := 12
		return Reading{Status: "online", Players: &n}
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); s.collect(context.Background(), c) }()
	<-entered
	updated := c
	updated.Revision = 2
	updated.Enabled = false
	body, _ := json.Marshal(updated)
	if w := request(Router(s, readSecret, adminSecret), "PUT", "/v1/admin/servers/"+testID, adminSecret, body); w.Code != 200 {
		t.Fatal(w.Code)
	}
	close(release)
	wg.Wait()
	if store.records != 0 {
		t.Fatal("recorded disabled revision")
	}
}
func TestPeakAndStale(t *testing.T) {
	n := 15
	c := config()
	store := &memoryStore{configs: []Config{c}}
	s := New(store)
	s.Probe = func(context.Context, Endpoint, bool) Reading { return Reading{Status: "online", Players: &n} }
	s.collect(context.Background(), c)
	n = 3
	s.collect(context.Background(), c)
	p := store.snapshots[0]
	if p.Peak == nil || *p.Peak != 15 || p.Players == nil || *p.Players != 3 {
		t.Fatalf("peak lost: %+v", p)
	}
	p.UpdatedAt = time.Now().Add(-5 * time.Minute)
	p = Present(c, p)
	if p.Status != "stale" || p.Players != nil || p.Java != nil {
		t.Fatal("stale numbers leaked")
	}
}

func TestSchedulerFairnessAndConcurrencyLimit(t *testing.T) {
	store := &memoryStore{}
	for i := 0; i < 20; i++ {
		c := config()
		c.ID = string(rune('a' + i))
		store.configs = append(store.configs, c)
	}
	s := New(store)
	release := make(chan struct{})
	s.Probe = func(context.Context, Endpoint, bool) Reading { <-release; return Reading{Status: "unknown"} }
	// Previously sampled servers must not starve servers that have never run.
	for i := 0; i < 8; i++ {
		s.last[store.configs[i].ID] = time.Now().Add(-time.Minute)
		s.revisions[store.configs[i].ID] = 1
	}
	var wg sync.WaitGroup
	s.schedule(context.Background(), &wg)
	s.mu.Lock()
	if len(s.active) != 8 {
		t.Errorf("active=%d", len(s.active))
	}
	for i := 0; i < 8; i++ {
		if _, ok := s.active[store.configs[i].ID]; ok {
			t.Error("rescheduled old sample before untouched servers")
		}
	}
	s.mu.Unlock()
	close(release)
	wg.Wait()
}

func TestConcurrentCapacityAdmission(t *testing.T) {
	store := &memoryStore{}
	for i := 0; i < 999; i++ {
		c := config()
		c.ID = string(rune(i))
		store.configs = append(store.configs, c)
	}
	r := Router(New(store), readSecret, adminSecret)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, id := range []string{testID, "4fc3df5c-4e26-4655-8faa-bc79e5c861cb"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			c := config()
			c.ID = id
			b, _ := json.Marshal(c)
			codes <- request(r, "PUT", "/v1/admin/servers/"+id, adminSecret, b).Code
		}(id)
	}
	wg.Wait()
	close(codes)
	accepted, rejected := 0, 0
	for code := range codes {
		if code == 200 {
			accepted++
		}
		if code == 409 {
			rejected++
		}
	}
	if accepted != 1 || rejected != 1 || len(store.configs) != 1000 {
		t.Fatalf("capacity: accepted=%d rejected=%d configs=%d", accepted, rejected, len(store.configs))
	}
}
