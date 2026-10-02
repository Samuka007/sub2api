package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

const metricNamespace = "sub2api"

// Closed label values keep the application source plane independent of traffic volume.
type Outcome string
type TokenKind string
type ErrorClass string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeError   Outcome = "error"

	TokenInput         TokenKind = "input"
	TokenOutput        TokenKind = "output"
	TokenCacheCreation TokenKind = "cache_creation"
	TokenCacheRead     TokenKind = "cache_read"

	ErrorBusinessLimited ErrorClass = "business_limited"
	ErrorSLA             ErrorClass = "sla"
	ErrorUpstream429     ErrorClass = "upstream_429"
	ErrorUpstream529     ErrorClass = "upstream_529"
	ErrorUpstreamOther   ErrorClass = "upstream_other"
	ErrorInternal        ErrorClass = "internal"
)

var (
	validOutcomes = map[Outcome]struct{}{
		OutcomeSuccess: {}, OutcomeError: {},
	}
	validTokenKinds = map[TokenKind]struct{}{
		TokenInput: {}, TokenOutput: {}, TokenCacheCreation: {}, TokenCacheRead: {},
	}
	validErrorClasses = map[ErrorClass]struct{}{
		ErrorBusinessLimited: {}, ErrorSLA: {}, ErrorUpstream429: {},
		ErrorUpstream529: {}, ErrorUpstreamOther: {}, ErrorInternal: {},
	}
)

var defaultSource = NewSource()

// SetDefault replaces the process-local source used by service event hooks.
// It is called once while the private metrics registry is initialized.
func SetDefault(source *Source) {
	if source == nil {
		return
	}
	defaultMu.Lock()
	defaultSource = source
	defaultMu.Unlock()
}

func Default() *Source {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultSource
}

var defaultMu sync.RWMutex

func ObserveCompleted(outcome Outcome)        { Default().ObserveCompleted(outcome) }
func ObserveTokens(kind TokenKind, value int) { Default().ObserveTokens(kind, value) }
func ObserveError(class ErrorClass)           { Default().ObserveError(class) }
func ObserveClassifiedError(businessLimited bool, owner string, statusCode, upstreamStatusCode *int) {
	Default().ObserveClassifiedError(businessLimited, owner, statusCode, upstreamStatusCode)
}
func ObserveDuration(seconds float64) { Default().ObserveDuration(seconds) }
func ObserveTTFT(seconds float64)     { Default().ObserveTTFT(seconds) }

// ObserveUsage records one terminal successful usage result. It is intentionally
// called at the application usage boundary rather than from the Ops snapshot job.
func ObserveUsage(input, output, cacheCreation, cacheRead int, durationSeconds, ttftSeconds *float64) {
	ObserveCompleted(OutcomeSuccess)
	ObserveTokens(TokenInput, input)
	ObserveTokens(TokenOutput, output)
	ObserveTokens(TokenCacheCreation, cacheCreation)
	ObserveTokens(TokenCacheRead, cacheRead)
	if durationSeconds != nil {
		ObserveDuration(*durationSeconds)
	}
	if ttftSeconds != nil {
		ObserveTTFT(*ttftSeconds)
	}
}

type Source struct {
	requests *prometheus.CounterVec
	tokens   *prometheus.CounterVec
	errors   *prometheus.CounterVec
	duration prometheus.Histogram
	ttft     prometheus.Histogram
}

func NewSource() *Source {
	duration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: metricNamespace,
		Subsystem: "request",
		Name:      "duration_seconds",
		Help:      "Completed Sub2API request duration in seconds. Long-running LLM requests are bucketed through 30 minutes.",
		Buckets:   []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1200, 1800},
	})
	ttft := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: metricNamespace,
		Subsystem: "request",
		Name:      "ttft_seconds",
		Help:      "Completed streaming request time to first token in seconds.",
		Buckets:   []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
	})
	source := &Source{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: "requests",
			Name:      "completed_total",
			Help:      "Completed Sub2API requests by bounded terminal outcome.",
		}, []string{"outcome"}),
		tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: "tokens",
			Name:      "total",
			Help:      "Sub2API tokens reported at request completion by bounded token kind.",
		}, []string{"kind"}),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: "errors",
			Name:      "total",
			Help:      "Sub2API terminal errors by bounded application classification.",
		}, []string{"class"}),
		duration: duration,
		ttft:     ttft,
	}
	for outcome := range validOutcomes {
		source.requests.WithLabelValues(string(outcome))
	}
	for kind := range validTokenKinds {
		source.tokens.WithLabelValues(string(kind))
	}
	for class := range validErrorClasses {
		source.errors.WithLabelValues(string(class))
	}
	return source
}

func (s *Source) Describe(ch chan<- *prometheus.Desc) {
	s.requests.Describe(ch)
	s.tokens.Describe(ch)
	s.errors.Describe(ch)
	ch <- s.duration.Desc()
	ch <- s.ttft.Desc()
}

func (s *Source) Collect(ch chan<- prometheus.Metric) {
	s.requests.Collect(ch)
	s.tokens.Collect(ch)
	s.errors.Collect(ch)
	s.duration.Collect(ch)
	s.ttft.Collect(ch)
}

func (s *Source) ObserveCompleted(outcome Outcome) {
	if s == nil {
		return
	}
	if _, ok := validOutcomes[outcome]; ok {
		s.requests.WithLabelValues(string(outcome)).Inc()
	}
}

func (s *Source) ObserveTokens(kind TokenKind, value int) {
	if s == nil || value <= 0 {
		return
	}
	if _, ok := validTokenKinds[kind]; ok {
		s.tokens.WithLabelValues(string(kind)).Add(float64(value))
	}
}

func (s *Source) ObserveError(class ErrorClass) {
	if s == nil {
		return
	}
	if _, ok := validErrorClasses[class]; ok {
		s.errors.WithLabelValues(string(class)).Inc()
	}
}

func (s *Source) ObserveClassifiedError(businessLimited bool, owner string, statusCode, upstreamStatusCode *int) {
	if s == nil {
		return
	}
	s.ObserveCompleted(OutcomeError)
	s.ObserveError(ClassifyError(businessLimited, owner, statusCode, upstreamStatusCode))
}

func (s *Source) ObserveDuration(seconds float64) {
	if s == nil || seconds < 0 {
		return
	}
	s.duration.Observe(seconds)
}

func ClassifyError(businessLimited bool, owner string, statusCode, upstreamStatusCode *int) ErrorClass {
	if businessLimited {
		return ErrorBusinessLimited
	}
	code := 0
	if upstreamStatusCode != nil && *upstreamStatusCode > 0 {
		code = *upstreamStatusCode
	} else if statusCode != nil {
		code = *statusCode
	}
	if owner == "provider" {
		switch code {
		case 429:
			return ErrorUpstream429
		case 529:
			return ErrorUpstream529
		default:
			return ErrorUpstreamOther
		}
	}
	if code >= 500 || owner == "internal" {
		return ErrorInternal
	}
	return ErrorSLA
}

func (s *Source) ObserveTTFT(seconds float64) {
	if s == nil || seconds < 0 {
		return
	}
	s.ttft.Observe(seconds)
}
