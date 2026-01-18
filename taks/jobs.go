package task

import (
	"MineTracker/data"
	"time"
)

type PingJob struct {
	interval time.Duration
	servers  []data.PingableServer
}

type Cache1hJob struct {
	interval time.Duration
	servers  []data.PingableServer
}

type Cache24hJob struct {
	interval time.Duration
	servers  []data.PingableServer
}

type Cache7dJob struct {
	interval time.Duration
	servers  []data.PingableServer
}

type Cache30dJob struct {
	interval time.Duration
	servers  []data.PingableServer
}
