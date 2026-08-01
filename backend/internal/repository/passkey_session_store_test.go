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

func TestPasskeySessionStoreConsumesTokenExactlyOnce(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	store := NewPasskeySessionStore(client)
	want := &service.PasskeySession{Kind: "login", UserID: 42}

	token, err := store.Store(context.Background(), want, 5*time.Minute)
	require.NoError(t, err)
	require.NotEmpty(t, token)

	got, err := store.Consume(context.Background(), token)
	require.NoError(t, err)
	require.Equal(t, want.Kind, got.Kind)
	require.Equal(t, want.UserID, got.UserID)

	_, err = store.Consume(context.Background(), token)
	require.ErrorIs(t, err, service.ErrPasskeySession)
}

func TestPasskeySessionStoreRejectsInvalidTokens(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewPasskeySessionStore(client)

	for _, token := range []string{"", "   ", string(make([]byte, 129))} {
		_, err := store.Consume(context.Background(), token)
		require.ErrorIs(t, err, service.ErrPasskeySession)
	}
}
