package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGatewayCacheCodexState(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := &gatewayCache{rdb: client}
	ctx := context.Background()

	require.NoError(t, cache.SetCodexState(ctx, "state-key", []byte("payload"), time.Minute))
	payload, err := cache.GetCodexState(ctx, "state-key")
	require.NoError(t, err)
	require.Equal(t, []byte("payload"), payload)

	mini.FastForward(2 * time.Minute)
	_, err = cache.GetCodexState(ctx, "state-key")
	require.ErrorIs(t, err, service.ErrCodexStateNotFound)

	require.NoError(t, cache.SetCodexState(ctx, "state-key", []byte("payload"), time.Minute))
	require.NoError(t, cache.DeleteCodexState(ctx, "state-key"))
	_, err = cache.GetCodexState(ctx, "state-key")
	require.ErrorIs(t, err, service.ErrCodexStateNotFound)
}
