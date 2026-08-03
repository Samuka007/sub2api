package appmetrics

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
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
	registry.MustRegister(newSnapshotCollector(ops, exports))

	path := cfg.Path
	if path == "" {
		path = "/metrics"
	}
	promHandler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	exactHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		promHandler.ServeHTTP(w, r)
	})
	return &Metrics{cfg: cfg, handler: exactHandler, serve: func(server *http.Server, listener net.Listener) error {
		return server.Serve(listener)
	}}, nil
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

// LastError reports the current generation's unexpected Serve failure. A
// successful restart clears it; public application APIs remain unaffected.
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

type snapshotCollector struct {
	ops     OpsSnapshotSource
	exports ExportStatusSource
	desc    map[string]*prometheus.Desc
	reason  *prometheus.Desc
}

func newSnapshotCollector(ops OpsSnapshotSource, exports ExportStatusSource) *snapshotCollector {
	opsHelp := func(description string) string {
		return "Existing Ops collector-leader one-minute snapshot gauge; not per-instance or additive: " + description
	}
	modeltraceHelp := func(description string) string {
		return "Active modeltrace generation gauge; value resets when the active generation changes: " + description
	}
	help := map[string]string{
		"ops_snapshot_timestamp_seconds":               opsHelp("snapshot timestamp in Unix seconds."),
		"ops_window_success_requests":                  opsHelp("successful request count."),
		"ops_window_error_requests":                    opsHelp("total error count."),
		"ops_window_business_limited_requests":         opsHelp("business-limited count."),
		"ops_window_sla_error_requests":                opsHelp("SLA error count."),
		"ops_window_upstream_errors_excluding_429_529": opsHelp("upstream errors excluding 429 and 529."),
		"ops_window_upstream_429_errors":               opsHelp("upstream 429 count."),
		"ops_window_upstream_529_errors":               opsHelp("upstream 529 count."),
		"ops_window_tokens":                            opsHelp("consumed token count."),
		"ops_window_account_switches":                  opsHelp("account switch count."),
		"ops_qps":                                      opsHelp("requests per second."),
		"ops_tps":                                      opsHelp("tokens per second."),
		"ops_request_duration_p50_milliseconds":        opsHelp("request duration p50 in milliseconds."),
		"ops_request_duration_p90_milliseconds":        opsHelp("request duration p90 in milliseconds."),
		"ops_request_duration_p95_milliseconds":        opsHelp("request duration p95 in milliseconds."),
		"ops_request_duration_p99_milliseconds":        opsHelp("request duration p99 in milliseconds."),
		"ops_request_duration_average_milliseconds":    opsHelp("average request duration in milliseconds."),
		"ops_request_duration_maximum_milliseconds":    opsHelp("maximum request duration in milliseconds."),
		"ops_ttft_p50_milliseconds":                    opsHelp("time-to-first-token p50 in milliseconds."),
		"ops_ttft_p90_milliseconds":                    opsHelp("time-to-first-token p90 in milliseconds."),
		"ops_ttft_p95_milliseconds":                    opsHelp("time-to-first-token p95 in milliseconds."),
		"ops_ttft_p99_milliseconds":                    opsHelp("time-to-first-token p99 in milliseconds."),
		"ops_ttft_average_milliseconds":                opsHelp("average time-to-first-token in milliseconds."),
		"ops_ttft_maximum_milliseconds":                opsHelp("maximum time-to-first-token in milliseconds."),
		"infra_cpu_usage_percent":                      opsHelp("CPU usage percent."),
		"infra_memory_used_bytes":                      opsHelp("memory used in bytes."),
		"infra_memory_total_bytes":                     opsHelp("memory total in bytes."),
		"infra_memory_usage_percent":                   opsHelp("memory usage percent."),
		"infra_database_up":                            opsHelp("database status, 1 up and 0 down."),
		"infra_redis_up":                               opsHelp("Redis status, 1 up and 0 down."),
		"infra_goroutines":                             opsHelp("goroutine count."),
		"infra_concurrency_queue_depth":                opsHelp("concurrency queue depth."),
		"modeltrace_enabled":                           modeltraceHelp("1 when enabled, otherwise 0."),
		"modeltrace_ended_spans":                       modeltraceHelp("ended spans."),
		"modeltrace_export_attempted_spans":            modeltraceHelp("export-attempted spans."),
		"modeltrace_exported_spans":                    modeltraceHelp("exported spans."),
		"modeltrace_export_failed_spans":               modeltraceHelp("terminally failed export spans."),
		"modeltrace_export_panics":                     modeltraceHelp("exporter panics."),
	}
	desc := make(map[string]*prometheus.Desc, len(help)+2)
	for name, text := range help {
		desc[name] = prometheus.NewDesc("sub2api_"+name, text, nil, nil)
	}
	desc["infra_database_connections"] = prometheus.NewDesc("sub2api_infra_database_connections", opsHelp("database connections by fixed state."), []string{"state"}, nil)
	desc["infra_redis_connections"] = prometheus.NewDesc("sub2api_infra_redis_connections", opsHelp("Redis connections by fixed state."), []string{"state"}, nil)
	return &snapshotCollector{
		ops: ops, exports: exports, desc: desc,
		reason: prometheus.NewDesc("sub2api_modeltrace_export_failed_spans_by_reason", modeltraceHelp("terminally failed export spans by existing fixed reason."), []string{"reason"}, nil),
	}
}

func (c *snapshotCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range c.desc {
		ch <- desc
	}
	ch <- c.reason
}

func (c *snapshotCollector) Collect(ch chan<- prometheus.Metric) {
	if c.ops != nil {
		if snapshot := c.ops.LatestSnapshot(); snapshot != nil {
			c.collectOps(ch, snapshot)
		}
	}
	status := modeltrace.ExportStatus{}
	if c.exports != nil {
		status = c.exports.ExportStatus()
	}
	c.collectExportStatus(ch, status)
}

func (c *snapshotCollector) gauge(ch chan<- prometheus.Metric, name string, value float64, labels ...string) {
	ch <- prometheus.MustNewConstMetric(c.desc[name], prometheus.GaugeValue, value, labels...)
}

func (c *snapshotCollector) collectOps(ch chan<- prometheus.Metric, s *service.OpsInsertSystemMetricsInput) {
	c.gauge(ch, "ops_snapshot_timestamp_seconds", float64(s.CreatedAt.Unix()))
	c.gauge(ch, "ops_window_success_requests", float64(s.SuccessCount))
	c.gauge(ch, "ops_window_error_requests", float64(s.ErrorCountTotal))
	c.gauge(ch, "ops_window_business_limited_requests", float64(s.BusinessLimitedCount))
	c.gauge(ch, "ops_window_sla_error_requests", float64(s.ErrorCountSLA))
	c.gauge(ch, "ops_window_upstream_errors_excluding_429_529", float64(s.UpstreamErrorCountExcl429529))
	c.gauge(ch, "ops_window_upstream_429_errors", float64(s.Upstream429Count))
	c.gauge(ch, "ops_window_upstream_529_errors", float64(s.Upstream529Count))
	c.gauge(ch, "ops_window_tokens", float64(s.TokenConsumed))
	c.gauge(ch, "ops_window_account_switches", float64(s.AccountSwitchCount))
	optionalGauge(ch, c.desc["ops_qps"], s.QPS, 1)
	optionalGauge(ch, c.desc["ops_tps"], s.TPS, 1)
	optionalGauge(ch, c.desc["ops_request_duration_p50_milliseconds"], s.DurationP50Ms, 1)
	optionalGauge(ch, c.desc["ops_request_duration_p90_milliseconds"], s.DurationP90Ms, 1)
	optionalGauge(ch, c.desc["ops_request_duration_p95_milliseconds"], s.DurationP95Ms, 1)
	optionalGauge(ch, c.desc["ops_request_duration_p99_milliseconds"], s.DurationP99Ms, 1)
	optionalGauge(ch, c.desc["ops_request_duration_average_milliseconds"], s.DurationAvgMs, 1)
	optionalGauge(ch, c.desc["ops_request_duration_maximum_milliseconds"], s.DurationMaxMs, 1)
	optionalGauge(ch, c.desc["ops_ttft_p50_milliseconds"], s.TTFTP50Ms, 1)
	optionalGauge(ch, c.desc["ops_ttft_p90_milliseconds"], s.TTFTP90Ms, 1)
	optionalGauge(ch, c.desc["ops_ttft_p95_milliseconds"], s.TTFTP95Ms, 1)
	optionalGauge(ch, c.desc["ops_ttft_p99_milliseconds"], s.TTFTP99Ms, 1)
	optionalGauge(ch, c.desc["ops_ttft_average_milliseconds"], s.TTFTAvgMs, 1)
	optionalGauge(ch, c.desc["ops_ttft_maximum_milliseconds"], s.TTFTMaxMs, 1)
	optionalGauge(ch, c.desc["infra_cpu_usage_percent"], s.CPUUsagePercent, 1)
	optionalGauge(ch, c.desc["infra_memory_used_bytes"], s.MemoryUsedMB, 1024*1024)
	optionalGauge(ch, c.desc["infra_memory_total_bytes"], s.MemoryTotalMB, 1024*1024)
	optionalGauge(ch, c.desc["infra_memory_usage_percent"], s.MemoryUsagePercent, 1)
	optionalBoolGauge(ch, c.desc["infra_database_up"], s.DBOK)
	optionalBoolGauge(ch, c.desc["infra_redis_up"], s.RedisOK)
	optionalLabeledGauge(ch, c.desc["infra_database_connections"], s.DBConnActive, "active")
	optionalLabeledGauge(ch, c.desc["infra_database_connections"], s.DBConnIdle, "idle")
	optionalLabeledGauge(ch, c.desc["infra_redis_connections"], s.RedisConnTotal, "total")
	optionalLabeledGauge(ch, c.desc["infra_redis_connections"], s.RedisConnIdle, "idle")
	optionalGauge(ch, c.desc["infra_goroutines"], s.GoroutineCount, 1)
	optionalGauge(ch, c.desc["infra_concurrency_queue_depth"], s.ConcurrencyQueueDepth, 1)
}

func (c *snapshotCollector) collectExportStatus(ch chan<- prometheus.Metric, s modeltrace.ExportStatus) {
	enabled := 0.0
	if s.Enabled {
		enabled = 1
	}
	c.gauge(ch, "modeltrace_enabled", enabled)
	c.gauge(ch, "modeltrace_ended_spans", float64(s.EndedSpans))
	c.gauge(ch, "modeltrace_export_attempted_spans", float64(s.AttemptedSpans))
	c.gauge(ch, "modeltrace_exported_spans", float64(s.ExportedSpans))
	c.gauge(ch, "modeltrace_export_failed_spans", float64(s.FailedSpans))
	c.gauge(ch, "modeltrace_export_panics", float64(s.Panics))
	for reason, value := range map[string]uint64{
		"invalid_utf8": s.FailedInvalidUTF8, "collector_refused": s.FailedCollectorRefused,
		"timeout": s.FailedTimeout, "queue_full": s.FailedQueueFull, "other": s.FailedOther,
	} {
		ch <- prometheus.MustNewConstMetric(c.reason, prometheus.GaugeValue, float64(value), reason)
	}
}

type number interface{ ~int | ~int64 | ~float64 }

func optionalGauge[T number](ch chan<- prometheus.Metric, desc *prometheus.Desc, value *T, multiplier float64) {
	if value != nil {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, float64(*value)*multiplier)
	}
}

func optionalBoolGauge(ch chan<- prometheus.Metric, desc *prometheus.Desc, value *bool) {
	if value == nil {
		return
	}
	number := 0.0
	if *value {
		number = 1
	}
	ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, number)
}

func optionalLabeledGauge(ch chan<- prometheus.Metric, desc *prometheus.Desc, value *int, label string) {
	if value != nil {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, float64(*value), label)
	}
}
