package modeltrace

import (
	"context"
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
