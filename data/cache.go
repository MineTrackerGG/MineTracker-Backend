package data

import (
	"context"
	"time"

	"github.com/allegro/bigcache/v3"
)

var Cache *bigcache.BigCache

func InitCache() error {
	config := bigcache.Config{
		Shards:             1024,
		LifeWindow:         1 * time.Hour,
		CleanWindow:        5 * time.Minute,
		MaxEntriesInWindow: 1000 * 10 * 60,
		MaxEntrySize:       500,
		Verbose:            false,
		HardMaxCacheSize:   8192,
	}

	cache, err := bigcache.New(context.Background(), config)
	Cache = cache
	return err
}
