package handler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSubmitForwardUsageOnErrorRunsPartialUsageOnceThroughStoppedPoolFallback(t *testing.T) {
	h := &GatewayHandler{usageRecordWorkerPool: newStoppedUsageRecordPoolForTest()}
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	result := &service.ForwardResult{RequestID: "partial-request"}
	var submitted atomic.Int32
	var recorded atomic.Int32

	handled := submitForwardUsageOnError(result, errors.New("stream interrupted"), func(got *service.ForwardResult) {
		submitted.Add(1)
		require.Same(t, result, got)
		h.submitUsageRecordTask(parent, func(ctx context.Context) {
			require.NoError(t, ctx.Err())
			recorded.Add(1)
		})
	})

	require.True(t, handled)
	require.EqualValues(t, 1, submitted.Load())
	require.EqualValues(t, 1, recorded.Load())
}

func TestSubmitForwardUsageOnErrorIgnoresNilResultAndSuccess(t *testing.T) {
	var submitted atomic.Int32
	submit := func(*service.ForwardResult) { submitted.Add(1) }

	require.True(t, submitForwardUsageOnError(nil, errors.New("stream interrupted"), submit))
	require.False(t, submitForwardUsageOnError(&service.ForwardResult{}, nil, submit))
	require.Zero(t, submitted.Load())
}
