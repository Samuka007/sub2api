package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const opsMetricsCollectorLeaderLockKey = "ops:metrics:collector:leader"

type opsMetricsLeaderLock struct {
	rdb *redis.Client
}

// NewOpsMetricsLeaderLock preserves the exact Redis key used by the original
// Ops metrics collector. Unlike the generic leader lock cache, this narrow
// adapter never accepts an arbitrary key and therefore cannot add a namespace.
func NewOpsMetricsLeaderLock(rdb *redis.Client) service.OpsMetricsLeaderLock {
	return &opsMetricsLeaderLock{rdb: rdb}
}

func (l *opsMetricsLeaderLock) TryAcquire(ctx context.Context, owner string, ttl time.Duration) (bool, error) {
	return l.rdb.SetNX(ctx, opsMetricsCollectorLeaderLockKey, owner, ttl).Result()
}

func (l *opsMetricsLeaderLock) Release(ctx context.Context, owner string) error {
	return leaderLockReleaseScript.Run(ctx, l.rdb, []string{opsMetricsCollectorLeaderLockKey}, owner).Err()
}
