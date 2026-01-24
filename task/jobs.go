package task

import (
	"MineTracker/data"
	"time"
)

type PingJob struct {
	interval time.Duration
	servers  []data.PingableServer
}
