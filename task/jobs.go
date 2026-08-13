package task

import (
	"MineTracker/data"
	"sync"
	"time"
)

type PingJob struct {
	interval time.Duration
	servers  []data.PingableServer
	mu       sync.RWMutex
}

func (j *PingJob) snapshotServers() []data.PingableServer {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return append([]data.PingableServer(nil), j.servers...)
}
