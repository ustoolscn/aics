package feishu

import (
	"context"
	"sync"
	"time"
)

type Deduper interface {
	Mark(ctx context.Context, key string) (bool, error)
}

type memoryDeduper struct {
	mu   sync.Mutex
	ttl  time.Duration
	seen map[string]time.Time
}

func NewMemoryDeduper(ttl time.Duration) *memoryDeduper {
	return &memoryDeduper{
		ttl:  ttl,
		seen: make(map[string]time.Time),
	}
}

func (d *memoryDeduper) Mark(_ context.Context, key string) (bool, error) {
	if key == "" {
		return true, nil
	}
	now := time.Now()

	d.mu.Lock()
	defer d.mu.Unlock()

	for item, expiresAt := range d.seen {
		if now.After(expiresAt) {
			delete(d.seen, item)
		}
	}

	if expiresAt, ok := d.seen[key]; ok && now.Before(expiresAt) {
		return false, nil
	}
	d.seen[key] = now.Add(d.ttl)
	return true, nil
}
