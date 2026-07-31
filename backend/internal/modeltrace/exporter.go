// Package modeltrace exports model gateway traces to self-hosted Langfuse via
// OTLP/HTTP.
package modeltrace

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	serviceName          = "sub2api"
	tracerName           = "github.com/Wei-Shaw/sub2api/internal/modeltrace"
	langfuseIngestionHdr = "x-langfuse-ingestion-version"
	defaultPromptBytes   = 1 << 20
	defaultResponseBytes = 1 << 20
	defaultMediaBytes    = 1 << 20
	// defaultExportTimeout is the single-batch OTLP export deadline including the
	// OTLP exporter's internal retry loop. It must cover the full retry window so
	// a transiently refused Collector is retried instead of discarded. <=0 config
	// values fall back to this.
	defaultExportTimeout    = 60 * time.Second
	defaultRetryInitial     = 5 * time.Second
	defaultRetryMaxInterval = 30 * time.Second
	defaultRetryMaxElapsed  = 55 * time.Second
	maxCaptureBytes         = 8 << 20
	defaultMaxQueueSize     = 256
	defaultMaxExportBatch   = 16
	defaultBatchTimeout     = 1000 * time.Millisecond
	exportHealthLogInterval = time.Minute
)

type exportStats struct {
	endedSpans     atomic.Uint64
	attemptedSpans atomic.Uint64
	exportedSpans  atomic.Uint64
	failedSpans    atomic.Uint64
	panicCount     atomic.Uint64
	// failedByReason records the last terminal export failure per reason so
	// operators can distinguish invalid_utf8, collector_refused, timeout,
	// queue_full and other errors without re-logging upstream content.
	failedInvalidUTF8      atomic.Uint64
	failedCollectorRefused atomic.Uint64
	failedTimeout          atomic.Uint64
	failedQueueFull        atomic.Uint64
	failedOther            atomic.Uint64
	lastLogNanos           atomic.Int64
	source                 string
	version                int64
}

type exportStatsSnapshot struct {
	Ended, Attempted, Exported, Failed, Panics, PendingOrDropped                           uint64
	FailedInvalidUTF8, FailedCollectorRefused, FailedTimeout, FailedQueueFull, FailedOther uint64
}

func (s *exportStats) snapshot() exportStatsSnapshot {
	if s == nil {
		return exportStatsSnapshot{}
	}
	result := exportStatsSnapshot{
		Ended: s.endedSpans.Load(), Attempted: s.attemptedSpans.Load(),
		Exported: s.exportedSpans.Load(), Failed: s.failedSpans.Load(), Panics: s.panicCount.Load(),
		FailedInvalidUTF8: s.failedInvalidUTF8.Load(), FailedCollectorRefused: s.failedCollectorRefused.Load(),
		FailedTimeout: s.failedTimeout.Load(), FailedQueueFull: s.failedQueueFull.Load(),
		FailedOther: s.failedOther.Load(),
	}
	if result.Ended > result.Attempted {
		result.PendingOrDropped = result.Ended - result.Attempted
	}
	return result
}

func (s *exportStats) log(result string, force bool) {
	if s == nil {
		return
	}
	now := time.Now().UnixNano()
	last := s.lastLogNanos.Load()
	if !force && last != 0 && time.Duration(now-last) < exportHealthLogInterval {
		return
	}
	if !s.lastLogNanos.CompareAndSwap(last, now) && !force {
		return
	}
	stats := s.snapshot()
	slog.Info("model trace export health",
		"result", result, "source", s.source, "config_version", s.version,
		"ended_spans", stats.Ended, "attempted_spans", stats.Attempted,
		"exported_spans", stats.Exported, "failed_spans", stats.Failed,
		"export_panics", stats.Panics, "pending_or_dropped_spans", stats.PendingOrDropped,
		"failed_invalid_utf8", stats.FailedInvalidUTF8,
		"failed_collector_refused", stats.FailedCollectorRefused,
		"failed_timeout", stats.FailedTimeout,
		"failed_queue_full", stats.FailedQueueFull,
		"failed_other", stats.FailedOther,
	)
}

// classifyExportError maps a terminal OTLP export error to a coarse reason
// bucket for observability. It intentionally inspects only error text markers
// that contain no captured content or credentials; the upstream error string
// is never logged and its body is not propagated onto span fields.
const (
	exportReasonInvalidUTF8      = "invalid_utf8"
	exportReasonCollectorRefused = "collector_refused"
	exportReasonTimeout          = "timeout"
	exportReasonQueueFull        = "queue_full"
	exportReasonOther            = "other"
	retryableNetworkErrorMarker  = "modeltrace_retryable_network_error"
)

func classifyExportError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, retryableNetworkErrorMarker):
		return exportReasonTimeout
	case strings.Contains(msg, "invalid utf") || strings.Contains(msg, "invalidutf") || strings.Contains(msg, "malformed") || strings.Contains(msg, "rune error"):
		return exportReasonInvalidUTF8
	case strings.Contains(msg, "refused") || strings.Contains(msg, "high memory") || strings.Contains(msg, "resource exhausted") || strings.Contains(msg, "too many requests") || strings.Contains(msg, "throttl") || strings.Contains(msg, "unavailable") || strings.Contains(msg, "503") || strings.Contains(msg, "429"):
		return exportReasonCollectorRefused
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded") || strings.Contains(msg, "context deadline") || strings.Contains(msg, "context canceled") || strings.Contains(msg, "connection refused") || strings.Contains(msg, "no such host"):
		return exportReasonTimeout
	case strings.Contains(msg, "queue") && strings.Contains(msg, "full"):
		return exportReasonQueueFull
	default:
		return exportReasonOther
	}
}

// retryableNetworkTransport converts transient RoundTrip failures into a
// synthetic 503 response. otlptracehttp v1.37 retries retryable HTTP statuses,
// but it only retries a transport error when url.Error.Temporary reports true;
// connection-refused and abrupt Collector restarts no longer satisfy that
// deprecated predicate on current Go releases.
type retryableNetworkTransport struct {
	base http.RoundTripper
}

func (t *retryableNetworkTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err == nil || request.Context().Err() != nil || !isRetryableNetworkError(err) {
		return response, err
	}
	return &http.Response{
		StatusCode:    http.StatusServiceUnavailable,
		Status:        "503 Service Unavailable",
		Header:        http.Header{},
		Body:          io.NopCloser(strings.NewReader(retryableNetworkErrorMarker)),
		ContentLength: int64(len(retryableNetworkErrorMarker)),
		Request:       request,
	}, nil
}

func (t *retryableNetworkTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func isRetryableNetworkError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var operationError *net.OpError
	if errors.As(err, &operationError) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func newRetryableOTLPHTTPClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{
		Transport: &retryableNetworkTransport{base: transport},
		Timeout:   timeout,
	}
}

func (s *exportStats) recordFailure(reason string, n uint64) {
	if s == nil || n == 0 {
		return
	}
	switch reason {
	case exportReasonInvalidUTF8:
		s.failedInvalidUTF8.Add(n)
	case exportReasonCollectorRefused:
		s.failedCollectorRefused.Add(n)
	case exportReasonTimeout:
		s.failedTimeout.Add(n)
	case exportReasonQueueFull:
		s.failedQueueFull.Add(n)
	default:
		s.failedOther.Add(n)
	}
}

type failOpenExporter struct {
	delegate sdktrace.SpanExporter
	stats    *exportStats
}

func (e failOpenExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) (err error) {
	if e.delegate == nil {
		return nil
	}
	if e.stats != nil {
		e.stats.attemptedSpans.Add(uint64(len(spans)))
	}
	defer func() {
		if recover() != nil {
			if e.stats != nil {
				e.stats.failedSpans.Add(uint64(len(spans)))
				e.stats.panicCount.Add(1)
				e.stats.log("panic", false)
			}
			err = errors.New("modeltrace: exporter panic")
		}
	}()
	err = e.delegate.ExportSpans(ctx, spans)
	if e.stats != nil {
		if err != nil {
			e.stats.failedSpans.Add(uint64(len(spans)))
			e.stats.recordFailure(classifyExportError(err), uint64(len(spans)))
			// Log only the classified reason and counts. The upstream error may
			// embed the endpoint, request URL or captured content; never log it.
			slog.Warn("model trace export failed",
				"source", e.stats.source, "config_version", e.stats.version,
				"reason", classifyExportError(err), "spans", len(spans),
				"failed_spans", e.stats.failedSpans.Load(),
			)
			e.stats.log("failure", false)
		} else {
			e.stats.exportedSpans.Add(uint64(len(spans)))
			e.stats.log("success", false)
		}
	}
	return err
}

func (e failOpenExporter) Shutdown(ctx context.Context) (err error) {
	if e.delegate == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			err = errors.New("modeltrace: exporter shutdown panic")
		}
	}()
	return e.delegate.Shutdown(ctx)
}

type monitoredSpanProcessor struct {
	delegate sdktrace.SpanProcessor
	stats    *exportStats
}

func (p *monitoredSpanProcessor) OnStart(ctx context.Context, span sdktrace.ReadWriteSpan) {
	p.delegate.OnStart(ctx, span)
}

func (p *monitoredSpanProcessor) OnEnd(span sdktrace.ReadOnlySpan) {
	if p.stats != nil {
		p.stats.endedSpans.Add(1)
	}
	p.delegate.OnEnd(span)
}

func (p *monitoredSpanProcessor) Shutdown(ctx context.Context) error {
	err := p.delegate.Shutdown(ctx)
	p.stats.log("shutdown", true)
	return err
}

func (p *monitoredSpanProcessor) ForceFlush(ctx context.Context) error {
	err := p.delegate.ForceFlush(ctx)
	p.stats.log("flush", true)
	return err
}

type generation struct {
	cfg                  config.ModelTracingConfig
	source               string
	version              int64
	fingerprint          string
	allowSourceDowngrade bool
	provider             *sdktrace.TracerProvider
	tracer               trace.Tracer
	shutdown             func(context.Context) error
	exportTimeout        time.Duration
	exportRetry          config.ModelTracingExportRetryConfig
	stats                *exportStats
	refs                 sync.WaitGroup
	closeOnce            sync.Once
}

func (g *generation) enabled() bool { return g != nil && g.provider != nil }

func (g *generation) close(ctx context.Context) error {
	if g == nil || g.shutdown == nil {
		return nil
	}
	var err error
	g.closeOnce.Do(func() { err = g.shutdown(ctx) })
	return err
}

// GenerationSnapshot pins one immutable exporter/config generation until Release.
// Callers must release it exactly once after the request or WebSocket turn ends.
type GenerationSnapshot struct {
	g       *generation
	release sync.Once
}

func (s *GenerationSnapshot) Enabled() bool { return s != nil && s.g.enabled() }
func (s *GenerationSnapshot) Config() config.ModelTracingConfig {
	if s == nil || s.g == nil {
		return config.ModelTracingConfig{}
	}
	return s.g.cfg
}
func (s *GenerationSnapshot) Tracer() trace.Tracer {
	if s == nil || s.g == nil || s.g.tracer == nil {
		return otel.GetTracerProvider().Tracer(tracerName)
	}
	return s.g.tracer
}
func (s *GenerationSnapshot) Fingerprint() string {
	if s == nil || s.g == nil {
		return ""
	}
	return s.g.fingerprint
}
func (s *GenerationSnapshot) Source() string {
	if s == nil || s.g == nil {
		return ConfigSourceDisabled
	}
	return s.g.source
}
func (s *GenerationSnapshot) Version() int64 {
	if s == nil || s.g == nil {
		return 0
	}
	return s.g.version
}
func (s *GenerationSnapshot) Release() {
	if s == nil || s.g == nil {
		return
	}
	s.release.Do(s.g.refs.Done)
}

// Retain creates another reference while this snapshot is still held.
func (s *GenerationSnapshot) Retain() *GenerationSnapshot {
	if s == nil || s.g == nil {
		return &GenerationSnapshot{}
	}
	s.g.refs.Add(1)
	return &GenerationSnapshot{g: s.g}
}

// Manager is a stable facade over atomically replaceable immutable generations.
type Manager struct {
	mu      sync.RWMutex
	active  *generation
	closed  bool
	retired sync.WaitGroup
}

// NewManager constructs a Manager. Disabled or no endpoint => no-op.
func NewManager(ctx context.Context, cfg config.ModelTracingConfig) (*Manager, error) {
	g, err := buildGeneration(ctx, cfg, ConfigSourceDeployment, 0)
	if err != nil {
		return nil, err
	}
	logGenerationApplied(g)
	return &Manager{active: g}, nil
}

func buildGeneration(ctx context.Context, cfg config.ModelTracingConfig, source string, version int64) (*generation, error) {
	prompt, response, media := boundedSizes(cfg)
	cfg.PromptMaxBytes = prompt
	cfg.ResponseMaxBytes = response
	cfg.MediaMaxBytes = media
	exportTimeout, retry, queueSize, batchSize, batchTimeout := resolveExportSettings(cfg)
	cfg.ExportTimeoutSeconds = int(exportTimeout.Seconds())
	cfg.ExportRetry = retry
	cfg.ExportQueueSize = queueSize
	cfg.ExportBatchSize = batchSize
	cfg.ExportBatchTimeoutMs = int(batchTimeout.Milliseconds())
	g := &generation{cfg: cfg, source: source, version: version}
	g.fingerprint = generationFingerprint(cfg, source, version)
	if !cfg.Enabled || strings.TrimSpace(cfg.Endpoint) == "" {
		return g, nil
	}
	if err := config.ValidateModelTracingEndpoint(cfg.Endpoint); err != nil {
		return nil, fmt.Errorf("modeltrace: invalid endpoint: %w", err)
	}
	if cfg.PublicKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("modeltrace: public_key and secret_key required when enabled")
	}

	endpoint, path, insecure := splitEndpoint(cfg.Endpoint)
	headers := map[string]string{
		"Authorization":      "Basic " + base64.StdEncoding.EncodeToString([]byte(cfg.PublicKey+":"+cfg.SecretKey)),
		langfuseIngestionHdr: "4",
	}
	exporterOpts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithURLPath(path),
		otlptracehttp.WithHeaders(headers),
		otlptracehttp.WithTimeout(exportTimeout),
		otlptracehttp.WithHTTPClient(newRetryableOTLPHTTPClient(exportTimeout)),
	}
	exporterOpts = append(exporterOpts, otlptracehttp.WithRetry(otlptracehttp.RetryConfig{
		Enabled:         retry.Enabled,
		InitialInterval: time.Duration(retry.InitialIntervalSeconds) * time.Second,
		MaxInterval:     time.Duration(retry.MaxIntervalSeconds) * time.Second,
		MaxElapsedTime:  time.Duration(retry.MaxElapsedTimeSeconds) * time.Second,
	}))
	if insecure {
		exporterOpts = append(exporterOpts, otlptracehttp.WithInsecure())
	} else {
		exporterOpts = append(exporterOpts, otlptracehttp.WithTLSClientConfig(&tls.Config{MinVersion: tls.VersionTLS12}))
	}

	exporter, err := otlptracehttp.New(ctx, exporterOpts...)
	if err != nil {
		return nil, fmt.Errorf("modeltrace: build exporter: %w", err)
	}
	res, err := resource.New(ctx, resource.WithAttributes(
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion("modeltrace-v1"),
	))
	if err != nil {
		return nil, fmt.Errorf("modeltrace: build resource: %w", err)
	}
	stats := &exportStats{source: source, version: version}
	g.stats = stats
	batchProcessor := sdktrace.NewBatchSpanProcessor(
		failOpenExporter{delegate: exporter, stats: stats},
		sdktrace.WithMaxQueueSize(queueSize),
		sdktrace.WithMaxExportBatchSize(batchSize),
		sdktrace.WithBatchTimeout(batchTimeout),
		sdktrace.WithExportTimeout(exportTimeout),
	)
	g.exportTimeout = exportTimeout
	g.exportRetry = retry
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(&monitoredSpanProcessor{delegate: batchProcessor, stats: stats}),
		sdktrace.WithResource(res),
	)
	g.provider = provider
	g.tracer = provider.Tracer(tracerName)
	g.shutdown = provider.Shutdown
	return g, nil
}

func generationFingerprint(cfg config.ModelTracingConfig, source string, version int64) string {
	payload, _ := json.Marshal(struct {
		Config  config.ModelTracingConfig `json:"config"`
		Source  string                    `json:"source"`
		Version int64                     `json:"version"`
	}{Config: cfg, Source: source, Version: version})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func boundedSizes(cfg config.ModelTracingConfig) (int, int, int) {
	p := cfg.PromptMaxBytes
	if p <= 0 {
		p = defaultPromptBytes
	}
	if p > maxCaptureBytes {
		p = maxCaptureBytes
	}
	r := cfg.ResponseMaxBytes
	if r <= 0 {
		r = defaultResponseBytes
	}
	if r > maxCaptureBytes {
		r = maxCaptureBytes
	}
	m := cfg.MediaMaxBytes
	if m <= 0 {
		m = defaultMediaBytes
	}
	if m > maxCaptureBytes {
		m = maxCaptureBytes
	}
	return p, r, m
}

// resolveExportSettings fills documented defaults for the OTLP export-timeout,
// retry backoff and BatchSpanProcessor queue/batch sizing so a transiently
// refused Collector is retried within a bounded window instead of discarded
// after one or two attempts.
func resolveExportSettings(cfg config.ModelTracingConfig) (time.Duration, config.ModelTracingExportRetryConfig, int, int, time.Duration) {
	timeout := time.Duration(cfg.ExportTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultExportTimeout
	}
	retryConfigured := cfg.ExportRetry.InitialIntervalSeconds > 0 ||
		cfg.ExportRetry.MaxIntervalSeconds > 0 ||
		cfg.ExportRetry.MaxElapsedTimeSeconds > 0 ||
		cfg.ExportRetry.Enabled
	retry := cfg.ExportRetry
	if retry.InitialIntervalSeconds <= 0 {
		retry.InitialIntervalSeconds = int(defaultRetryInitial.Seconds())
	}
	if retry.MaxIntervalSeconds <= 0 {
		retry.MaxIntervalSeconds = int(defaultRetryMaxInterval.Seconds())
	}
	if retry.MaxElapsedTimeSeconds <= 0 {
		retry.MaxElapsedTimeSeconds = int(defaultRetryMaxElapsed.Seconds())
	}
	if retry.InitialIntervalSeconds > retry.MaxIntervalSeconds {
		retry.InitialIntervalSeconds = retry.MaxIntervalSeconds
	}
	// Retry is enabled by default so transient Collector refusals recover within
	// the export-timeout window; only an explicitly disabled retry stays off.
	if !retryConfigured {
		retry.Enabled = true
	}
	maxElapsed := time.Duration(retry.MaxElapsedTimeSeconds) * time.Second
	if maxElapsed > 0 && maxElapsed >= timeout {
		// Bound retries within the export-timeout window so the SDK does not
		// start a retry that the processor will immediately cancel.
		maxElapsed = timeout - time.Second
		if maxElapsed < time.Second {
			maxElapsed = time.Second
		}
		retry.MaxElapsedTimeSeconds = int(maxElapsed.Seconds())
	}
	queueSize := cfg.ExportQueueSize
	if queueSize <= 0 {
		queueSize = defaultMaxQueueSize
	}
	batchSize := cfg.ExportBatchSize
	if batchSize <= 0 {
		batchSize = defaultMaxExportBatch
	}
	if batchSize > queueSize {
		batchSize = queueSize
	}
	batchTimeout := time.Duration(cfg.ExportBatchTimeoutMs) * time.Millisecond
	if batchTimeout <= 0 {
		batchTimeout = defaultBatchTimeout
	}
	return timeout, retry, queueSize, batchSize, batchTimeout
}

func splitEndpoint(raw string) (host string, path string, insecure bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw, "/", false
	}
	host = u.Host
	if u.Port() == "" {
		if strings.EqualFold(u.Scheme, "http") {
			host = net.JoinHostPort(u.Hostname(), "80")
		} else {
			host = net.JoinHostPort(u.Hostname(), "443")
		}
	}
	path = u.Path
	if path == "" {
		path = "/"
	}
	insecure = strings.EqualFold(u.Scheme, "http")
	return
}

// sanitizeEndpointForDisplay removes URL components that must never be exposed
// in model-tracing configuration responses. It tolerates legacy persisted
// values so administrators can inspect and replace them without revealing
// credentials or query fragments.
func sanitizeEndpointForDisplay(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Hostname() == "" {
		return ""
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}

func (m *Manager) Acquire() *GenerationSnapshot {
	if m == nil {
		return &GenerationSnapshot{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed || m.active == nil {
		return &GenerationSnapshot{}
	}
	m.active.refs.Add(1)
	return &GenerationSnapshot{g: m.active}
}

func (m *Manager) Enabled() bool {
	snapshot := m.Acquire()
	defer snapshot.Release()
	return snapshot.Enabled()
}

func (m *Manager) Tracer() trace.Tracer {
	snapshot := m.Acquire()
	defer snapshot.Release()
	return snapshot.Tracer()
}

func (m *Manager) Config() config.ModelTracingConfig {
	snapshot := m.Acquire()
	defer snapshot.Release()
	return snapshot.Config()
}

func (m *Manager) Fingerprint() string {
	snapshot := m.Acquire()
	defer snapshot.Release()
	return snapshot.Fingerprint()
}

// ApplySnapshot builds a complete generation before atomically publishing it.
// A failed build leaves the active generation untouched.
func (m *Manager) ApplySnapshot(ctx context.Context, snapshot ConfigSnapshot) error {
	if m == nil {
		return errors.New("modeltrace: manager is nil")
	}
	cfg := snapshot.Config
	cfg.PromptMaxBytes, cfg.ResponseMaxBytes, cfg.MediaMaxBytes = boundedSizes(cfg)
	if m.Fingerprint() == generationFingerprint(cfg, snapshot.Source, snapshot.ConfigVersion) {
		return nil
	}
	next, err := buildGeneration(ctx, snapshot.Config, snapshot.Source, snapshot.ConfigVersion)
	if err != nil {
		return err
	}
	next.allowSourceDowngrade = snapshot.allowSourceDowngrade
	return m.installGeneration(next)
}

func (m *Manager) installGeneration(next *generation) error {
	if m == nil || next == nil {
		return errors.New("modeltrace: generation is nil")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = next.close(context.Background())
		return errors.New("modeltrace: manager is shut down")
	}
	if m.active != nil && m.active.fingerprint == next.fingerprint {
		m.mu.Unlock()
		_ = next.close(context.Background())
		return nil
	}
	if rejectGeneration(m.active, next) {
		m.mu.Unlock()
		_ = next.close(context.Background())
		return nil
	}
	previous := m.active
	m.active = next
	m.registerRetirementLocked(previous)
	m.mu.Unlock()
	m.retireRegistered(previous)
	logGenerationApplied(next)
	return nil
}

func rejectGeneration(active, next *generation) bool {
	if active == nil || next == nil {
		return false
	}
	if active.source == ConfigSourceRuntime && next.source == ConfigSourceRuntime {
		return next.version <= active.version
	}
	return !next.allowSourceDowngrade && generationSourcePriority(next.source) < generationSourcePriority(active.source)
}

func generationSourcePriority(source string) int {
	switch source {
	case ConfigSourceRuntime:
		return 2
	case ConfigSourceDeployment:
		return 1
	default:
		return 0
	}
}

func logGenerationApplied(g *generation) {
	if g == nil {
		return
	}
	slog.Info("model trace configuration applied",
		"enabled", g.enabled(), "source", g.source, "config_version", g.version,
		"prompt_max_bytes", g.cfg.PromptMaxBytes, "response_max_bytes", g.cfg.ResponseMaxBytes,
		"media_max_bytes", g.cfg.MediaMaxBytes, "capture_media_content", g.cfg.CaptureMediaContent,
		"export_timeout_seconds", g.cfg.ExportTimeoutSeconds,
		"export_retry_enabled", g.cfg.ExportRetry.Enabled,
		"export_retry_initial_seconds", g.cfg.ExportRetry.InitialIntervalSeconds,
		"export_retry_max_interval_seconds", g.cfg.ExportRetry.MaxIntervalSeconds,
		"export_retry_max_elapsed_seconds", g.cfg.ExportRetry.MaxElapsedTimeSeconds,
		"export_queue_size", g.cfg.ExportQueueSize, "export_batch_size", g.cfg.ExportBatchSize,
		"export_batch_timeout_ms", g.cfg.ExportBatchTimeoutMs,
	)
}

func (m *Manager) registerRetirementLocked(g *generation) {
	if g != nil {
		m.retired.Add(1)
	}
}

func (m *Manager) retireRegistered(g *generation) {
	if m == nil || g == nil {
		return
	}
	go func() {
		defer m.retired.Done()
		g.refs.Wait()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = g.close(ctx)
	}()
}

func (m *Manager) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}
	var active *generation
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		active = m.active
		m.active = nil
		m.registerRetirementLocked(active)
	}
	m.mu.Unlock()
	m.retireRegistered(active)
	done := make(chan struct{})
	go func() {
		m.retired.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
