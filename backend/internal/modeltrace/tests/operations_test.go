package modeltrace_test

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/modeltrace"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestModelTraceOperationalSignals(t *testing.T) {
	t.Run("successful export", func(t *testing.T) {
		stats := modeltrace.TestingNewExportStats(modeltrace.ConfigSourceRuntime, 7)
		stats.AddEnded(2)
		exporter := modeltrace.TestingFailOpenExporter(benchmarkDiscardExporter{}, stats)
		require.NoError(t, exporter.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{nil, nil}))
		require.Equal(t, modeltrace.TestingExportStatsSnapshot{Ended: 2, Attempted: 2, Exported: 2}, stats.Snapshot())
	})

	t.Run("failed export", func(t *testing.T) {
		stats := modeltrace.TestingNewExportStats(modeltrace.ConfigSourceRuntime, 8)
		stats.AddEnded(3)
		exporter := modeltrace.TestingFailOpenExporter(errorSpanExporter{}, stats)
		require.Error(t, exporter.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{nil, nil}))
		require.Equal(t, modeltrace.TestingExportStatsSnapshot{Ended: 3, Attempted: 2, Failed: 2, PendingOrDropped: 1, FailedOther: 2}, stats.Snapshot())
	})

	t.Run("panic is counted without escaping", func(t *testing.T) {
		stats := modeltrace.TestingNewExportStats(modeltrace.ConfigSourceRuntime, 9)
		stats.AddEnded(1)
		exporter := modeltrace.TestingFailOpenExporter(panicSpanExporter{}, stats)
		require.Error(t, exporter.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{nil}))
		require.Equal(t, modeltrace.TestingExportStatsSnapshot{Ended: 1, Attempted: 1, Failed: 1, Panics: 1}, stats.Snapshot())
	})
}

func TestModelTraceRolloutDisabledByDefault(t *testing.T) {
	manager, err := modeltrace.NewManager(context.Background(), config.ModelTracingConfig{})
	require.NoError(t, err)
	require.False(t, manager.Enabled())
	snapshot := manager.Acquire()
	require.Equal(t, modeltrace.ConfigSourceDeployment, snapshot.Source())
	snapshot.Release()
	shutdownManager(t, manager)
}

func TestModelTraceRollbackSwitch(t *testing.T) {
	target := newFakeOTLPServer(t)
	manager := newAsyncTestManager(t, target.server.URL)
	require.True(t, manager.Enabled())

	require.NoError(t, manager.ApplySnapshot(context.Background(), modeltrace.ConfigSnapshot{
		Config: config.ModelTracingConfig{
			Enabled: false, PromptMaxBytes: modeltrace.TestingDefaultPromptBytes,
			ResponseMaxBytes: modeltrace.TestingDefaultResponseBytes, MediaMaxBytes: modeltrace.TestingDefaultMediaBytes,
		},
		Source: modeltrace.ConfigSourceRuntime, ConfigVersion: 1,
	}))
	require.False(t, manager.Enabled())
	shutdownManager(t, manager)
}
