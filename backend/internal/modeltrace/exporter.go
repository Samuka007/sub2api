// Package modeltrace provides OTEL trace export for model gateway requests to a
// self-hosted Langfuse instance via OTLP/HTTP. It implements the
// add-model-request-otel-tracing OpenSpec change.
package modeltrace

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
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
	serviceName           = "sub2api"
	tracerName            = "github.com/Wei-Shaw/sub2api/internal/modeltrace"
	otlpTracesPathSuffix  = "/v1/traces"
	otlpBasePath          = "/api/public/otel"
	langfuseIngestionHdr  = "x-langfuse-ingestion-version"
	defaultPromptBytes    = 1 << 20
	defaultResponseBytes  = 1 << 20
	defaultMediaBytes     = 1 << 20
	defaultExportTimeout  = 10 * time.Second
	defaultMaxQueueSize   = 2048
	defaultMaxExportBatch = 512
	defaultBatchTimeout   = 1000 * time.Millisecond
)

type Manager struct {
	cfg       config.ModelTracingConfig
	provider  *sdktrace.TracerProvider
	tracer    trace.Tracer
	shutdown  func(context.Context) error
	closeOnce sync.Once
}

// NewManager constructs a Manager. Disabled or no endpoint => no-op.
func NewManager(ctx context.Context, cfg config.ModelTracingConfig) (*Manager, error) {
	if !cfg.Enabled || strings.TrimSpace(cfg.Endpoint) == "" {
		return &Manager{cfg: cfg}, nil
	}
	if err := ValidateEndpoint(cfg.Endpoint); err != nil {
		return nil, fmt.Errorf("modeltrace: invalid endpoint: %w", err)
	}
	if cfg.PublicKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("modeltrace: public_key and secret_key required when enabled")
	}

	prompt, response, media := boundedSizes(cfg)
	endpoint, path, insecure := splitEndpoint(cfg.Endpoint)

	headers := map[string]string{
		"Authorization":      "Basic " + base64.StdEncoding.EncodeToString([]byte(cfg.PublicKey+":"+cfg.SecretKey)),
		langfuseIngestionHdr: "4",
	}
	exporterOpts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithURLPath(path),
		otlptracehttp.WithHeaders(headers),
		otlptracehttp.WithTimeout(defaultExportTimeout),
	}
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
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithMaxQueueSize(defaultMaxQueueSize),
			sdktrace.WithMaxExportBatchSize(defaultMaxExportBatch),
			sdktrace.WithBatchTimeout(defaultBatchTimeout),
			sdktrace.WithExportTimeout(defaultExportTimeout),
		),
		sdktrace.WithResource(res),
	)
	cfg.PromptMaxBytes = prompt
	cfg.ResponseMaxBytes = response
	cfg.MediaMaxBytes = media
	return &Manager{
		cfg:      cfg,
		provider: provider,
		tracer:   provider.Tracer(tracerName),
		shutdown: provider.Shutdown,
	}, nil
}

func boundedSizes(cfg config.ModelTracingConfig) (int, int, int) {
	p := cfg.PromptMaxBytes
	if p <= 0 {
		p = defaultPromptBytes
	}
	r := cfg.ResponseMaxBytes
	if r <= 0 {
		r = defaultResponseBytes
	}
	m := cfg.MediaMaxBytes
	if m <= 0 {
		m = defaultMediaBytes
	}
	return p, r, m
}

func splitEndpoint(raw string) (host string, path string, insecure bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw, otlpBasePath + otlpTracesPathSuffix, false
	}
	host = u.Host
	if u.Port() == "" {
		if strings.EqualFold(u.Scheme, "http") {
			host = net.JoinHostPort(u.Hostname(), "80")
		} else {
			host = net.JoinHostPort(u.Hostname(), "443")
		}
	}
	path = strings.TrimRight(u.Path, "/")
	if path == "" {
		path = otlpBasePath
	}
	path = path + otlpTracesPathSuffix
	insecure = strings.EqualFold(u.Scheme, "http")
	return
}

// ValidateEndpoint: HTTPS always allowed; HTTP only for loopback.
func ValidateEndpoint(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("endpoint is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if strings.EqualFold(u.Scheme, "http") && !isLoopbackHost(host) {
		return fmt.Errorf("http endpoint must be loopback, got %q", host)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (m *Manager) Enabled() bool { return m != nil && m.provider != nil }

func (m *Manager) Tracer() trace.Tracer {
	if m == nil || m.provider == nil {
		return otel.GetTracerProvider().Tracer(tracerName)
	}
	return m.tracer
}

func (m *Manager) Config() config.ModelTracingConfig {
	if m == nil {
		return config.ModelTracingConfig{}
	}
	return m.cfg
}

func (m *Manager) Shutdown(ctx context.Context) error {
	if m == nil || m.provider == nil {
		return nil
	}
	var err error
	m.closeOnce.Do(func() {
		err = m.shutdown(ctx)
	})
	return err
}
