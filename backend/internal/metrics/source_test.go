package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestSourceMetricsExposeLowCardinalityFacts(t *testing.T) {
	source := NewSource()
	registry := prometheus.NewRegistry()
	require.NoError(t, registry.Register(source))

	source.ObserveCompleted(OutcomeSuccess)
	source.ObserveCompleted(OutcomeError)
	source.ObserveTokens(TokenInput, 10)
	source.ObserveTokens(TokenOutput, 20)
	source.ObserveTokens(TokenCacheCreation, 30)
	source.ObserveTokens(TokenCacheRead, 40)
	source.ObserveError(ErrorUpstream429)
	source.ObserveDuration(0.25)
	source.ObserveTTFT(0.08)

	text := testutil.ToFloat64
	require.Equal(t, float64(1), text(source.requests.WithLabelValues(string(OutcomeSuccess))))
	require.Equal(t, float64(1), text(source.requests.WithLabelValues(string(OutcomeError))))
	require.Equal(t, float64(10), text(source.tokens.WithLabelValues(string(TokenInput))))
	require.Equal(t, float64(1), text(source.errors.WithLabelValues(string(ErrorUpstream429))))

	gathered, err := registry.Gather()
	require.NoError(t, err)
	require.Len(t, gathered, 5)
	for _, family := range gathered {
		require.NotContains(t, family.GetName(), "ops_qps")
		require.NotContains(t, family.GetName(), "ops_tps")
		require.NotContains(t, family.GetName(), "request_duration_p95")
	}
}

func TestSourceMetricsInitializeAllFixedCounterLabels(t *testing.T) {
	source := NewSource()
	registry := prometheus.NewRegistry()
	require.NoError(t, registry.Register(source))

	families, err := registry.Gather()
	require.NoError(t, err)
	require.Len(t, families, 5)

	for _, outcome := range []Outcome{OutcomeSuccess, OutcomeError} {
		require.NotNil(t, source.requests.WithLabelValues(string(outcome)))
		_ = outcome
	}
}

func TestSourceMetricsRejectInvalidLabelsAndUnknownTTFT(t *testing.T) {
	source := NewSource()

	source.ObserveCompleted(Outcome("provider-openai"))
	source.ObserveTokens(TokenKind("model"), 100)
	source.ObserveError(ErrorClass("raw error text"))
	source.ObserveTTFT(-1)

	require.Equal(t, float64(0), testutil.ToFloat64(source.requests.WithLabelValues(string(OutcomeSuccess))))
	require.Equal(t, float64(0), testutil.ToFloat64(source.tokens.WithLabelValues(string(TokenInput))))
	require.Equal(t, float64(0), testutil.ToFloat64(source.errors.WithLabelValues(string(ErrorInternal))))
}

func TestSourceMetricsClassifiedErrorUpdatesOutcomeAndClass(t *testing.T) {
	source := NewSource()
	status := 429

	source.ObserveClassifiedError(false, "provider", &status, &status)

	require.Equal(t, float64(1), testutil.ToFloat64(source.requests.WithLabelValues(string(OutcomeError))))
	require.Equal(t, float64(1), testutil.ToFloat64(source.errors.WithLabelValues(string(ErrorUpstream429))))
	require.Equal(t, float64(0), testutil.ToFloat64(source.errors.WithLabelValues(string(ErrorInternal))))
}

func TestSourceMetricsDurationHistogramCoversLongLLMRequests(t *testing.T) {
	source := NewSource()
	registry := prometheus.NewRegistry()
	require.NoError(t, registry.Register(source))

	families, err := registry.Gather()
	require.NoError(t, err)

	for _, family := range families {
		if family.GetName() != "sub2api_request_duration_seconds" {
			continue
		}
		require.Len(t, family.GetMetric(), 1)
		buckets := family.GetMetric()[0].GetHistogram().GetBucket()
		upperBounds := make([]float64, 0, len(buckets))
		for _, bucket := range buckets {
			upperBounds = append(upperBounds, bucket.GetUpperBound())
		}
		require.Equal(t, []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1200, 1800}, upperBounds)
		return
	}
	t.Fatal("sub2api_request_duration_seconds metric family not found")
}

func TestSourceMetricsHistogramUsesSeconds(t *testing.T) {
	source := NewSource()
	registry := prometheus.NewRegistry()
	require.NoError(t, registry.Register(source))

	source.ObserveDuration(250 * 1e-3)
	source.ObserveTTFT(80 * 1e-3)

	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		switch family.GetName() {
		case "sub2api_request_duration_seconds", "sub2api_request_ttft_seconds":
			require.Equal(t, "HISTOGRAM", family.GetType().String())
		}
	}
}
