//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

const modelIQSuccessFixture = `{
  "private_top_level": "must-not-leak",
  "monitored_at": "2026-07-15T12:00:00+08:00",
  "status": "community_confirmed",
  "model_iq": {
    "quota_calibration": {"secret": "must-not-leak"},
    "comparisons": {
        "gpt_test_high": {
          "label": "GPT Test high",
          "model": "gpt-test",
          "reasoning_effort": "high",
          "private_comparison": "must-not-leak",
          "latest": {
            "date": "2026-07-15-pm",
            "score": 120,
            "status": "green",
            "passed": 8,
            "tasks": 10,
            "invalid": 0,
            "total_tokens": 1000,
            "input_tokens": 900,
            "cached_input_tokens": 700,
            "output_tokens": 100,
            "wall_seconds": 60,
            "wall_time_human": "1分钟",
            "model": "gpt-test",
            "reasoning_effort": "high",
            "valid_tasks": 10,
            "cost_usd": 1.25,
            "private_latest": "must-not-leak"
          },
          "recent_days": [
            {
              "date": "2026-07-14-pm",
              "score": 105,
              "status": "green",
              "passed": 7,
              "tasks": 10,
              "invalid": 0,
              "total_tokens": 800,
              "input_tokens": 700,
              "cached_input_tokens": 500,
              "output_tokens": 100,
              "wall_seconds": 50,
              "wall_time_human": "50秒",
              "private_history": "must-not-leak"
            }
          ]
        }
    }
  }
}`

func newModelIQTestService(server *httptest.Server, cacheTTL time.Duration) *ModelIQService {
	svc := NewModelIQService(&config.Config{
		CodexRadar: config.CodexRadarConfig{
			Enabled:  true,
			BaseURL:  server.URL,
			APIToken: "test-token",
			Timeout:  time.Second,
			CacheTTL: cacheTTL,
		},
	})
	client := server.Client()
	client.Timeout = time.Second
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	svc.httpClient = client
	return svc
}

func TestModelIQServiceFetchesWhitelistedDataAndCaches(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("Accept"))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(modelIQSuccessFixture))
	}))
	defer server.Close()

	svc := newModelIQTestService(server, 5*time.Minute)
	view, err := svc.Get(context.Background())
	require.NoError(t, err)
	require.False(t, view.Stale)
	require.Equal(t, "2026-07-15T12:00:00+08:00", view.MonitoredAt)
	require.Equal(t, "community_confirmed", view.Status)
	require.Len(t, view.ModelIQ.Comparisons, 1)
	require.Equal(t, float64(120), view.ModelIQ.Comparisons["gpt_test_high"].Latest.Score)

	cached, err := svc.Get(context.Background())
	require.NoError(t, err)
	require.False(t, cached.Stale)
	require.Equal(t, int32(1), requests.Load())

	raw, err := json.Marshal(cached)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private_top_level")
	require.NotContains(t, string(raw), "quota_calibration")
	require.NotContains(t, string(raw), "private_comparison")
	require.NotContains(t, string(raw), "private_latest")
	require.NotContains(t, string(raw), "private_history")
	require.NotContains(t, string(raw), "test-token")
}

func TestModelIQServiceManualRefreshBypassesCacheAndCoalescesRequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestNumber := requests.Add(1)
		if requestNumber > 1 {
			time.Sleep(30 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(modelIQSuccessFixture))
	}))
	defer server.Close()

	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	svc := newModelIQTestService(server, 5*time.Minute)
	svc.now = func() time.Time { return now }

	_, err := svc.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, int32(1), requests.Load())

	now = now.Add(modelIQManualCooldown + time.Second)
	start := make(chan struct{})
	results := make(chan error, 5)
	for range 5 {
		go func() {
			<-start
			_, refreshErr := svc.Refresh(context.Background())
			results <- refreshErr
		}()
	}
	close(start)
	for range 5 {
		require.NoError(t, <-results)
	}
	require.Equal(t, int32(2), requests.Load(), "concurrent manual refreshes should share one upstream request")

	now = now.Add(modelIQManualCooldown + time.Second)
	_, err = svc.Refresh(context.Background())
	require.NoError(t, err)
	require.Equal(t, int32(3), requests.Load(), "manual refresh should fetch again after the cooldown")
}

func TestModelIQServiceManualRefreshBacksOffAfterFailure(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}))
	defer server.Close()

	svc := newModelIQTestService(server, 5*time.Minute)
	_, err := svc.Refresh(context.Background())
	require.Error(t, err)
	require.Equal(t, "MODEL_IQ_UPSTREAM_UNAVAILABLE", infraerrors.Reason(err))

	_, err = svc.Refresh(context.Background())
	require.Error(t, err)
	require.Equal(t, "MODEL_IQ_REFRESH_RATE_LIMITED", infraerrors.Reason(err))
	require.Equal(t, int32(1), requests.Load(), "failed refreshes should enter retry backoff")
}

func TestModelIQServiceRefreshesOnStartAndInterval(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(modelIQSuccessFixture))
	}))
	defer server.Close()

	svc := newModelIQTestService(server, 5*time.Minute)
	svc.refreshInterval = 20 * time.Millisecond
	svc.Start()
	require.Eventually(t, func() bool {
		return requests.Load() >= 2
	}, time.Second, 10*time.Millisecond)

	svc.Stop()
	requestsAfterStop := requests.Load()
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, requestsAfterStop, requests.Load())

	view, err := svc.Get(context.Background())
	require.NoError(t, err)
	require.Len(t, view.ModelIQ.Comparisons, 1)
	require.Equal(t, requestsAfterStop, requests.Load(), "Get should use the scheduled refresh cache")
}

func TestModelIQServiceReturnsStaleSnapshotWhenRefreshFails(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(modelIQSuccessFixture))
			return
		}
		http.Error(w, "upstream internal details", http.StatusBadGateway)
	}))
	defer server.Close()

	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	svc := newModelIQTestService(server, 5*time.Minute)
	svc.now = func() time.Time { return now }

	fresh, err := svc.Get(context.Background())
	require.NoError(t, err)
	require.False(t, fresh.Stale)

	now = now.Add(6 * time.Minute)
	stale, err := svc.Get(context.Background())
	require.NoError(t, err)
	require.True(t, stale.Stale)
	require.Equal(t, fresh.FetchedAt, stale.FetchedAt)

	retryBackoff, err := svc.Get(context.Background())
	require.NoError(t, err)
	require.True(t, retryBackoff.Stale)
	require.Equal(t, int32(2), requests.Load())
}

func TestModelIQServiceReturnsControlledErrorWithoutCachedData(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"sensitive upstream body test-token"}`))
	}))
	defer server.Close()

	svc := newModelIQTestService(server, 5*time.Minute)
	_, err := svc.Get(context.Background())
	require.Error(t, err)
	require.Equal(t, http.StatusBadGateway, infraerrors.Code(err))
	require.Equal(t, "MODEL_IQ_UPSTREAM_UNAVAILABLE", infraerrors.Reason(err))
	require.NotContains(t, err.Error(), "sensitive upstream body")
	require.NotContains(t, err.Error(), "test-token")
}

func TestModelIQServiceRejectsInvalidAndOversizedJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "invalid JSON", body: `{"model_iq":`},
		{name: "oversized JSON", body: `{"padding":"` + strings.Repeat("x", modelIQMaxResponseBytes) + `"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			svc := newModelIQTestService(server, 5*time.Minute)
			_, err := svc.Get(context.Background())
			require.Error(t, err)
			require.Equal(t, "MODEL_IQ_UPSTREAM_UNAVAILABLE", infraerrors.Reason(err))
		})
	}
}

func TestModelIQServiceRequiresEnabledTokenConfiguration(t *testing.T) {
	svc := NewModelIQService(&config.Config{})
	_, err := svc.Get(context.Background())
	require.ErrorIs(t, err, ErrModelIQNotConfigured)
}

func TestModelIQServiceRejectsPlainHTTPBaseURL(t *testing.T) {
	svc := NewModelIQService(&config.Config{
		CodexRadar: config.CodexRadarConfig{
			Enabled:  true,
			BaseURL:  "http://codexradar.example/api/v1/current",
			APIToken: "test-token",
		},
	})

	_, err := svc.Get(context.Background())
	require.ErrorIs(t, err, ErrModelIQNotConfigured)
	require.NotContains(t, err.Error(), "test-token")
}
