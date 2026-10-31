package verone

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAutoDiscoveryHints(t *testing.T) {
	cases := []struct {
		name, startup string
		env           map[string]any
		egg, kind     string
	}{
		{"generic", "java {{STARTUP_FLAGS}} -jar {{SERVER_JARFILE}}", map[string]any{"SERVER_JARFILE": "server.jar", "MINECRAFT_VERSION": 1.21}, "", "java"},
		{"generic without variables", "java -jar server.jar", nil, "", "java"},
		{"neoforge args", "java @user_jvm_args.txt @libraries/net/neoforged/neoforge/unix_args.txt", map[string]any{"NEOFORGE_VERSION": "21.1.2"}, "", "java"},
		{"velocity generic", "java -jar {{SERVER_JARFILE}}", map[string]any{"SERVER_JARFILE": "server.jar", "VELOCITY_VERSION": "3.4.0"}, "", "proxy"},
		{"bungee", "java -jar BungeeCord.jar", nil, "", "proxy"},
		{"bungee generic", "java -jar server.jar", map[string]any{"BUNGEE_VERSION": "latest"}, "BungeeCord", "proxy"},
		{"bungeecord generic no egg", "java -jar server.jar", map[string]any{"BUNGEECORD_VERSION": "latest"}, "", "proxy"},
		{"waterfall generic", "java -jar server.jar", map[string]any{"WATERFALL_VERSION": "latest"}, "", "proxy"},
		{"geyser generic standalone", "java -jar server.jar", map[string]any{"GEYSER_VERSION": "latest"}, "Geyser Standalone", "proxy"},
		{"geyser generic plugin", "java -jar server.jar", map[string]any{"GEYSER_VERSION": "latest"}, "Paper with Geyser", "java"},
		{"backend stale bungee", "java -jar paper.jar", map[string]any{"BUNGEE_VERSION": "latest"}, "BungeeCord", "java"},
		{"waterfall", "java -jar waterfall.jar", nil, "", "proxy"},
		{"geyser standalone", "java -jar Geyser-Standalone.jar", nil, "", "proxy"},
		{"geyser plugin backend", "java -jar paper.jar", map[string]any{"GEYSER_VERSION": "latest"}, "Geyser plugin", "java"},
		{"residual proxy download backend", "java -jar paper.jar", map[string]any{"DL_PATH": "https://api.papermc.io/v2/projects/waterfall", "VELOCITY_VERSION": "stale"}, "", "java"},
		{"bedrock", "./bedrock_server", map[string]any{"BEDROCK_VERSION": "latest"}, "", "bedrock"},
		{"python", "python3 app.py", map[string]any{"PASSWORD": "do-not-persist"}, "Minecraft", "other"},
		{"mixed startup", "python3 prepare.py && java -jar server.jar", nil, "", "java"},
		{"egg alone", "./start.sh", nil, "Velocity", "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := discover(panelContainer{Startup: c.startup, Environment: c.env}, c.egg)
			if d.Kind != c.kind {
				t.Fatalf("kind=%s want%s", d.Kind, c.kind)
			}
			data, _ := json.Marshal(d)
			if strings.Contains(string(data), "do-not-persist") || strings.Contains(string(data), "PASSWORD") {
				t.Fatal("metadata leaked")
			}
		})
	}
}
func autoSourceItem(id, kind string) InventoryServer {
	a := Allocation{Host: "8.8.8.8", Port: 25565}
	return InventoryServer{ID: id, Allocations: []Allocation{a}, Primary: &a, Discovery: Discovery{Kind: kind, Signature: "v1"}}
}
func TestAutoInventoryMigrationRefreshAndNoResponse(t *testing.T) {
	h, repo, store := hostingFixture()
	source := &inventoryFake{rows: []InventoryServer{autoSourceItem(testID, "java")}}
	h.Source = source
	// Migrate a pre-automation pending row without replacing its technical identity.
	repo.state.Rows = []HostingRow{{ID: testID, MonitorID: "stable-monitor", Participation: Participation{Revision: 7, Role: "pending", Enabled: true, Mode: "backends"}}}
	peak := 25
	store.snapshots = []Snapshot{{ID: "stable-monitor", Peak: &peak, Revision: 7, UpdatedAt: time.Now()}}
	if err := h.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	row := repo.state.Rows[0]
	if !row.Automatic || row.Role != "standalone" || row.Query.Java == nil || row.EditorialID != nil || row.Revision != 8 || row.MonitorID != "stable-monitor" {
		t.Fatalf("migration %+v", row)
	}
	if err := h.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.state.Rows[0].Revision != 8 {
		t.Fatal("identical sync changed revision")
	}
	configs := h.configs(repo.state)
	if len(configs) != 1 || !configs[0].Enabled || !configs[0].Hosting {
		t.Fatal("automatic collection not enabled")
	}
	h.Service.Probe = func(context.Context, Endpoint, bool) Reading { return Reading{Status: "no_response"} }
	h.Service.collect(context.Background(), configs[0])
	p := store.snapshots[0]
	if p.Players != nil || p.Status != "no_response" || p.Peak == nil || *p.Peak != 25 {
		t.Fatalf("failed sample fabricated/lost history %+v", p)
	}
	source.rows[0].Discovery.Signature = "v2"
	if err := h.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.state.Rows[0].Revision != 9 {
		t.Fatal("version change did not advance revision")
	}
	source.rows[0].Primary = &Allocation{Host: "1.1.1.1", Port: 25566}
	source.rows[0].Allocations = []Allocation{*source.rows[0].Primary}
	if err := h.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	row = repo.state.Rows[0]
	if row.Revision != 10 || row.Query.Java.Host != "1.1.1.1" || row.Query.Java.Port != 25566 || row.MonitorID != "stable-monitor" {
		t.Fatal("allocation refresh/identity failed")
	}
	source.rows = nil
	if err := h.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.state.Rows[0].Present || h.configs(repo.state)[0].Enabled || store.snapshots[0].Peak == nil {
		t.Fatal("removal destroyed history or left collector active")
	}
}
func TestAutoPrimaryOnlyAndPrivateNoFallback(t *testing.T) {
	for _, kind := range []string{"java", "bedrock", "unknown", "proxy", "other"} {
		item := autoSourceItem(testID, kind)
		p := automaticParticipation(item, 1)
		if kind == "proxy" || kind == "other" {
			if p.Role != "excluded" || p.Query.Java != nil || p.Query.Bedrock != nil {
				t.Fatal("excluded queried")
			}
			continue
		}
		if kind == "java" && (p.Query.Java == nil || p.Query.Bedrock != nil) {
			t.Fatal("java query")
		}
		if kind == "bedrock" && (p.Query.Bedrock == nil || p.Query.Java != nil) {
			t.Fatal("bedrock query")
		}
		if kind == "unknown" && (p.Query.Java == nil || p.Query.Bedrock == nil) {
			t.Fatal("unknown protocol fallback")
		}
		item.Primary = &Allocation{Host: "10.0.0.1", Port: 25565}
		p = automaticParticipation(item, 2)
		if p.Query.Java != nil || p.Query.Bedrock != nil {
			t.Fatal("private primary fell back to secondary")
		}
		item.Primary = nil
		p = automaticParticipation(item, 3)
		if p.Query.Java != nil || p.Query.Bedrock != nil {
			t.Fatal("missing primary fell back to secondary")
		}
	}
}
func TestAutoAdapterPrimaryAndNoMetadataRetention(t *testing.T) {
	panel, _ := NewPterodactyl("https://panel.example", "secret")
	panel.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		item := fixtureInventory(testID)
		a := item["attributes"].(map[string]any)
		a["allocation"] = 17
		a["container"] = map[string]any{"startup_command": "java -jar {{SERVER_JARFILE}}", "environment": map[string]any{"SERVER_JARFILE": "server.jar", "MINECRAFT_VERSION": 1.21, "SECRET": "must-not-survive"}}
		a["relationships"] = map[string]any{"egg": map[string]any{"object": "null_resource", "attributes": nil}, "allocations": map[string]any{"data": []any{map[string]any{"attributes": map[string]any{"id": 18, "ip": "1.1.1.1", "port": 25566}}, map[string]any{"attributes": map[string]any{"id": 17, "ip": "2606:4700:4700:0000:0000:0000:0000:ABCD", "port": 25565}}}}}
		body, _ := json.Marshal(map[string]any{"data": []any{item}, "meta": map[string]any{"pagination": map[string]any{"current_page": 1, "total_pages": 1}}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
	items, err := panel.Fetch(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatal(err)
	}
	item := items[0]
	if item.Primary == nil || item.Primary.Host != "2606:4700:4700::abcd" || item.Primary.Port != 25565 || item.Discovery.Kind != "java" {
		t.Fatalf("primary %+v", item)
	}
	data, _ := json.Marshal(item)
	for _, raw := range []string{"must-not-survive", "SECRET", "startup_command", "SERVER_JARFILE"} {
		if strings.Contains(string(data), raw) {
			t.Fatal("raw metadata retained")
		}
	}
}
func TestAutoProxySampleDoesNotBreakEditorial(t *testing.T) {
	for _, hosting := range []bool{false, true} {
		c := config()
		c.Hosting = hosting
		store := &memoryStore{configs: []Config{c}}
		s := New(store)
		s.Probe = func(context.Context, Endpoint, bool) Reading {
			n := 10
			return Reading{Status: "online", Proxy: true, Players: &n}
		}
		s.collect(context.Background(), c)
		p := store.snapshots[0]
		if !p.Proxy {
			t.Fatal("proxy signal lost")
		}
		if hosting && (p.Players != nil || p.Status != "unknown") {
			t.Fatal("hosting proxy got counted")
		}
		if !hosting && (p.Players == nil || *p.Players != 10 || p.Status != "online") {
			t.Fatal("editorial proxy regressed")
		}
		data, _ := json.Marshal(p)
		if strings.Contains(strings.ToLower(string(data)), "proxy") {
			t.Fatal("private proxy signal exposed")
		}
	}
}

func TestAutoUncertainSourcesCountHonestly(t *testing.T) {
	h, repo, _ := hostingFixture()
	row, p := hostingRow("candidate", "standalone", "", "backends", 8)
	row.Automatic = true
	row.Discovery = Discovery{Kind: "java", Uncertain: true}
	repo.state.Rows = []HostingRow{row}
	configs := map[string]Config{}
	for _, c := range h.configs(repo.state) {
		configs[c.ID] = c
	}
	out := h.summary(repo.state, configs, map[string]Snapshot{p.ID: p})
	if out.Players == nil || *out.Players != 8 || out.State != "partial" || out.Coverage.Pending != 1 {
		t.Fatalf("uncertain sample %+v", out)
	}
	p.Proxy = true
	out = h.summary(repo.state, configs, map[string]Snapshot{p.ID: p})
	if out.Players != nil || out.Coverage.Eligible != 0 || out.Coverage.Pending != 0 {
		t.Fatal("runtime proxy not excluded")
	}
}
