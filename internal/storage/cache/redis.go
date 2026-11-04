package cache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis adapts a go-redis client to the Backend interface.
type Redis struct {
	client *redis.Client
}

// NewRedis returns a Backend using client.
func NewRedis(client *redis.Client) *Redis {
	return &Redis{client: client}
}

// Get implements Backend. A missing key is not an error.
func (r *Redis) Get(ctx context.Context, key string) (string, bool, error) {
	value, err := r.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// Set implements Backend.
func (r *Redis) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return r.client.Set(ctx, key, value, ttl).Err()
}
