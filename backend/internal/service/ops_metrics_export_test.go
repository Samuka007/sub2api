package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type fakeOpsMetricsLeaderLock struct {
	mu         sync.Mutex
	owner      string
	acquireErr error
	releaseErr error
}

func (f *fakeOpsMetricsLeaderLock) TryAcquire(_ context.Context, owner string, _ time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.acquireErr != nil {
		return false, f.acquireErr
	}
	if f.owner != "" {
		return false, nil
	}
	f.owner = owner
	return true, nil
}

func (f *fakeOpsMetricsLeaderLock) Release(_ context.Context, owner string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.releaseErr != nil {
		return f.releaseErr
	}
	if f.owner == owner {
		f.owner = ""
	}
	return nil
}

func (f *fakeOpsMetricsLeaderLock) occupy(owner string) {
	f.mu.Lock()
	f.owner = owner
	f.mu.Unlock()
}

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
	lock := &fakeOpsMetricsLeaderLock{}
	collector := &OpsMetricsCollector{leaderLock: lock, instanceID: "instance-a"}

	release, acquired := collector.tryAcquireLeaderLock(context.Background())
	require.True(t, acquired)
	require.NotNil(t, release)
	collector.publishPersistedSnapshot(&OpsInsertSystemMetricsInput{SuccessCount: 3})

	lock.occupy("peer")
	_, acquired = collector.tryAcquireLeaderLock(context.Background())
	require.False(t, acquired)
	require.Nil(t, collector.LatestSnapshot())
}

func TestLeaderSnapshotClearsWhenRedisAndFallbackLockError(t *testing.T) {
	lock := &fakeOpsMetricsLeaderLock{}
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

func TestOpsMetricsLeaderLockReleaseFailureIsObservableWithoutBlocking(t *testing.T) {
	lock := &fakeOpsMetricsLeaderLock{releaseErr: errors.New("sensitive owner payload")}
	collector := &OpsMetricsCollector{leaderLock: lock, instanceID: "instance-a"}
	release, acquired := collector.tryAcquireLeaderLock(context.Background())
	require.True(t, acquired)

	require.NotPanics(t, release)
	require.Equal(t, uint64(1), collector.LeaderLockReleaseFailures())
}

func TestOpsMetricsCollectorStopCancelsAndWaitsForRun(t *testing.T) {
	entered := make(chan struct{})
	exited := make(chan struct{})
	collector := &OpsMetricsCollector{collectOnceHook: func(ctx context.Context) {
		close(entered)
		<-ctx.Done()
		close(exited)
	}}
	collector.Start()
	<-entered

	collector.Stop()
	select {
	case <-exited:
	default:
		t.Fatal("Stop returned before the running collection exited")
	}
}

func TestOpsMetricsCollectorStopBeforeStartDoesNotPoisonLifecycle(t *testing.T) {
	entered := make(chan struct{})
	collector := &OpsMetricsCollector{collectOnceHook: func(ctx context.Context) {
		close(entered)
		<-ctx.Done()
	}}
	collector.Stop()
	collector.Start()
	<-entered

	done := make(chan struct{})
	go func() {
		collector.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("collector started after an early Stop could not be stopped")
	}
}

func TestOpsMetricsCollectorRepeatedConcurrentStartStopDoesNotLeakRuns(t *testing.T) {
	var active atomic.Int32
	var maxActive atomic.Int32
	collector := &OpsMetricsCollector{collectOnceHook: func(ctx context.Context) {
		current := active.Add(1)
		for {
			maximum := maxActive.Load()
			if current <= maximum || maxActive.CompareAndSwap(maximum, current) {
				break
			}
		}
		<-ctx.Done()
		active.Add(-1)
	}}

	for range 5 {
		var starts sync.WaitGroup
		for range 8 {
			starts.Add(1)
			go func() { defer starts.Done(); collector.Start() }()
		}
		starts.Wait()
		require.Eventually(t, func() bool { return active.Load() == 1 }, time.Second, time.Millisecond)

		var stops sync.WaitGroup
		for range 8 {
			stops.Add(1)
			go func() { defer stops.Done(); collector.Stop() }()
		}
		stops.Wait()
		require.Zero(t, active.Load())
	}
	require.Equal(t, int32(1), maxActive.Load())
}
