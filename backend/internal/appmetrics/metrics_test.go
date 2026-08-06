package appmetrics

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

type fakeExportSource struct {
	status modeltrace.ExportStatus
	totals modeltrace.ExportTotals
}

func (f fakeExportSource) ExportStatus() modeltrace.ExportStatus { return f.status }
func (f fakeExportSource) ExportTotals() modeltrace.ExportTotals { return f.totals }

func TestServeFailureClearsStateIsObservableAndAllowsRestart(t *testing.T) {
	metrics, err := New(config.MetricsConfig{Enabled: true, Host: "127.0.0.1", Port: 0}, fakeOpsSource{}, fakeExportSource{})
	require.NoError(t, err)
	serveResults := make(chan error, 2)
	metrics.serve = func(_ *http.Server, _ net.Listener) error { return <-serveResults }

	require.NoError(t, metrics.Start())
	require.NotNil(t, metrics.Addr())
	serveFailure := errors.New("accept failed")
	serveResults <- serveFailure
	require.Eventually(t, func() bool { return metrics.Addr() == nil }, time.Second, time.Millisecond)
	require.ErrorIs(t, metrics.LastError(), serveFailure)

	require.NoError(t, metrics.Start())
	require.NotNil(t, metrics.Addr())
	require.NoError(t, metrics.Shutdown(context.Background()))
	serveResults <- http.ErrServerClosed
}

func TestOldServeFailureCannotClearRestartedServerState(t *testing.T) {
	metrics, err := New(config.MetricsConfig{Enabled: true, Host: "127.0.0.1", Port: 0}, fakeOpsSource{}, fakeExportSource{})
	require.NoError(t, err)
	firstResult := make(chan error, 1)
	secondResult := make(chan error, 1)
	serveStarted := make(chan int, 2)
	var serveCalls int
	var serveMu sync.Mutex
	metrics.serve = func(_ *http.Server, _ net.Listener) error {
		serveMu.Lock()
		serveCalls++
		call := serveCalls
		serveMu.Unlock()
		serveStarted <- call
		if call == 1 {
			return <-firstResult
		}
		return <-secondResult
	}

	require.NoError(t, metrics.Start())
	require.Equal(t, 1, <-serveStarted)
	require.NoError(t, metrics.Shutdown(context.Background()))
	require.NoError(t, metrics.Start())
	require.Equal(t, 2, <-serveStarted)
	restartedAddr := metrics.Addr()
	require.NotNil(t, restartedAddr)

	firstResult <- errors.New("late old serve failure")
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, restartedAddr.String(), metrics.Addr().String())
	require.NoError(t, metrics.LastError())

	require.NoError(t, metrics.Shutdown(context.Background()))
	secondResult <- http.ErrServerClosed
}

func TestRegistryProjectsExistingOpsSnapshotInfraAndExportStatus(t *testing.T) {
	dbOK, redisOK := true, false
	redisTotal, redisIdle := 20, 15
	dbActive, dbIdle := 4, 6
	goroutines, queueDepth := 88, 3

	metrics, err := New(
		config.MetricsConfig{Enabled: true},
		fakeOpsSource{snapshot: &service.OpsInsertSystemMetricsInput{
			CreatedAt: time.Unix(1_700_000_000, 0).UTC(), WindowMinutes: 1,
			SuccessCount: 100, ErrorCountTotal: 12, BusinessLimitedCount: 3, ErrorCountSLA: 9,
			DBOK: &dbOK, RedisOK: &redisOK,
			RedisConnTotal: &redisTotal, RedisConnIdle: &redisIdle,
			DBConnActive: &dbActive, DBConnIdle: &dbIdle,
			GoroutineCount: &goroutines, ConcurrencyQueueDepth: &queueDepth,
		}},
		fakeExportSource{
			status: modeltrace.ExportStatus{
				Enabled: true, EndedSpans: 50, AttemptedSpans: 45, ExportedSpans: 40,
				FailedSpans: 5, Panics: 1,
				FailedInvalidUTF8: 1, FailedCollectorRefused: 2, FailedTimeout: 1, FailedQueueFull: 1,
			},
			totals: modeltrace.ExportTotals{
				EndedSpans: 150, AttemptedSpans: 145, ExportedSpans: 138, FailedSpans: 7, Panics: 2,
				FailedInvalidUTF8: 1, FailedCollectorRefused: 2, FailedTimeout: 2, FailedQueueFull: 1, FailedOther: 1,
			},
		},
	)
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()

	for _, expected := range []string{
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
		`sub2api_requests_completed_total{outcome="success"} 0`,
		`sub2api_tokens_total{kind="input"} 0`,
		`# TYPE sub2api_request_duration_seconds histogram`,
		`# TYPE sub2api_modeltrace_export_attempted_spans_total counter`,
		`sub2api_modeltrace_ended_spans_total 150`,
		`sub2api_modeltrace_export_attempted_spans_total 145`,
		`sub2api_modeltrace_export_terminal_spans_total{outcome="success"} 138`,
		`sub2api_modeltrace_export_terminal_spans_total{outcome="failure"} 7`,
		`sub2api_modeltrace_export_panics_total 2`,
		`sub2api_modeltrace_export_failed_spans_total{reason="timeout"} 2`,
	} {
		require.Contains(t, body, expected)
	}
	for _, removed := range []string{"sub2api_ops_", "sub2api_infra_cpu_", "sub2api_infra_memory_"} {
		require.NotContains(t, body, removed)
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
	require.Contains(t, body, "sub2api_requests_completed_total{outcome=\"success\"} 0")
	require.Contains(t, body, "sub2api_modeltrace_enabled 0")
}

func TestRegistryInitializesEveryProcessFailureReasonCounterAtZero(t *testing.T) {
	metrics, err := New(config.MetricsConfig{Enabled: true}, fakeOpsSource{}, fakeExportSource{})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, reason := range []string{"invalid_utf8", "collector_refused", "timeout", "queue_full", "other"} {
		require.Contains(t, body, `sub2api_modeltrace_export_failed_spans_total{reason="`+reason+`"} 0`)
	}
}

func TestRegistryExportsExactFixedProcessCounterSeries(t *testing.T) {
	metrics, err := New(config.MetricsConfig{Enabled: true}, fakeOpsSource{}, fakeExportSource{})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	var samples []string
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if strings.HasPrefix(line, "sub2api_modeltrace_") && strings.Contains(line, "_total") {
			samples = append(samples, strings.Fields(line)[0])
		}
	}
	require.ElementsMatch(t, []string{
		"sub2api_modeltrace_ended_spans_total",
		"sub2api_modeltrace_export_attempted_spans_total",
		`sub2api_modeltrace_export_terminal_spans_total{outcome="success"}`,
		`sub2api_modeltrace_export_terminal_spans_total{outcome="failure"}`,
		`sub2api_modeltrace_export_failed_spans_total{reason="invalid_utf8"}`,
		`sub2api_modeltrace_export_failed_spans_total{reason="collector_refused"}`,
		`sub2api_modeltrace_export_failed_spans_total{reason="timeout"}`,
		`sub2api_modeltrace_export_failed_spans_total{reason="queue_full"}`,
		`sub2api_modeltrace_export_failed_spans_total{reason="other"}`,
		"sub2api_modeltrace_export_panics_total",
	}, samples)
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

func TestOneMinuteSnapshotFamiliesAreNotExposed(t *testing.T) {
	metrics, err := New(
		config.MetricsConfig{Enabled: true},
		fakeOpsSource{snapshot: &service.OpsInsertSystemMetricsInput{WindowMinutes: 1, SuccessCount: 1}},
		fakeExportSource{},
	)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	require.NotContains(t, body, "sub2api_ops_window_success_requests")
	require.NotContains(t, body, "sub2api_infra_cpu_usage_percent")
}

type mutableFakeOpsSource struct {
	snapshot *service.OpsInsertSystemMetricsInput
}

func (f *mutableFakeOpsSource) LatestSnapshot() *service.OpsInsertSystemMetricsInput {
	return f.snapshot
}

func TestNilOpsFieldsAreOmittedOnEachScrape(t *testing.T) {
	source := &mutableFakeOpsSource{snapshot: &service.OpsInsertSystemMetricsInput{}}
	metrics, err := New(config.MetricsConfig{Enabled: true}, source, fakeExportSource{})
	require.NoError(t, err)

	first := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.NotContains(t, first.Body.String(), "sub2api_ops_qps")

	source.snapshot = &service.OpsInsertSystemMetricsInput{}
	second := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.NotContains(t, second.Body.String(), "sub2api_ops_qps")
}

func TestAllExportedSnapshotSamplesUseTheirDeclaredLifecycleType(t *testing.T) {
	metrics, err := New(
		config.MetricsConfig{Enabled: true},
		fakeOpsSource{snapshot: &service.OpsInsertSystemMetricsInput{SuccessCount: 1}},
		fakeExportSource{
			status: modeltrace.ExportStatus{Enabled: true, ExportedSpans: 1},
			totals: modeltrace.ExportTotals{ExportedSpans: 1},
		},
	)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	require.Contains(t, body, "# TYPE sub2api_requests_completed_total counter")
	require.Contains(t, body, "# TYPE sub2api_modeltrace_exported_spans gauge")
	require.Contains(t, body, "# TYPE sub2api_modeltrace_export_terminal_spans_total counter")
}
