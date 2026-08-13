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
