//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQuotaRecoveryStatusDisabledAndEffectiveConfig(t *testing.T) {
	cfg := quotaRecoveryTestConfig()
	cfg.QuotaRecovery.Enabled = false
	svc := newQuotaRecoveryService(nil, nil, nil, cfg)

	status := svc.GetStatus()
	require.False(t, status.Enabled)
	require.Equal(t, "disabled", status.Status)
	require.False(t, status.Healthy)
	require.Equal(t, "stopped", status.LifecycleState)
	require.False(t, status.LeaseHeld)
	require.Equal(t, cfg.QuotaRecovery.IntervalSeconds, status.Config.IntervalSeconds)
	require.Equal(t, cfg.QuotaRecovery.BatchSize, status.Config.BatchSize)
	require.Equal(t, cfg.QuotaRecovery.Concurrency, status.Config.Concurrency)
	require.Equal(t, cfg.QuotaRecovery.TimeoutSeconds, status.Config.TimeoutSeconds)
	require.Nil(t, status.CurrentRun)
	require.Nil(t, status.LastRun)
}
func TestQuotaRecoveryStatusRunningFirstRunAndCancellationPreserveHistory(t *testing.T) {
	now := time.Date(2026, 8, 12, 1, 2, 3, 0, time.UTC)
	svc := newQuotaRecoveryService(nil, nil, nil, quotaRecoveryTestConfig())
	svc.now = func() time.Time { return now }
	svc.markLeaseAcquired(now)
	svc.setNextRunAt(now.Add(time.Hour))
	svc.markLeaseLost(now.Add(time.Minute))

	status := svc.GetStatus()
	require.Nil(t, status.NextRunAt, "a lost lease must not expose a stale scheduled time")
	require.Equal(t, "reacquiring", status.LifecycleState)
	require.False(t, status.LeaseHeld)
	require.False(t, status.LeaseHealthy)

	reacquiredAt := now.Add(2 * time.Minute)
	svc.markLeaseReacquired(reacquiredAt)
	status = svc.GetStatus()
	require.True(t, status.LeaseHeld)
	require.True(t, status.LeaseHealthy)
	require.Equal(t, "unknown", status.Status)
	require.True(t, status.Healthy, "a running lease is healthy while waiting for the first cycle")
	require.Equal(t, "running", status.LifecycleState)
	require.True(t, status.LeaseHeld)
	require.True(t, status.LeaseHealthy)
	require.Equal(t, reacquiredAt, status.ObservedAt)
	require.Nil(t, status.LastRun)

	svc.beginRunStatus("scheduled", now, true)
	svc.finishRunStatus(QuotaRecoveryRunResult{
		Listed: 10, Checked: 8, Recovered: 2, Exhausted: 3, Unknown: 1, Skipped: 2, CASMisses: 1, Errors: 1,
	}, nil)
	status = svc.GetStatus()
	require.NotNil(t, status.LastRun)
	require.Equal(t, 10, status.LastRun.Listed)
	require.Equal(t, 2, status.LastRun.Recovered)
	require.Equal(t, 1, status.LastRun.Errors)
	require.Equal(t, "warning", status.Status)
	require.True(t, status.Healthy)

	previous := *status.LastRun
	svc.beginRunStatus("scheduled", now, true)
	svc.finishRunStatus(QuotaRecoveryRunResult{Listed: 99, Recovered: 99}, context.Canceled)
	status = svc.GetStatus()
	require.Nil(t, status.CurrentRun)
	require.Equal(t, previous, *status.LastRun, "cancellation must not publish partial counters as a completed cycle")
}

func TestQuotaRecoveryStatusHeartbeatAndErrorsAreRedacted(t *testing.T) {
	first := time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC)
	second := first.Add(30 * time.Second)
	svc := newQuotaRecoveryService(nil, nil, nil, quotaRecoveryTestConfig())
	svc.markLeaseAcquired(first)
	require.Equal(t, first, svc.GetStatus().ObservedAt)
	svc.touchStatus(second)
	require.Equal(t, second, svc.GetStatus().ObservedAt)

	svc.beginRunStatus("scheduled", second, true)
	svc.finishRunStatus(QuotaRecoveryRunResult{}, fmt.Errorf("account_id=123 upstream secret=do-not-expose: %w", errors.New("provider body")))
	status := svc.GetStatus()
	require.Equal(t, "reconciliation failed", status.LastRun.LastError)
	payload, err := json.Marshal(status)
	require.NoError(t, err)
	// Assert redaction of the exact leak fragments. A bare "123" assertion
	// would be over-broad: wall-clock timestamps in the payload can contain
	// the digit sequence "123" (e.g. nanoseconds), spuriously failing.
	require.NotContains(t, string(payload), "account_id=123")
	require.NotContains(t, string(payload), "do-not-expose")
}
