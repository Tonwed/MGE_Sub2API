package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

func (c *gatewayCache) SetCodexState(ctx context.Context, key string, payload []byte, ttl time.Duration) error {
	return c.rdb.Set(ctx, key, payload, ttl).Err()
}

func (c *gatewayCache) GetCodexState(ctx context.Context, key string) ([]byte, error) {
	payload, err := c.rdb.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, service.ErrCodexStateNotFound
		}
		return nil, err
	}
	return payload, nil
}

func (c *gatewayCache) DeleteCodexState(ctx context.Context, key string) error {
	return c.rdb.Del(ctx, key).Err()
}
