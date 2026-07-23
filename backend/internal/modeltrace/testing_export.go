package modeltrace

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/modeltrace/recording"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Testing-only exports for external tests in modeltrace/tests.

const (
	TestingRootSpanName                   = rootSpanName
	TestingFirstOutputMsAttribute         = firstOutputMsAttribute
	TestingMaxEntryFactBytes              = maxEntryFactBytes
	TestingTracerName                     = tracerName
	TestingDefaultPromptBytes             = defaultPromptBytes
	TestingDefaultResponseBytes           = defaultResponseBytes
	TestingDefaultMediaBytes              = defaultMediaBytes
	TestingRedactedValue                  = redactedValue
	TestingDefaultMaxQueueSize            = defaultMaxQueueSize
	TestingDefaultMaxExportBatch          = defaultMaxExportBatch
	TestingMaxCaptureBytes                = maxCaptureBytes
	TestingStreamStatusCompleted          = streamStatusCompleted
	TestingStreamStatusCancelled          = streamStatusCancelled
	TestingStreamStatusClientDisconnected = streamStatusClientDisconnected
	TestingStreamStatusAttribute          = streamStatusAttribute
	TestingStreamErrorStageAttribute      = streamErrorStageAttribute
)

var (
	TestingDefaultBatchTimeout           = defaultBatchTimeout
	TestingDefaultExportTimeout          = defaultExportTimeout
	TestingErrUpstreamResponseIncomplete = errUpstreamResponseIncomplete
)

type TestingCapturePolicy struct {
	MediaMaxBytes       int
	CaptureMediaContent bool
}

func testingCapturePolicy(p TestingCapturePolicy) capturePolicy {
	return capturePolicy{
		mediaMaxBytes:       p.MediaMaxBytes,
		captureMediaContent: p.CaptureMediaContent,
	}
}

func TestingCaptureModelContent(raw []byte, originalBytes, limit int, policy TestingCapturePolicy) string {
	return captureModelContent(raw, originalBytes, limit, testingCapturePolicy(policy))
}

func TestingCaptureModelContentWithType(raw []byte, originalBytes, limit int, contentType string, policy TestingCapturePolicy) string {
	return captureModelContentWithType(raw, originalBytes, limit, contentType, testingCapturePolicy(policy))
}

func TestingBoundedSizes(cfg config.ModelTracingConfig) (prompt, response, media int) {
	return boundedSizes(cfg)
}

func TestingSanitizeTraceError(msg string) string {
	return sanitizeTraceError(msg)
}

func TestingLangfusePublicBaseURL(endpoint string) string {
	return langfusePublicBaseURL(endpoint)
}

type TestingEntryFacts struct {
	Protocol    string
	ClientModel string
}

func TestingResolveEntryFacts(path, contentType string, body []byte) TestingEntryFacts {
	facts := resolveEntryFacts(path, contentType, body)
	return TestingEntryFacts{Protocol: facts.Protocol, ClientModel: facts.ClientModel}
}

func TestingGenerationFingerprint(cfg config.ModelTracingConfig, source string, version int64) string {
	return generationFingerprint(cfg, source, version)
}

func TestingNewTraceRecorder(
	ctx context.Context,
	tracer trace.Tracer,
	identity servermiddleware.ResolvedIdentity,
	promptMaxBytes, responseMaxBytes int,
	policy TestingCapturePolicy,
	generation *GenerationSnapshot,
) recording.Recorder {
	return newTraceRecorder(ctx, tracer, identity, promptMaxBytes, responseMaxBytes, testingCapturePolicy(policy), generation)
}

func (m *Manager) TestingInstallGeneration(next *TestingGeneration) error {
	return m.installGeneration(next.g)
}

type TestingGeneration struct {
	g *generation
}

type TestingGenerationConfig struct {
	Cfg         config.ModelTracingConfig
	Source      string
	Version     int64
	Fingerprint string
	Shutdown    func(context.Context) error
}

func TestingNewGeneration(cfg TestingGenerationConfig) *TestingGeneration {
	return &TestingGeneration{g: &generation{
		cfg:         cfg.Cfg,
		source:      cfg.Source,
		version:     cfg.Version,
		fingerprint: cfg.Fingerprint,
		shutdown:    cfg.Shutdown,
	}}
}

func (gen *TestingGeneration) BindTracerProvider(provider *sdktrace.TracerProvider) {
	gen.g.provider = provider
	gen.g.tracer = provider.Tracer(tracerName)
	gen.g.shutdown = provider.Shutdown
}

func TestingNewManagerWithActive(active *TestingGeneration) *Manager {
	return &Manager{active: active.g}
}

func TestingManagerWithTestExporter(exporter sdktrace.SpanExporter) *Manager {
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(
		failOpenExporter{delegate: exporter},
		sdktrace.WithMaxQueueSize(2),
		sdktrace.WithMaxExportBatchSize(1),
	))
	cfg := config.ModelTracingConfig{Enabled: true, PromptMaxBytes: 4096, ResponseMaxBytes: 4096, MediaMaxBytes: 4096}
	gen := &generation{
		cfg:         cfg,
		source:      ConfigSourceDeployment,
		fingerprint: generationFingerprint(cfg, ConfigSourceDeployment, 0),
		provider:    provider,
		tracer:      provider.Tracer(tracerName),
		shutdown:    provider.Shutdown,
	}
	return &Manager{active: gen}
}

type TestingExportStatsSnapshot = exportStatsSnapshot

type TestingExportStats struct {
	inner *exportStats
}

func TestingNewExportStats(source string, version int64) *TestingExportStats {
	return &TestingExportStats{inner: &exportStats{source: source, version: version}}
}

func (s *TestingExportStats) AddEnded(n int64) {
	s.inner.endedSpans.Add(uint64(n))
}

func (s *TestingExportStats) Snapshot() TestingExportStatsSnapshot {
	return s.inner.snapshot()
}

func TestingFailOpenExporter(delegate sdktrace.SpanExporter, stats *TestingExportStats) sdktrace.SpanExporter {
	var inner *exportStats
	if stats != nil {
		inner = stats.inner
	}
	return failOpenExporter{delegate: delegate, stats: inner}
}

func TestingSetConfigManagerRefreshInterval(m *ConfigManager, interval time.Duration) {
	m.refreshInterval = interval
}

func TestingResponsesWSTurnInputLen(turn *ResponsesWSTurn) int {
	if turn == nil {
		return 0
	}
	return len(turn.input)
}
