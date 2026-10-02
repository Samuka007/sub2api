package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newOpsMetricsLeaderLockTestClient(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	return rdb, mr
}

func TestOpsMetricsLeaderLockLegacyKeyContention(t *testing.T) {
	rdb, mr := newOpsMetricsLeaderLockTestClient(t)
	require.NoError(t, mr.Set(opsMetricsCollectorLeaderLockKey, "legacy-owner"))
	lock := NewOpsMetricsLeaderLock(rdb)

	acquired, err := lock.TryAcquire(context.Background(), "new-owner", time.Minute)
	require.NoError(t, err)
	require.False(t, acquired)
	owner, err := mr.Get(opsMetricsCollectorLeaderLockKey)
	require.NoError(t, err)
	require.Equal(t, "legacy-owner", owner)
	require.False(t, mr.Exists(leaderLockKeyPrefix+opsMetricsCollectorLeaderLockKey))
}

func TestOpsMetricsLeaderLockAcquiresOnlyExactLegacyKey(t *testing.T) {
	rdb, mr := newOpsMetricsLeaderLockTestClient(t)
	lock := NewOpsMetricsLeaderLock(rdb)

	acquired, err := lock.TryAcquire(context.Background(), "owner-a", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	owner, err := mr.Get(opsMetricsCollectorLeaderLockKey)
	require.NoError(t, err)
	require.Equal(t, "owner-a", owner)
	require.False(t, mr.Exists(leaderLockKeyPrefix+opsMetricsCollectorLeaderLockKey))
}

func TestOpsMetricsLeaderLockOwnerMismatchDoesNotRelease(t *testing.T) {
	rdb, mr := newOpsMetricsLeaderLockTestClient(t)
	require.NoError(t, mr.Set(opsMetricsCollectorLeaderLockKey, "peer-owner"))
	lock := NewOpsMetricsLeaderLock(rdb)

	require.NoError(t, lock.Release(context.Background(), "stale-owner"))
	owner, err := mr.Get(opsMetricsCollectorLeaderLockKey)
	require.NoError(t, err)
	require.Equal(t, "peer-owner", owner)
}
