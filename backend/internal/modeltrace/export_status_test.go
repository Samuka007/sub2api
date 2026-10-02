package modeltrace

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestCanonicalGenerationConfigUsesEffectiveDefaultsAndNormalization(t *testing.T) {
	defaults := canonicalGenerationConfig(config.ModelTracingConfig{
		Enabled:   true,
		Endpoint:  " https://langfuse.example.test ",
		PublicKey: "public",
		SecretKey: "secret",
	})
	explicit := config.ModelTracingConfig{
		Enabled:              true,
		Destination:          config.ModelTracingDestinationLangfuse,
		Endpoint:             "https://langfuse.example.test/api/public/otel/v1/traces",
		PublicKey:            "public",
		SecretKey:            "secret",
		PromptMaxBytes:       defaultPromptBytes,
		ResponseMaxBytes:     defaultResponseBytes,
		MediaMaxBytes:        defaultMediaBytes,
		ExportTimeoutSeconds: int(defaultExportTimeout.Seconds()),
		ExportRetry: config.ModelTracingExportRetryConfig{
			Enabled:                true,
			InitialIntervalSeconds: int(defaultRetryInitial.Seconds()),
			MaxIntervalSeconds:     int(defaultRetryMaxInterval.Seconds()),
			MaxElapsedTimeSeconds:  int(defaultRetryMaxElapsed.Seconds()),
		},
		ExportQueueSize:      defaultMaxQueueSize,
		ExportBatchSize:      defaultMaxExportBatch,
		ExportBatchTimeoutMs: int(defaultBatchTimeout.Milliseconds()),
	}

	require.Equal(t, explicit, defaults)
	require.Equal(t,
		generationFingerprint(explicit, ConfigSourceRuntime, 7),
		generationFingerprint(defaults, ConfigSourceRuntime, 7),
	)
}

type exportStatusTestExporter struct{ err error }

func (e exportStatusTestExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return e.err
}

func (exportStatusTestExporter) Shutdown(context.Context) error { return nil }

type exportStatusPanicExporter struct{}

func (exportStatusPanicExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	panic("test exporter panic")
}

func (exportStatusPanicExporter) Shutdown(context.Context) error { return nil }

func TestManagerExportStatusReadsOnlyActiveGeneration(t *testing.T) {
	oldStats := &exportStats{}
	oldStats.endedSpans.Add(9)
	activeStats := &exportStats{}
	activeStats.endedSpans.Add(5)
	activeStats.attemptedSpans.Add(4)
	activeStats.exportedSpans.Add(3)
	activeStats.failedSpans.Add(1)
	activeStats.failedTimeout.Add(1)

	provider := sdktrace.NewTracerProvider()
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	manager := &Manager{active: &generation{
		cfg:      config.ModelTracingConfig{Enabled: true},
		provider: provider,
		stats:    activeStats,
	}}
	status := manager.ExportStatus()
	require.Equal(t, ExportStatus{
		Enabled: true, EndedSpans: 5, AttemptedSpans: 4, ExportedSpans: 3,
		FailedSpans: 1, FailedTimeout: 1,
	}, status)
	require.NotEqual(t, oldStats.snapshot().Ended, status.EndedSpans)
}

func TestDisabledGenerationExportStatusHasNoStaleCounts(t *testing.T) {
	manager := &Manager{active: &generation{cfg: config.ModelTracingConfig{}}}
	require.Equal(t, ExportStatus{}, manager.ExportStatus())
}

func TestManagerExportStatusResetsWhenActiveGenerationChanges(t *testing.T) {
	provider := sdktrace.NewTracerProvider()
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	oldStats := &exportStats{}
	oldStats.exportedSpans.Add(7)
	manager := &Manager{active: &generation{provider: provider, stats: oldStats}}
	require.Equal(t, uint64(7), manager.ExportStatus().ExportedSpans)

	manager.mu.Lock()
	manager.active = &generation{provider: provider, stats: &exportStats{}}
	manager.mu.Unlock()
	require.Zero(t, manager.ExportStatus().ExportedSpans)
}

func TestManagerExportTotalsSurviveActiveGenerationChanges(t *testing.T) {
	provider := sdktrace.NewTracerProvider()
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	totals := &exportEventCounts{}
	totals.endedSpans.Add(14)
	totals.attemptedSpans.Add(12)
	totals.exportedSpans.Add(9)
	totals.failedSpans.Add(3)
	totals.failedTimeout.Add(2)
	totals.failedOther.Add(1)

	manager := &Manager{
		active: &generation{provider: provider, stats: &exportStats{}},
		totals: totals,
	}
	require.Equal(t, ExportTotals{
		EndedSpans:     14,
		AttemptedSpans: 12,
		ExportedSpans:  9,
		FailedSpans:    3,
		FailedTimeout:  2,
		FailedOther:    1,
	}, manager.ExportTotals())

	manager.mu.Lock()
	manager.active = &generation{provider: provider, stats: &exportStats{}}
	manager.mu.Unlock()
	require.Equal(t, uint64(12), manager.ExportTotals().AttemptedSpans)
	require.Equal(t, uint64(9), manager.ExportTotals().ExportedSpans)
	require.Equal(t, uint64(3), manager.ExportTotals().FailedSpans)
}

func TestManagerExportTotalsTrackRealOutcomesAcrossGenerations(t *testing.T) {
	totals := &exportEventCounts{}
	firstStats := &exportStats{totals: totals}
	first := failOpenExporter{delegate: exportStatusTestExporter{}, stats: firstStats}
	require.NoError(t, first.ExportSpans(context.Background(), make([]sdktrace.ReadOnlySpan, 3)))

	secondStats := &exportStats{totals: totals}
	second := failOpenExporter{delegate: exportStatusTestExporter{err: errors.New("timeout")}, stats: secondStats}
	require.Error(t, second.ExportSpans(context.Background(), make([]sdktrace.ReadOnlySpan, 2)))

	manager := &Manager{totals: totals}
	require.Equal(t, ExportTotals{
		AttemptedSpans: 5,
		ExportedSpans:  3,
		FailedSpans:    2,
		FailedTimeout:  2,
	}, manager.ExportTotals())
	require.Equal(t, uint64(3), firstStats.snapshot().Exported)
	require.Equal(t, uint64(2), secondStats.snapshot().Failed)
	require.Equal(t, uint64(2), secondStats.snapshot().FailedTimeout)
}

func TestManagerExportTotalsIncludeRetiredGenerationTail(t *testing.T) {
	provider := sdktrace.NewTracerProvider()
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	totals := &exportEventCounts{}
	retiredStats := &exportStats{totals: totals}
	activeStats := &exportStats{totals: totals}
	manager := &Manager{
		active: &generation{provider: provider, stats: activeStats},
		totals: totals,
	}

	retiredExporter := failOpenExporter{delegate: exportStatusTestExporter{}, stats: retiredStats}
	require.NoError(t, retiredExporter.ExportSpans(context.Background(), make([]sdktrace.ReadOnlySpan, 4)))

	require.Equal(t, uint64(4), manager.ExportTotals().ExportedSpans)
	require.Zero(t, manager.ExportStatus().ExportedSpans)
}

func TestManagerExportTotalsKeepPanicBatchSeparateFromReasonSpans(t *testing.T) {
	totals := &exportEventCounts{}
	stats := &exportStats{totals: totals}
	exporter := failOpenExporter{delegate: exportStatusPanicExporter{}, stats: stats}

	require.Error(t, exporter.ExportSpans(context.Background(), make([]sdktrace.ReadOnlySpan, 4)))

	require.Equal(t, ExportTotals{
		AttemptedSpans: 4,
		FailedSpans:    4,
		Panics:         1,
	}, (&Manager{totals: totals}).ExportTotals())
	require.Equal(t, uint64(4), stats.snapshot().Failed)
	require.Equal(t, uint64(1), stats.snapshot().Panics)
	require.Zero(t, stats.snapshot().FailedOther)
}

func TestManagerExportTotalsAreExactUnderConcurrentGenerations(t *testing.T) {
	const workers = 64
	totals := &exportEventCounts{}
	successStats := &exportStats{totals: totals}
	failureStats := &exportStats{totals: totals}
	successExporter := failOpenExporter{delegate: exportStatusTestExporter{}, stats: successStats}
	failureExporter := failOpenExporter{delegate: exportStatusTestExporter{err: errors.New("timeout")}, stats: failureStats}

	var wg sync.WaitGroup
	results := make(chan error, workers*2)
	wg.Add(workers * 2)
	for range workers {
		go func() {
			defer wg.Done()
			results <- successExporter.ExportSpans(context.Background(), make([]sdktrace.ReadOnlySpan, 3))
		}()
		go func() {
			defer wg.Done()
			if err := failureExporter.ExportSpans(context.Background(), make([]sdktrace.ReadOnlySpan, 2)); err == nil {
				results <- errors.New("failure exporter returned nil")
				return
			}
			results <- nil
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}

	require.Equal(t, ExportTotals{
		AttemptedSpans: workers * 5,
		ExportedSpans:  workers * 3,
		FailedSpans:    workers * 2,
		FailedTimeout:  workers * 2,
	}, (&Manager{totals: totals}).ExportTotals())
}
