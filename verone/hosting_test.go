package verone

import (
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"io"
	"net/http"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
)

type hostingMemory struct{ state HostingState }

func (m *hostingMemory) Load(context.Context) (HostingState, error) {
	b, _ := bson.Marshal(m.state)
	var state HostingState
	bson.Unmarshal(b, &state)
	return state, nil
}
func (m *hostingMemory) Save(_ context.Context, state HostingState) error {
	m.state = state
	return nil
}

type inventoryFake struct{ rows []InventoryServer }

func (f *inventoryFake) Fetch(context.Context) ([]InventoryServer, error) { return f.rows, nil }

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixtureInventory(id string) map[string]any {
	return map[string]any{"attributes": map[string]any{"uuid": id, "name": "private name", "status": nil, "relationships": map[string]any{"allocations": map[string]any{"data": []any{map[string]any{"attributes": map[string]any{"ip": "8.8.8.8", "port": 25565}}, map[string]any{"attributes": map[string]any{"ip": "10.0.0.1", "port": 25566}}}}}}}
}
func TestHostingPaginationAndPanelRebinding(t *testing.T) {
	panel, err := newPterodactyl("https://panel.example", "secret", fakeDNS{ips: map[string][]netip.Addr{"panel.example": {netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}}})
	if err != nil {
		t.Fatal(err)
	}
	transport := panel.client.Transport.(*http.Transport)
	if conn, err := transport.DialContext(context.Background(), "tcp", "panel.example:443"); err == nil {
		conn.Close()
		t.Fatal("panel rebinding accepted")
	}
	calls := 0
	panel.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer secret" || r.URL.Path != "/api/application/servers" || r.URL.Query().Get("include") != "allocations,egg" {
			t.Fatal("unexpected inventory request")
		}
		id := testID
		if calls == 2 {
			id = "4fc3df5c-4e26-4655-8faa-bc79e5c861cb"
		}
		body, _ := json.Marshal(map[string]any{"data": []any{fixtureInventory(id)}, "meta": map[string]any{"pagination": map[string]any{"current_page": calls, "total_pages": 2}}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: http.Header{}}, nil
	})
	rows, err := panel.Fetch(context.Background())
	if err != nil || len(rows) != 2 || calls != 2 || len(rows[0].Allocations) != 1 {
		t.Fatalf("pagination: %+v %v calls%d", rows, err, calls)
	}
	if panel.client.CheckRedirect(nil, nil) == nil {
		t.Fatal("redirect accepted")
	}
	if _, err := NewPterodactyl("http://panel.example", "secret"); err == nil {
		t.Fatal("HTTP panel accepted")
	}
	calls = 0
	panel.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[],"meta":{"pagination":{"current_page":1,"total_pages":99}}}`))}, nil
	})
	if _, err := panel.Fetch(context.Background()); err == nil || calls != 1 {
		t.Fatal("unbounded pagination accepted")
	}
}
func hostingFixture() (*Hosting, *hostingMemory, *memoryStore) {
	now := time.Now().UTC()
	repo := &hostingMemory{state: HostingState{SyncedAt: &now}}
	store := &memoryStore{}
	s := New(store)
	h := &Hosting{Service: s, Repository: repo, Panel: "https://panel.example", Interval: time.Minute}
	s.Hosting = h
	return h, repo, store
}
func hostingRow(id, role, group, mode string, n int) (HostingRow, Snapshot) {
	var g *string
	if group != "" {
		g = &group
	}
	endpoint := &Endpoint{Host: "8.8.8." + stringPort(uint16(n+1)), Port: 25565}
	row := HostingRow{ID: id, MonitorID: id, Present: true, Allocations: []Allocation{{Host: endpoint.Host, Port: endpoint.Port}}, Participation: Participation{Revision: 1, Role: role, Group: g, Mode: mode, Enabled: true, Query: HostingQuery{Java: endpoint}}}
	v := n
	return row, Snapshot{ID: id, Revision: 1, Status: "online", Players: &v, UpdatedAt: time.Now().UTC()}
}
func TestHostingProxyDedupFreshnessSuspension(t *testing.T) {
	h, repo, _ := hostingFixture()
	proxy, p := hostingRow("proxy", "proxy", "network", "proxy", 10)
	proxy.Authority = true
	a, pa := hostingRow("a", "backend", "network", "proxy", 4)
	b, pb := hostingRow("b", "backend", "network", "proxy", 6)
	repo.state.Rows = []HostingRow{proxy, a, b}
	configs := map[string]Config{}
	for _, c := range h.configs(repo.state) {
		configs[c.ID] = c
	}
	snap := map[string]Snapshot{p.ID: p, pa.ID: pa, pb.ID: pb}
	out := h.summary(repo.state, configs, snap)
	if out.Players == nil || *out.Players != 10 || out.Coverage.Eligible != 2 || out.State != "complete" {
		t.Fatalf("backend-only %+v", out)
	}
	repo.state.Rows[1].Suspended = true
	out = h.summary(repo.state, configs, snap)
	if out.Players == nil || *out.Players != 6 || out.State != "partial" {
		t.Fatal("suspended backend counted")
	}
	pb.UpdatedAt = time.Now().Add(-181 * time.Second)
	snap[pb.ID] = pb
	out = h.summary(repo.state, configs, snap)
	if out.Players != nil {
		t.Fatal("stale backend counted")
	}
	old := time.Now().Add(-4 * time.Minute)
	repo.state.SyncedAt = &old
	out = h.summary(repo.state, configs, snap)
	if out.Players != nil {
		t.Fatal("stale inventory counted")
	}
}

func TestHostingSyncIdentityRemovalAndEditorialGuard(t *testing.T) {
	h, repo, store := hostingFixture()
	source := &inventoryFake{rows: []InventoryServer{{ID: testID, Name: "private", Allocations: []Allocation{{Host: "8.8.8.8", Port: 25565}}}}}
	h.Source = source
	if err := h.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	row := repo.state.Rows[0]
	id := row.MonitorID
	if row.Role != "standalone" || row.Revision != 1 {
		t.Fatal("auto inventory not configured")
	}
	row.Role = "standalone"
	row.Query.Java = &Endpoint{Host: "8.8.8.8", Port: 25565}
	row.Revision = 2
	editorial := config()
	editorial.Java = &Endpoint{Host: "1.1.1.1", Port: 25565}
	store.configs = []Config{editorial}
	editorialID := editorial.ID
	row.EditorialID = &editorialID
	if _, err := h.Update(context.Background(), row.ID, row.Participation); err != ErrInvalidHosting {
		t.Fatalf("arbitrary editorial accepted %v", err)
	}
	editorial.Java = row.Query.Java
	store.configs = []Config{editorial}
	if _, err := h.Update(context.Background(), row.ID, row.Participation); err != nil {
		t.Fatal(err)
	}
	row.Query.Java = &Endpoint{Host: "8.8.4.4", Port: 25565}
	row.Revision = 3
	if _, err := h.Update(context.Background(), row.ID, row.Participation); err != ErrInvalidHosting {
		t.Fatal("non allocation accepted")
	}
	source.rows[0].Suspended = true
	if err := h.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !repo.state.Rows[0].Suspended || repo.state.Rows[0].MonitorID != id {
		t.Fatal("suspend changed identity")
	}
	source.rows = nil
	if err := h.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.state.Rows[0].Present || repo.state.Rows[0].MonitorID != id {
		t.Fatal("removal erased identity")
	}
	if len(store.snapshots) != 0 {
		t.Fatal("unexpected history write")
	}
}
func TestHostingHTTPPrivacy(t *testing.T) {
	h, repo, _ := hostingFixture()
	row, _ := hostingRow(testID, "pending", "", "backends", 1)
	row.Name = "private client name"
	repo.state.Rows = []HostingRow{row}
	r := Router(h.Service, readSecret, adminSecret)
	if w := request(r, "GET", "/v1/admin/hosting/servers", readSecret, nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := request(r, "GET", "/v1/hosting/summary", readSecret, nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), row.Name) || strings.Contains(w.Body.String(), "8.8.8.") || strings.Contains(w.Body.String(), testID) {
		t.Fatal("private fields exposed", w.Body)
	}
	w = request(r, "GET", "/v1/admin/hosting/servers", adminSecret, nil)
	var data struct{ Servers []HostingRow }
	if json.Unmarshal(w.Body.Bytes(), &data) != nil || len(data.Servers) != 1 || !reflect.DeepEqual(data.Servers[0].Query, row.Query) {
		t.Fatal("admin contract", w.Body)
	}
}

func TestHostingCrossplayAndAmbiguousProxies(t *testing.T) {
	h, repo, _ := hostingFixture()
	row, p := hostingRow("crossplay", "standalone", "", "backends", 8)
	row.Query.Bedrock = row.Query.Java
	repo.state.Rows = []HostingRow{row}
	configs := map[string]Config{}
	for _, c := range h.configs(repo.state) {
		configs[c.ID] = c
	}
	snapshots := map[string]Snapshot{p.ID: p}
	out := h.summary(repo.state, configs, snapshots)
	if out.Players == nil || *out.Players != 8 || out.Coverage.Responding != 1 {
		t.Fatalf("crossplay %+v", out)
	}
	a, pa := hostingRow("proxy1", "proxy", "group", "proxy", 10)
	b, pb := hostingRow("proxy2", "proxy", "group", "proxy", 12)
	a.Authority = true
	b.Authority = true
	repo.state.Rows = []HostingRow{a, b}
	for _, c := range h.configs(repo.state) {
		configs[c.ID] = c
	}
	snapshots[pa.ID] = pa
	snapshots[pb.ID] = pb
	out = h.summary(repo.state, configs, snapshots)
	if out.Players != nil || out.Coverage.Eligible != 0 {
		t.Fatal("proxies counted")
	}
	b.Role = "standalone"
	b.Group = nil
	a.Role = "standalone"
	a.Group = nil
	b.Query = a.Query
	b.Allocations = a.Allocations
	repo.state.Rows = []HostingRow{a, b}
	for _, c := range h.configs(repo.state) {
		configs[c.ID] = c
	}
	out = h.summary(repo.state, configs, snapshots)
	if out.Players != nil {
		t.Fatal("duplicate endpoint counted twice")
	}
}

func TestHostingSuspensionDuringCollection(t *testing.T) {
	h, repo, store := hostingFixture()
	row, _ := hostingRow(testID, "standalone", "", "backends", 7)
	repo.state.Rows = []HostingRow{row}
	h.Source = &inventoryFake{rows: []InventoryServer{{ID: row.ID, Name: row.Name, Suspended: true, Allocations: row.Allocations}}}
	c := h.configs(repo.state)[0]
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h.Service.Probe = func(context.Context, Endpoint, bool) Reading {
		close(entered)
		<-release
		n := 7
		return Reading{Status: "online", Players: &n}
	}
	go func() { defer close(done); h.Service.collect(context.Background(), c) }()
	<-entered
	if err := h.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	if store.records != 0 || h.configs(repo.state)[0].Enabled {
		t.Fatal("suspended collection published")
	}
}

func TestHostingCanonicalIPv6(t *testing.T) {
	const expanded = "2606:4700:4700:0000:0000:0000:0000:ABCD"
	const canonical = "2606:4700:4700::abcd"
	panel, err := NewPterodactyl("https://panel.example", "secret")
	if err != nil {
		t.Fatal(err)
	}
	panel.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		item := fixtureInventory(testID)
		attrs := item["attributes"].(map[string]any)
		attrs["relationships"] = map[string]any{"allocations": map[string]any{"data": []any{map[string]any{"attributes": map[string]any{"ip": expanded, "port": 25565}}}}}
		body, _ := json.Marshal(map[string]any{"data": []any{item}, "meta": map[string]any{"pagination": map[string]any{"current_page": 1, "total_pages": 1}}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
	inventory, err := panel.Fetch(context.Background())
	if err != nil || len(inventory) != 1 || inventory[0].Allocations[0].Host != canonical {
		t.Fatalf("IPv6 inventory %+v %v", inventory, err)
	}
	h, repo, _ := hostingFixture()
	row, p := hostingRow(testID, "standalone", "", "backends", 9)
	row.Allocations = inventory[0].Allocations
	row.Query.Java.Host = canonical
	repo.state.Rows = []HostingRow{row}
	participation := row.Participation
	participation.Revision++
	participation.Query = HostingQuery{Java: &Endpoint{Host: expanded, Port: 25565}}
	updated, err := h.Update(context.Background(), testID, participation)
	if err != nil || updated.Query.Java.Host != canonical {
		t.Fatalf("IPv6 update %+v %v", updated, err)
	}
	// Old persisted spellings must still collide with canonical endpoints.
	other := updated
	other.ID = "other"
	other.MonitorID = "other"
	other.Query = HostingQuery{Java: &Endpoint{Host: expanded, Port: 25565}}
	other.Allocations = []Allocation{{Host: expanded, Port: 25565}}
	repo.state.Rows = []HostingRow{updated, other}
	configs := map[string]Config{}
	for _, c := range h.configs(repo.state) {
		configs[c.ID] = c
	}
	p.Revision = updated.Revision
	otherSnapshot := p
	otherSnapshot.ID = other.MonitorID
	out := h.summary(repo.state, configs, map[string]Snapshot{updated.MonitorID: p, other.MonitorID: otherSnapshot})
	if out.Players != nil || out.Coverage.Responding != 0 {
		t.Fatalf("IPv6 alias escaped dedup %+v", out)
	}
	if canonicalHostingHost("::FFFF:8.8.8.8") != "8.8.8.8" {
		t.Fatal("mapped IPv4 was not unmapped")
	}
}
