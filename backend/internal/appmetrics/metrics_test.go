package appmetrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/modeltrace"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type fakeOpsSource struct {
	snapshot *service.OpsInsertSystemMetricsInput
}

func (f fakeOpsSource) LatestSnapshot() *service.OpsInsertSystemMetricsInput { return f.snapshot }

type fakeExportSource struct{ status modeltrace.ExportStatus }

func (f fakeExportSource) ExportStatus() modeltrace.ExportStatus { return f.status }

func TestRegistryProjectsExistingOpsSnapshotInfraAndExportStatus(t *testing.T) {
	qps, tps := 2.5, 90.0
	durationP95, durationP99 := 250, 800
	ttftP95, ttftP99 := 120, 300
	cpu, memoryPercent := 17.5, 42.0
	memoryUsed, memoryTotal := int64(1024), int64(4096)
	dbOK, redisOK := true, false
	redisTotal, redisIdle := 20, 15
	dbActive, dbIdle := 4, 6
	goroutines, queueDepth := 88, 3

	metrics, err := New(
		config.MetricsConfig{Enabled: true},
		fakeOpsSource{snapshot: &service.OpsInsertSystemMetricsInput{
			CreatedAt: time.Unix(1_700_000_000, 0).UTC(), WindowMinutes: 1,
			SuccessCount: 100, ErrorCountTotal: 12, BusinessLimitedCount: 3, ErrorCountSLA: 9,
			UpstreamErrorCountExcl429529: 4, Upstream429Count: 2, Upstream529Count: 1,
			TokenConsumed: 5400, AccountSwitchCount: 7, QPS: &qps, TPS: &tps,
			DurationP95Ms: &durationP95, DurationP99Ms: &durationP99,
			TTFTP95Ms: &ttftP95, TTFTP99Ms: &ttftP99,
			CPUUsagePercent: &cpu, MemoryUsedMB: &memoryUsed, MemoryTotalMB: &memoryTotal, MemoryUsagePercent: &memoryPercent,
			DBOK: &dbOK, RedisOK: &redisOK,
			RedisConnTotal: &redisTotal, RedisConnIdle: &redisIdle,
			DBConnActive: &dbActive, DBConnIdle: &dbIdle,
			GoroutineCount: &goroutines, ConcurrencyQueueDepth: &queueDepth,
		}},
		fakeExportSource{status: modeltrace.ExportStatus{
			Enabled: true, EndedSpans: 50, AttemptedSpans: 45, ExportedSpans: 40,
			FailedSpans: 5, Panics: 1,
			FailedInvalidUTF8: 1, FailedCollectorRefused: 2, FailedTimeout: 1, FailedQueueFull: 1,
		}},
	)
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()

	for _, expected := range []string{
		`sub2api_ops_snapshot_timestamp_seconds 1.7e+09`,
		`sub2api_ops_window_success_requests 100`,
		`sub2api_ops_window_error_requests 12`,
		`sub2api_ops_window_business_limited_requests 3`,
		`sub2api_ops_window_sla_error_requests 9`,
		`sub2api_ops_window_upstream_errors_excluding_429_529 4`,
		`sub2api_ops_window_upstream_429_errors 2`,
		`sub2api_ops_window_upstream_529_errors 1`,
		`sub2api_ops_window_tokens 5400`,
		`sub2api_ops_window_account_switches 7`,
		`sub2api_ops_qps 2.5`,
		`sub2api_ops_tps 90`,
		`sub2api_ops_request_duration_p95_milliseconds 250`,
		`sub2api_ops_request_duration_p99_milliseconds 800`,
		`sub2api_ops_ttft_p95_milliseconds 120`,
		`sub2api_ops_ttft_p99_milliseconds 300`,
		`sub2api_infra_cpu_usage_percent 17.5`,
		`sub2api_infra_memory_used_bytes 1.073741824e+09`,
		`sub2api_infra_memory_total_bytes 4.294967296e+09`,
		`sub2api_infra_memory_usage_percent 42`,
		`sub2api_infra_database_up 1`,
		`sub2api_infra_redis_up 0`,
		`sub2api_infra_database_connections{state="active"} 4`,
		`sub2api_infra_database_connections{state="idle"} 6`,
		`sub2api_infra_redis_connections{state="total"} 20`,
		`sub2api_infra_redis_connections{state="idle"} 15`,
		`sub2api_infra_goroutines 88`,
		`sub2api_infra_concurrency_queue_depth 3`,
		`sub2api_modeltrace_enabled 1`,
		`sub2api_modeltrace_ended_spans 50`,
		`sub2api_modeltrace_export_attempted_spans 45`,
		`sub2api_modeltrace_exported_spans 40`,
		`sub2api_modeltrace_export_failed_spans 5`,
		`sub2api_modeltrace_export_panics 1`,
		`sub2api_modeltrace_export_failed_spans_by_reason{reason="collector_refused"} 2`,
	} {
		require.Contains(t, body, expected)
	}

	for _, forbidden := range []string{
		"sub2api_build_info",
		"sub2api_infra_database_connections{state=\"waiting\"}",
		"sub2api_modeltrace_pending_or_dropped_spans",
		"go_",
		"process_",
		"sub2api_http_requests_total",
		"sub2api_http_in_flight_requests",
		"sub2api_upstream_failures_total",
		"sub2api_upstream_provider_failures_total",
	} {
		require.NotContains(t, body, forbidden)
	}
	require.NotContains(t, body, "account_id")
	require.NotContains(t, body, "request_id")
	require.NotContains(t, body, "model=")
	require.False(t, strings.Contains(body, "provider="))
}

func TestRegistryOmitsOpsSeriesUntilOriginalCollectorHasSnapshot(t *testing.T) {
	metrics, err := New(config.MetricsConfig{Enabled: true}, fakeOpsSource{}, fakeExportSource{})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	require.NotContains(t, body, "sub2api_ops_window_success_requests")
	require.Contains(t, body, "sub2api_modeltrace_enabled 0")
}

func TestListenerUsesConfiguredExactPathAndShutsDown(t *testing.T) {
	metrics, err := New(
		config.MetricsConfig{Enabled: true, Host: "127.0.0.1", Port: 0, Path: "/internal/metrics"},
		fakeOpsSource{}, fakeExportSource{},
	)
	require.NoError(t, err)
	require.NoError(t, metrics.Start())
	t.Cleanup(func() { require.NoError(t, metrics.Shutdown(context.Background())) })

	baseURL := "http://" + metrics.Addr().String()
	response, err := http.Get(baseURL + "/internal/metrics") //nolint:gosec // loopback test listener
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.NoError(t, response.Body.Close())

	response, err = http.Get(baseURL + "/metrics") //nolint:gosec // loopback test listener
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, response.StatusCode)
	require.NoError(t, response.Body.Close())

	require.NoError(t, metrics.Shutdown(context.Background()))
	_, err = http.Get(baseURL + "/internal/metrics") //nolint:gosec // verifies closed listener
	require.Error(t, err)
}

func TestDisabledMetricsDoesNotOpenListener(t *testing.T) {
	metrics, err := New(config.MetricsConfig{}, fakeOpsSource{}, fakeExportSource{})
	require.NoError(t, err)
	require.NoError(t, metrics.Start())
	require.Nil(t, metrics.Addr())
	require.NoError(t, metrics.Shutdown(context.Background()))
}

func TestOpsHelpNamesExistingOneMinuteSnapshot(t *testing.T) {
	cpu := 1.0
	metrics, err := New(
		config.MetricsConfig{Enabled: true},
		fakeOpsSource{snapshot: &service.OpsInsertSystemMetricsInput{WindowMinutes: 1, SuccessCount: 1, CPUUsagePercent: &cpu}},
		fakeExportSource{},
	)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	require.Contains(t, body, "# HELP sub2api_ops_window_success_requests Existing Ops collector-leader one-minute snapshot gauge; not per-instance or additive")
	require.Contains(t, body, "# HELP sub2api_infra_cpu_usage_percent Existing Ops collector-leader one-minute snapshot gauge; not per-instance or additive")
}

type mutableFakeOpsSource struct {
	snapshot *service.OpsInsertSystemMetricsInput
}

func (f *mutableFakeOpsSource) LatestSnapshot() *service.OpsInsertSystemMetricsInput {
	return f.snapshot
}

func TestNilOpsFieldsAreOmittedOnEachScrape(t *testing.T) {
	qps := 2.5
	source := &mutableFakeOpsSource{snapshot: &service.OpsInsertSystemMetricsInput{QPS: &qps}}
	metrics, err := New(config.MetricsConfig{Enabled: true}, source, fakeExportSource{})
	require.NoError(t, err)

	first := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Contains(t, first.Body.String(), "sub2api_ops_qps 2.5")

	source.snapshot = &service.OpsInsertSystemMetricsInput{}
	second := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.NotContains(t, second.Body.String(), "sub2api_ops_qps")
}

func TestAllExportedSnapshotSamplesAreGauges(t *testing.T) {
	metrics, err := New(
		config.MetricsConfig{Enabled: true},
		fakeOpsSource{snapshot: &service.OpsInsertSystemMetricsInput{SuccessCount: 1}},
		fakeExportSource{status: modeltrace.ExportStatus{Enabled: true, ExportedSpans: 1}},
	)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	require.Contains(t, body, "# TYPE sub2api_ops_window_success_requests gauge")
	require.Contains(t, body, "# TYPE sub2api_modeltrace_exported_spans gauge")
	require.NotContains(t, body, " counter\n")
}
