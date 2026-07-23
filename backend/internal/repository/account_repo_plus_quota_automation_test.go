package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestPlusQuotaAutomationExtraKeysAreSchedulerNeutral(t *testing.T) {
	t.Parallel()

	require.True(t, isSchedulerNeutralExtraKey(service.PlusQuotaAnomalyExtraKey))
	require.True(t, isSchedulerNeutralExtraKey(service.PlusQuotaLastResetExtraKey))
	require.True(t, isSchedulerNeutralExtraKey(service.PlusQuotaResetAttemptExtraKey))
	require.False(t, shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{
		service.PlusQuotaAnomalyExtraKey:      map[string]any{"status": service.PlusQuotaAnomalyStatusOpen},
		service.PlusQuotaLastResetExtraKey:    "2026-07-23T12:00:00Z",
		service.PlusQuotaResetAttemptExtraKey: map[string]any{"status": "attempting"},
	}))
}
