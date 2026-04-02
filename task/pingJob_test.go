package task

import (
	"MineTracker/data"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/influxdata/influxdb-client-go/v2/api/write"
)

type fakePinger struct {
	resp    *mcPingResult
	err     error
	called  bool
	host    string
	port    uint16
	timeout time.Duration
}

func (f *fakePinger) ping(host string, port uint16, timeout time.Duration) (*mcPingResult, error) {
	f.called = true
	f.host = host
	f.port = port
	f.timeout = timeout
	return f.resp, f.err
}

func TestParseAddress(t *testing.T) {
	host, port := parseAddress("example.org:25570")
	if host != "example.org" {
		t.Fatalf("unexpected host: %s", host)
	}
	if port == nil || *port != 25570 {
		t.Fatalf("unexpected port: %v", port)
	}

	host, port = parseAddress("example.org")
	if host != "example.org" {
		t.Fatalf("unexpected host without port: %s", host)
	}
	if port != nil {
		t.Fatalf("expected nil port for host without explicit port, got %v", *port)
	}

	host, port = parseAddress("[2001:db8::1]:25565")
	if host != "2001:db8::1" {
		t.Fatalf("unexpected ipv6 host: %s", host)
	}
	if port == nil || *port != 25565 {
		t.Fatalf("unexpected ipv6 port: %v", port)
	}

	host, port = parseAddress("2001:db8::1")
	if host != "2001:db8::1" {
		t.Fatalf("unexpected bare ipv6 host: %s", host)
	}
	if port != nil {
		t.Fatalf("expected nil port for bare ipv6, got %v", *port)
	}
}

func TestLoadMaxConcurrentPings(t *testing.T) {
	old := os.Getenv("PING_MAX_CONCURRENT")
	defer func() {
		if old == "" {
			_ = os.Unsetenv("PING_MAX_CONCURRENT")
			return
		}
		_ = os.Setenv("PING_MAX_CONCURRENT", old)
	}()

	_ = os.Unsetenv("PING_MAX_CONCURRENT")
	if got := loadMaxConcurrentPings(); got != 96 {
		t.Fatalf("default max concurrent should be 96, got %d", got)
	}

	_ = os.Setenv("PING_MAX_CONCURRENT", "not-a-number")
	if got := loadMaxConcurrentPings(); got != 96 {
		t.Fatalf("invalid env should fallback to 96, got %d", got)
	}

	_ = os.Setenv("PING_MAX_CONCURRENT", "0")
	if got := loadMaxConcurrentPings(); got != 1 {
		t.Fatalf("value below minimum should clamp to 1, got %d", got)
	}

	_ = os.Setenv("PING_MAX_CONCURRENT", "5000")
	if got := loadMaxConcurrentPings(); got != 1000 {
		t.Fatalf("value above maximum should clamp to 1000, got %d", got)
	}

	_ = os.Setenv("PING_MAX_CONCURRENT", "128")
	if got := loadMaxConcurrentPings(); got != 128 {
		t.Fatalf("expected explicit value 128, got %d", got)
	}
}

func TestPortOrDefault(t *testing.T) {
	def := uint16(25565)
	if got := portOrDefault(nil, def); got != def {
		t.Fatalf("expected default port %d, got %d", def, got)
	}

	p := uint16(25570)
	if got := portOrDefault(&p, def); got != 25570 {
		t.Fatalf("expected explicit port 25570, got %d", got)
	}
}

func TestPingServerFailureMarksOfflineAndQueuesWrite(t *testing.T) {
	oldCache := serverCacheMap
	oldDB := dbWriteQueue
	oldInflux := influxQueue
	oldLimit := pingLimit
	oldDropped := droppedInfluxPoints
	defer func() {
		serverCacheMap = oldCache
		dbWriteQueue = oldDB
		influxQueue = oldInflux
		pingLimit = oldLimit
		droppedInfluxPoints = oldDropped
	}()

	serverCacheMap = map[string]data.Server{}
	dbWriteQueue = make(chan dbWriteOp, 1)
	influxQueue = make(chan *write.Point, 1)
	pingLimit = make(chan struct{}, 1)
	droppedInfluxPoints = 0

	job := &PingJob{}
	server := data.PingableServer{Name: "A", IP: "example.org:25565", Type: "java"}
	fake := &fakePinger{err: errors.New("dial timeout")}

	job.pingServer(server, fake)

	if !fake.called {
		t.Fatal("expected pinger to be called")
	}
	if fake.host != "example.org" || fake.port != 25565 {
		t.Fatalf("unexpected ping target %s:%d", fake.host, fake.port)
	}

	entry, ok := serverCacheMap[server.IP]
	if !ok {
		t.Fatal("expected server cache entry")
	}
	if entry.Online {
		t.Fatal("expected offline server state")
	}
	if entry.PlayerCount != 0 {
		t.Fatalf("expected player count 0, got %d", entry.PlayerCount)
	}
	if !entry.Active {
		t.Fatal("expected newly created entry to default active=true")
	}

	select {
	case op := <-dbWriteQueue:
		if op.server.IP != server.IP || op.server.Online {
			t.Fatalf("unexpected db write payload: %+v", op.server)
		}
	default:
		t.Fatal("expected db write operation to be queued")
	}
}

func TestPingServerSuccessWritesZeroPlayerCount(t *testing.T) {
	oldCache := serverCacheMap
	oldDB := dbWriteQueue
	oldInflux := influxQueue
	oldLimit := pingLimit
	oldDropped := droppedInfluxPoints
	defer func() {
		serverCacheMap = oldCache
		dbWriteQueue = oldDB
		influxQueue = oldInflux
		pingLimit = oldLimit
		droppedInfluxPoints = oldDropped
	}()

	serverIP := "example.org:25565"
	serverCacheMap = map[string]data.Server{
		serverIP: {
			Name:        "Old",
			IP:          serverIP,
			Type:        "java",
			Online:      false,
			PlayerCount: 5,
			Peak:        7,
			Active:      true,
		},
	}
	dbWriteQueue = make(chan dbWriteOp, 1)
	influxQueue = make(chan *write.Point, 1)
	pingLimit = make(chan struct{}, 1)
	droppedInfluxPoints = 0

	job := &PingJob{}
	server := data.PingableServer{Name: "New", IP: serverIP, Type: "java"}
	fake := &fakePinger{
		resp: &mcPingResult{PlayerCount: 0, Favicon: ""},
	}

	job.pingServer(server, fake)

	entry := serverCacheMap[serverIP]
	if !entry.Online {
		t.Fatal("expected online server state")
	}
	if entry.PlayerCount != 0 {
		t.Fatalf("expected player count to update to 0, got %d", entry.PlayerCount)
	}
	if entry.Peak != 7 {
		t.Fatalf("expected peak to stay 7, got %d", entry.Peak)
	}

	select {
	case <-dbWriteQueue:
	default:
		t.Fatal("expected db write operation to be queued")
	}

	select {
	case <-influxQueue:
	default:
		t.Fatal("expected influx point to be queued")
	}
}
