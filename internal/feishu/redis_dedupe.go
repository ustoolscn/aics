package feishu

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisDeduper struct {
	client *redis.Client
	ttl    time.Duration
	prefix string
}

func NewRedisDeduper(client *redis.Client, ttl time.Duration, prefix string) *RedisDeduper {
	if prefix == "" {
		prefix = "aics:dedupe:feishu:"
	}
	return &RedisDeduper{client: client, ttl: ttl, prefix: prefix}
}

func (d *RedisDeduper) Mark(ctx context.Context, key string) (bool, error) {
	if key == "" {
		return true, nil
	}
	return d.client.SetNX(ctx, d.prefix+key, "1", d.ttl).Result()
}
