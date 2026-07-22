//go:build integration

package service

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"testing"
	"time"

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
