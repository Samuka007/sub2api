package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpsMetricsCollectorPublishesOnlyPersistedSnapshot(t *testing.T) {
	collector := &OpsMetricsCollector{}
	qps := 2.5
	first := &OpsInsertSystemMetricsInput{CreatedAt: time.Unix(100, 0), SuccessCount: 3, QPS: &qps}

	collector.publishPersistedSnapshot(first)
	got := collector.LatestSnapshot()
	require.NotSame(t, first, got)
	require.Equal(t, first.CreatedAt, got.CreatedAt)
	require.Equal(t, int64(3), got.SuccessCount)

	first.SuccessCount = 99
	qps = 9.9
	require.Equal(t, int64(3), collector.LatestSnapshot().SuccessCount)
	require.Equal(t, 2.5, *collector.LatestSnapshot().QPS)
}

func TestPersistAndPublishDoesNotReplaceSnapshotWhenOriginalOpsWriteFails(t *testing.T) {
	collector := &OpsMetricsCollector{opsRepo: &opsMetricsPersistStub{err: errors.New("write failed")}}
	collector.publishPersistedSnapshot(&OpsInsertSystemMetricsInput{CreatedAt: time.Unix(100, 0), SuccessCount: 3})

	err := collector.persistAndPublish(context.Background(), &OpsInsertSystemMetricsInput{CreatedAt: time.Unix(200, 0), SuccessCount: 5})
	require.Error(t, err)
	got := collector.LatestSnapshot()
	require.Equal(t, time.Unix(100, 0), got.CreatedAt)
	require.Equal(t, int64(3), got.SuccessCount)
}

type opsMetricsPersistStub struct {
	OpsRepository
	err error
}

func (s *opsMetricsPersistStub) InsertSystemMetrics(_ context.Context, _ *OpsInsertSystemMetricsInput) error {
	return s.err
}

func TestLeaderSnapshotClearsWhenNextAcquireIsNotLeader(t *testing.T) {
	lock := &fakeLeaderLockCache{}
	collector := &OpsMetricsCollector{leaderLock: lock, instanceID: "instance-a"}

	release, acquired := collector.tryAcquireLeaderLock(context.Background())
	require.True(t, acquired)
	require.NotNil(t, release)
	collector.publishPersistedSnapshot(&OpsInsertSystemMetricsInput{SuccessCount: 3})

	_, _ = lock.TryAcquireLeaderLock(context.Background(), opsMetricsCollectorLeaderLockKey, "peer", time.Minute)
	_, acquired = collector.tryAcquireLeaderLock(context.Background())
	require.False(t, acquired)
	require.Nil(t, collector.LatestSnapshot())
}

func TestLeaderSnapshotClearsWhenRedisAndFallbackLockError(t *testing.T) {
	lock := &fakeLeaderLockCache{}
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	collector := &OpsMetricsCollector{leaderLock: lock, db: db, instanceID: "instance-a"}
	release, acquired := collector.tryAcquireLeaderLock(context.Background())
	require.True(t, acquired)
	require.NotNil(t, release)
	collector.publishPersistedSnapshot(&OpsInsertSystemMetricsInput{SuccessCount: 3})

	lock.acquireErr = errors.New("lock failed")
	mock.ExpectQuery(`SELECT pg_try_advisory_lock`).WithArgs(opsMetricsCollectorAdvisoryLockID).WillReturnError(errors.New("lock failed"))
	_, acquired = collector.tryAcquireLeaderLock(context.Background())
	require.False(t, acquired)
	require.Nil(t, collector.LatestSnapshot())
	require.NoError(t, mock.ExpectationsWereMet())
	require.NoError(t, db.Close())
}

func TestPublishedSnapshotIsClearedWhenMonitoringIsDisabled(t *testing.T) {
	collector := &OpsMetricsCollector{cfg: &config.Config{Ops: config.OpsConfig{Enabled: false}}}
	collector.publishPersistedSnapshot(&OpsInsertSystemMetricsInput{SuccessCount: 3})
	collector.collectOnce()
	require.Nil(t, collector.LatestSnapshot())
}
