package main

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/appmetrics"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/modeltrace"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestApplicationActivateMetricsBindFailureDoesNotStartOpsAndCleanupIsIdempotent(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, occupied.Close()) })
	addr, ok := occupied.Addr().(*net.TCPAddr)
	require.True(t, ok)
	port := addr.Port

	metrics, err := appmetrics.New(config.MetricsConfig{
		Enabled: true,
		Host:    "127.0.0.1",
		Port:    port,
	}, nil, nil)
	require.NoError(t, err)
	ops := &service.OpsMetricsCollector{}
	var cleanupCalls atomic.Int32
	var modelTraceShutdownCalls atomic.Int32
	generation := modeltrace.TestingNewGeneration(modeltrace.TestingGenerationConfig{
		Shutdown: func(context.Context) error {
			modelTraceShutdownCalls.Add(1)
			return nil
		},
	})
	runtime := modeltrace.TestingNewManagerWithActive(generation)
	app := &Application{
		Metrics:    metrics,
		OpsMetrics: ops,
		ModelTrace: runtime,
		Cleanup:    func() { cleanupCalls.Add(1) },
	}

	require.Error(t, app.activate())
	require.False(t, ops.Running())

	app.cleanup()
	app.cleanup()
	require.Equal(t, int32(1), cleanupCalls.Load())
	require.Equal(t, int32(1), modelTraceShutdownCalls.Load())
	require.False(t, ops.Running())
	require.Nil(t, metrics.Addr())
}
