package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/whim-proxy/internal/types"
)

type redisStore struct {
	client   *redis.Client
	ttl      time.Duration
	capacity int64
}

// NewRedis returns a Redis-backed EventStore.
// url must be a valid Redis URL (e.g. redis://localhost:6379).
// ttl sets the expiry of each channel key after its last write; 0 means no expiry.
// capacity is the maximum number of events kept per channel.
func NewRedis(url string, ttl time.Duration, capacity int) (EventStore, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis URL: %w", err)
	}
	client := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	return &redisStore{client: client, ttl: ttl, capacity: int64(capacity)}, nil
}

func (r *redisStore) key(channel string) string {
	return "whim:logs:" + channel
}

func (r *redisStore) Push(channel string, event types.WebhookEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	ctx := context.Background()
	key := r.key(channel)
	pipe := r.client.Pipeline()
	pipe.LPush(ctx, key, data)
	pipe.LTrim(ctx, key, 0, r.capacity-1)
	if r.ttl > 0 {
		pipe.Expire(ctx, key, r.ttl)
	}
	_, err = pipe.Exec(ctx)
	return err
}

func (r *redisStore) Recent(channel string, n int) ([]types.WebhookEvent, error) {
	ctx := context.Background()
	vals, err := r.client.LRange(ctx, r.key(channel), 0, int64(n-1)).Result()
	if err != nil {
		return nil, err
	}
	// LRange returns newest-first; reverse to chronological order.
	events := make([]types.WebhookEvent, 0, len(vals))
	for i := len(vals) - 1; i >= 0; i-- {
		var e types.WebhookEvent
		if err := json.Unmarshal([]byte(vals[i]), &e); err != nil {
			continue
		}
		events = append(events, e)
	}
	return events, nil
}
