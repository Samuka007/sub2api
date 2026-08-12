//go:build integration

package service

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestDBAdvisoryLockLeaseIsExclusiveAcrossPools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("Docker is required for integration tests: %v", err)
		}
		t.Skip("Docker is unavailable")
	}

	container, err := tcpostgres.Run(
		ctx,
		"postgres:18.1-alpine3.23",
		tcpostgres.WithDatabase("sub2api_test"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		terminateCtx, terminateCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer terminateCancel()
		require.NoError(t, container.Terminate(terminateCtx))
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db1, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db1.Close()) })
	db2, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db2.Close()) })
	require.NoError(t, db1.PingContext(ctx))
	require.NoError(t, db2.PingContext(ctx))

	lockID := hashAdvisoryLockID("integration:advisory-lock-lease")
	lease1, acquired, err := tryAcquireDBAdvisoryLockLease(ctx, db1, lockID)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, lease1)

	lease2, acquired, err := tryAcquireDBAdvisoryLockLease(ctx, db2, lockID)
	require.NoError(t, err)
	require.False(t, acquired)
	require.Nil(t, lease2)

	require.NoError(t, lease1.Release())
	lease2, acquired, err = tryAcquireDBAdvisoryLockLease(ctx, db2, lockID)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, lease2)
	require.NoError(t, lease2.Ping(ctx))
	require.NoError(t, lease2.Release())
	require.NoError(t, lease2.Release())

	terminatedLease, acquired, err := tryAcquireDBAdvisoryLockLease(ctx, db1, lockID)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, terminatedLease)

	var backendPID int
	require.NoError(t, terminatedLease.conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&backendPID))
	require.Positive(t, backendPID)

	var terminated bool
	require.NoError(t, db2.QueryRowContext(ctx, "SELECT pg_terminate_backend($1, 5000)", backendPID).Scan(&terminated))
	require.True(t, terminated)
	require.Eventually(t, func() bool {
		pingCtx, pingCancel := context.WithTimeout(context.Background(), time.Second)
		defer pingCancel()
		return terminatedLease.Ping(pingCtx) != nil
	}, 5*time.Second, 50*time.Millisecond)

	releaseErr := terminatedLease.Release()
	require.Error(t, releaseErr)
	require.EqualError(t, terminatedLease.Release(), releaseErr.Error())

	var replacementLease *dbAdvisoryLockLease
	require.Eventually(t, func() bool {
		acquireCtx, acquireCancel := context.WithTimeout(context.Background(), time.Second)
		defer acquireCancel()
		var acquireErr error
		replacementLease, acquired, acquireErr = tryAcquireDBAdvisoryLockLease(acquireCtx, db2, lockID)
		return acquireErr == nil && acquired
	}, 5*time.Second, 50*time.Millisecond)
	require.NotNil(t, replacementLease)
	require.NoError(t, replacementLease.Release())
}

func TestQuotaRecoveryServiceReacquiresTerminatedAdvisoryLockLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("Docker is required for integration tests: %v", err)
		}
		t.Skip("Docker is unavailable")
	}

	container, err := tcpostgres.Run(
		ctx,
		"postgres:18.1-alpine3.23",
		tcpostgres.WithDatabase("sub2api_test"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		terminateCtx, terminateCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer terminateCancel()
		require.NoError(t, container.Terminate(terminateCtx))
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	serviceDB, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, serviceDB.Close()) })
	observerDB, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, observerDB.Close()) })
	require.NoError(t, serviceDB.PingContext(ctx))
	require.NoError(t, observerDB.PingContext(ctx))

	primaryCycles := &quotaRecoveryCycleProbeRepository{}
	primary := newQuotaRecoveryService(
		primaryCycles,
		quotaRecoveryIntegrationChecker{},
		nil,
		quotaRecoveryIntegrationConfig(),
	)
	primary.db = serviceDB
	primary.leaseHealthInterval = 10 * time.Millisecond
	primary.reacquireDelayFor = func(int) time.Duration { return 10 * time.Millisecond }
	t.Cleanup(primary.Stop)
	require.NoError(t, primary.Start())
	require.Eventually(t, func() bool {
		return primaryCycles.count.Load() == 1
	}, 5*time.Second, 10*time.Millisecond, "the initial lease must start exactly one recovery cycle")

	lockID := hashAdvisoryLockID(quotaRecoverySingletonLockKey)
	initialPID, err := advisoryLockHolderPID(ctx, observerDB, lockID)
	require.NoError(t, err)
	require.Positive(t, initialPID)

	var terminated bool
	require.NoError(t, observerDB.QueryRowContext(
		ctx,
		"SELECT pg_terminate_backend($1, 5000)",
		initialPID,
	).Scan(&terminated))
	require.True(t, terminated)

	var replacementPID int
	require.Eventually(t, func() bool {
		if primaryCycles.count.Load() < 2 {
			return false
		}
		pid, holderErr := advisoryLockHolderPID(ctx, observerDB, lockID)
		if holderErr != nil || pid == initialPID {
			return false
		}
		replacementPID = pid
		return true
	}, 10*time.Second, 20*time.Millisecond, "the running service must reacquire its lease and start a new cycle without another Start call")
	require.Positive(t, replacementPID)
	require.Equal(t, int64(2), primaryCycles.count.Load(), "a long cycle interval excludes a timer-driven second run")

	competitorCycles := &quotaRecoveryCycleProbeRepository{}
	competitor := newQuotaRecoveryService(
		competitorCycles,
		quotaRecoveryIntegrationChecker{},
		nil,
		quotaRecoveryIntegrationConfig(),
	)
	competitor.db = observerDB
	t.Cleanup(competitor.Stop)
	require.ErrorIs(t, competitor.Start(), ErrQuotaRecoveryAlreadyRunning)
	require.Zero(t, competitorCycles.count.Load())

	primary.Stop()
	require.NoError(t, competitor.Start(), "Stop must release the replacement lease")
	require.Eventually(t, func() bool {
		return competitorCycles.count.Load() == 1
	}, 5*time.Second, 10*time.Millisecond)
	competitor.Stop()
	require.Eventually(t, func() bool {
		_, holderErr := advisoryLockHolderPID(ctx, observerDB, lockID)
		return holderErr == sql.ErrNoRows
	}, 5*time.Second, 10*time.Millisecond, "Stop must leave no advisory-lock holder")
}

type quotaRecoveryCycleProbeRepository struct {
	AccountRepository
	count atomic.Int64
}

func (r *quotaRecoveryCycleProbeRepository) QuotaRecoveryCandidateUpperBound(context.Context, time.Time) (int64, error) {
	r.count.Add(1)
	return 0, nil
}

func (*quotaRecoveryCycleProbeRepository) ListQuotaRecoveryCandidates(
	context.Context,
	time.Time,
	int64,
	int64,
	int,
) ([]Account, error) {
	return nil, nil
}

func (*quotaRecoveryCycleProbeRepository) ClearRateLimitIfUnchanged(
	context.Context,
	QuotaRecoveryObservation,
) (bool, error) {
	return false, nil
}

type quotaRecoveryIntegrationChecker struct{}

func (quotaRecoveryIntegrationChecker) Check(context.Context, *Account) QuotaRecoveryCheckResult {
	return QuotaRecoveryCheckResult{Verdict: QuotaRecoveryUnknown, CheckedAt: time.Now().UTC()}
}

func quotaRecoveryIntegrationConfig() *config.Config {
	return &config.Config{
		RunMode: config.RunModeSimple,
		QuotaRecovery: config.QuotaRecoveryConfig{
			Enabled:         true,
			IntervalSeconds: 600,
			BatchSize:       50,
			Concurrency:     1,
			TimeoutSeconds:  75,
			JitterSeconds:   0,
		},
	}
}

func advisoryLockHolderPID(ctx context.Context, db *sql.DB, lockID int64) (int, error) {
	unsignedLockID := uint64(lockID)
	classID := int64(uint32(unsignedLockID >> 32))
	objectID := int64(uint32(unsignedLockID))
	var pid int
	err := db.QueryRowContext(
		ctx,
		`SELECT pid
		 FROM pg_locks
		 WHERE locktype = 'advisory'
		   AND classid = $1::oid
		   AND objid = $2::oid
		   AND objsubid = 1
		   AND granted`,
		classID,
		objectID,
	).Scan(&pid)
	return pid, err
}
