package appmetrics

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	httpPprof "net/http/pprof"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	applicationmetrics "github.com/Wei-Shaw/sub2api/internal/metrics"
	"github.com/Wei-Shaw/sub2api/internal/modeltrace"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type OpsSnapshotSource interface {
	LatestSnapshot() *service.OpsInsertSystemMetricsInput
}

type ExportStatusSource interface {
	ExportStatus() modeltrace.ExportStatus
	ExportTotals() modeltrace.ExportTotals
}

type Metrics struct {
	cfg     config.MetricsConfig
	handler http.Handler

	mu         sync.Mutex
	listener   net.Listener
	server     *http.Server
	generation uint64
	lastError  error
	serve      func(*http.Server, net.Listener) error
}

func New(cfg config.MetricsConfig, ops OpsSnapshotSource, exports ExportStatusSource) (*Metrics, error) {
	registry := prometheus.NewRegistry()
	source := applicationmetrics.NewSource()
	applicationmetrics.SetDefault(source)
	registry.MustRegister(source)
	registry.MustRegister(newApplicationStateCollector(ops, exports))

	path := cfg.Path
	if path == "" {
		path = "/metrics"
	}
	promHandler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	privateMux := http.NewServeMux()
	pprofEnabled := cfg.Enabled && cfg.PprofEnabled
	if pprofEnabled {
		profileGate := make(chan struct{}, 1)
		withProfileGate := func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case profileGate <- struct{}{}:
					defer func() { <-profileGate }()
					next.ServeHTTP(w, r)
				default:
					http.Error(w, "another profile is already running", http.StatusTooManyRequests)
				}
			})
		}
		privateMux.HandleFunc("GET /debug/pprof/{$}", httpPprof.Index)
		privateMux.HandleFunc("GET /debug/pprof/cmdline", httpPprof.Cmdline)
		privateMux.Handle("GET /debug/pprof/profile", withProfileGate(http.HandlerFunc(httpPprof.Profile)))
		privateMux.HandleFunc("GET /debug/pprof/symbol", httpPprof.Symbol)
		privateMux.HandleFunc("POST /debug/pprof/symbol", httpPprof.Symbol)
		privateMux.Handle("GET /debug/pprof/trace", withProfileGate(http.HandlerFunc(httpPprof.Trace)))
		for _, profile := range []string{"heap", "goroutine", "allocs", "block", "mutex"} {
			privateMux.Handle("GET /debug/pprof/"+profile, withProfileGate(httpPprof.Handler(profile)))
		}
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == path {
			promHandler.ServeHTTP(w, r)
			return
		}
		if pprofEnabled {
			if !isLoopbackRequest(r) {
				http.NotFound(w, r)
				return
			}
			privateMux.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
	return &Metrics{cfg: cfg, handler: handler, serve: func(server *http.Server, listener net.Listener) error {
		return server.Serve(listener)
	}}, nil
}

func isLoopbackRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (m *Metrics) Handler() http.Handler {
	if m == nil || m.handler == nil {
		return http.NotFoundHandler()
	}
	return m.handler
}

func (m *Metrics) Start() error {
	if m == nil || !m.cfg.Enabled {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listener != nil {
		return nil
	}
	host := m.cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", m.cfg.Port)))
	if err != nil {
		return fmt.Errorf("listen for metrics: %w", err)
	}
	server := &http.Server{
		Handler:           m.handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	m.generation++
	generation := m.generation
	m.listener = listener
	m.server = server
	m.lastError = nil
	serve := m.serve
	go func() {
		err := serve(server, listener)
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return
		}
		log.Printf("Metrics listener Serve failed: %v", err)
		m.mu.Lock()
		if m.generation == generation && m.listener == listener && m.server == server {
			m.listener = nil
			m.server = nil
			m.lastError = err
			_ = listener.Close()
		}
		m.mu.Unlock()
	}()
	return nil
}

func (m *Metrics) Addr() net.Addr {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listener == nil {
		return nil
	}
	return m.listener.Addr()
}

func (m *Metrics) LastError() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastError
}

func (m *Metrics) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	server := m.server
	m.server = nil
	m.listener = nil
	m.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.Shutdown(ctx)
}

type applicationStateCollector struct {
	ops     OpsSnapshotSource
	exports ExportStatusSource

	sampleTimestamp *prometheus.Desc
	databaseUp      *prometheus.Desc
	redisUp         *prometheus.Desc
	databaseConns   *prometheus.Desc
	redisConns      *prometheus.Desc
	goroutines      *prometheus.Desc
	queueDepth      *prometheus.Desc

	modeltraceEnabled        *prometheus.Desc
	modeltraceEnded          *prometheus.Desc
	modeltraceAttempted      *prometheus.Desc
	modeltraceExported       *prometheus.Desc
	modeltraceFailed         *prometheus.Desc
	modeltracePanics         *prometheus.Desc
	modeltraceFailedReasons  *prometheus.Desc
	modeltraceEndedTotal     *prometheus.Desc
	modeltraceAttemptedTotal *prometheus.Desc
	terminalTotal            *prometheus.Desc
	failureTotal             *prometheus.Desc
	panicsTotal              *prometheus.Desc
}

func newApplicationStateCollector(ops OpsSnapshotSource, exports ExportStatusSource) *applicationStateCollector {
	stateHelp := "Application-side state sample; it is not host or database-server telemetry."
	generationHelp := "Active modeltrace generation gauge; value resets when the active generation changes."
	processHelp := "Current Sub2API process counter; survives active modeltrace generation changes and resets on process restart."
	return &applicationStateCollector{
		ops: ops, exports: exports,
		sampleTimestamp:          prometheus.NewDesc("sub2api_application_state_sample_timestamp_seconds", "Timestamp of the latest successful application state sample.", nil, nil),
		databaseUp:               prometheus.NewDesc("sub2api_infra_database_up", stateHelp+" The application SELECT 1 check, 1 up and 0 down.", nil, nil),
		redisUp:                  prometheus.NewDesc("sub2api_infra_redis_up", stateHelp+" The application Redis PING check, 1 up and 0 down.", nil, nil),
		databaseConns:            prometheus.NewDesc("sub2api_infra_database_connections", stateHelp+" Application sql.DB pool connections by fixed state.", []string{"state"}, nil),
		redisConns:               prometheus.NewDesc("sub2api_infra_redis_connections", stateHelp+" Application Redis pool connections by fixed state.", []string{"state"}, nil),
		goroutines:               prometheus.NewDesc("sub2api_infra_goroutines", stateHelp+" Current application goroutine count.", nil, nil),
		queueDepth:               prometheus.NewDesc("sub2api_infra_concurrency_queue_depth", stateHelp+" Aggregate application concurrency waiting depth.", nil, nil),
		modeltraceEnabled:        prometheus.NewDesc("sub2api_modeltrace_enabled", generationHelp+" 1 when enabled, otherwise 0.", nil, nil),
		modeltraceEnded:          prometheus.NewDesc("sub2api_modeltrace_ended_spans", generationHelp+" Ended spans.", nil, nil),
		modeltraceAttempted:      prometheus.NewDesc("sub2api_modeltrace_export_attempted_spans", generationHelp+" Export-attempted spans.", nil, nil),
		modeltraceExported:       prometheus.NewDesc("sub2api_modeltrace_exported_spans", generationHelp+" Spans whose application exporter handoff returned success.", nil, nil),
		modeltraceFailed:         prometheus.NewDesc("sub2api_modeltrace_export_failed_spans", generationHelp+" Terminally failed export spans.", nil, nil),
		modeltracePanics:         prometheus.NewDesc("sub2api_modeltrace_export_panics", generationHelp+" Exporter panics.", nil, nil),
		modeltraceFailedReasons:  prometheus.NewDesc("sub2api_modeltrace_export_failed_spans_by_reason", generationHelp+" Terminally failed export spans by fixed reason.", []string{"reason"}, nil),
		modeltraceEndedTotal:     prometheus.NewDesc("sub2api_modeltrace_ended_spans_total", processHelp+" Ended spans.", nil, nil),
		modeltraceAttemptedTotal: prometheus.NewDesc("sub2api_modeltrace_export_attempted_spans_total", processHelp+" Export-attempted spans.", nil, nil),
		terminalTotal:            prometheus.NewDesc("sub2api_modeltrace_export_terminal_spans_total", processHelp+" Terminal export spans by fixed outcome.", []string{"outcome"}, nil),
		failureTotal:             prometheus.NewDesc("sub2api_modeltrace_export_failed_spans_total", processHelp+" Ordinary terminal export failures by fixed reason.", []string{"reason"}, nil),
		panicsTotal:              prometheus.NewDesc("sub2api_modeltrace_export_panics_total", processHelp+" Recovered exporter panic incidents, measured in incidents rather than spans.", nil, nil),
	}
}

func (c *applicationStateCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{
		c.sampleTimestamp, c.databaseUp, c.redisUp, c.databaseConns, c.redisConns, c.goroutines, c.queueDepth,
		c.modeltraceEnabled, c.modeltraceEnded, c.modeltraceAttempted, c.modeltraceExported, c.modeltraceFailed,
		c.modeltracePanics, c.modeltraceFailedReasons, c.modeltraceEndedTotal, c.modeltraceAttemptedTotal,
		c.terminalTotal, c.failureTotal, c.panicsTotal,
	} {
		ch <- desc
	}
}

func (c *applicationStateCollector) Collect(ch chan<- prometheus.Metric) {
	if c.ops != nil {
		if snapshot := c.ops.LatestSnapshot(); snapshot != nil {
			c.collectState(ch, snapshot)
		}
	}
	status := modeltrace.ExportStatus{}
	totals := modeltrace.ExportTotals{}
	if c.exports != nil {
		status = c.exports.ExportStatus()
		totals = c.exports.ExportTotals()
	}
	c.collectExportStatus(ch, status)
	c.collectExportTotals(ch, totals)
}

func (c *applicationStateCollector) collectState(ch chan<- prometheus.Metric, s *service.OpsInsertSystemMetricsInput) {
	ch <- prometheus.MustNewConstMetric(c.sampleTimestamp, prometheus.GaugeValue, float64(s.CreatedAt.Unix()))
	if s.DBOK != nil {
		ch <- prometheus.MustNewConstMetric(c.databaseUp, prometheus.GaugeValue, boolFloat(*s.DBOK))
	}
	if s.RedisOK != nil {
		ch <- prometheus.MustNewConstMetric(c.redisUp, prometheus.GaugeValue, boolFloat(*s.RedisOK))
	}
	if s.DBConnActive != nil {
		ch <- prometheus.MustNewConstMetric(c.databaseConns, prometheus.GaugeValue, float64(*s.DBConnActive), "active")
	}
	if s.DBConnIdle != nil {
		ch <- prometheus.MustNewConstMetric(c.databaseConns, prometheus.GaugeValue, float64(*s.DBConnIdle), "idle")
	}
	if s.RedisConnTotal != nil {
		ch <- prometheus.MustNewConstMetric(c.redisConns, prometheus.GaugeValue, float64(*s.RedisConnTotal), "total")
	}
	if s.RedisConnIdle != nil {
		ch <- prometheus.MustNewConstMetric(c.redisConns, prometheus.GaugeValue, float64(*s.RedisConnIdle), "idle")
	}
	if s.GoroutineCount != nil {
		ch <- prometheus.MustNewConstMetric(c.goroutines, prometheus.GaugeValue, float64(*s.GoroutineCount))
	}
	if s.ConcurrencyQueueDepth != nil {
		ch <- prometheus.MustNewConstMetric(c.queueDepth, prometheus.GaugeValue, float64(*s.ConcurrencyQueueDepth))
	}
}

func (c *applicationStateCollector) collectExportStatus(ch chan<- prometheus.Metric, s modeltrace.ExportStatus) {
	enabled := 0.0
	if s.Enabled {
		enabled = 1
	}
	ch <- prometheus.MustNewConstMetric(c.modeltraceEnabled, prometheus.GaugeValue, enabled)
	ch <- prometheus.MustNewConstMetric(c.modeltraceEnded, prometheus.GaugeValue, float64(s.EndedSpans))
	ch <- prometheus.MustNewConstMetric(c.modeltraceAttempted, prometheus.GaugeValue, float64(s.AttemptedSpans))
	ch <- prometheus.MustNewConstMetric(c.modeltraceExported, prometheus.GaugeValue, float64(s.ExportedSpans))
	ch <- prometheus.MustNewConstMetric(c.modeltraceFailed, prometheus.GaugeValue, float64(s.FailedSpans))
	ch <- prometheus.MustNewConstMetric(c.modeltracePanics, prometheus.GaugeValue, float64(s.Panics))
	for reason, value := range map[string]uint64{
		"invalid_utf8": s.FailedInvalidUTF8, "collector_refused": s.FailedCollectorRefused,
		"timeout": s.FailedTimeout, "queue_full": s.FailedQueueFull, "other": s.FailedOther,
	} {
		ch <- prometheus.MustNewConstMetric(c.modeltraceFailedReasons, prometheus.GaugeValue, float64(value), reason)
	}
}

func (c *applicationStateCollector) collectExportTotals(ch chan<- prometheus.Metric, s modeltrace.ExportTotals) {
	ch <- prometheus.MustNewConstMetric(c.modeltraceEndedTotal, prometheus.CounterValue, float64(s.EndedSpans))
	ch <- prometheus.MustNewConstMetric(c.modeltraceAttemptedTotal, prometheus.CounterValue, float64(s.AttemptedSpans))
	ch <- prometheus.MustNewConstMetric(c.terminalTotal, prometheus.CounterValue, float64(s.ExportedSpans), "success")
	ch <- prometheus.MustNewConstMetric(c.terminalTotal, prometheus.CounterValue, float64(s.FailedSpans), "failure")
	ch <- prometheus.MustNewConstMetric(c.panicsTotal, prometheus.CounterValue, float64(s.Panics))
	for reason, value := range map[string]uint64{
		"invalid_utf8": s.FailedInvalidUTF8, "collector_refused": s.FailedCollectorRefused,
		"timeout": s.FailedTimeout, "queue_full": s.FailedQueueFull, "other": s.FailedOther,
	} {
		ch <- prometheus.MustNewConstMetric(c.failureTotal, prometheus.CounterValue, float64(value), reason)
	}
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
