package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	modelIQMaxResponseBytes = 512 * 1024
	modelIQDefaultTimeout   = 15 * time.Second
	modelIQDefaultCacheTTL  = 5 * time.Minute
	modelIQRefreshInterval  = time.Hour
	modelIQManualCooldown   = 30 * time.Second
)

var (
	ErrModelIQNotConfigured = infraerrors.ServiceUnavailable(
		"MODEL_IQ_NOT_CONFIGURED",
		"model IQ ranking is not configured",
	)
	ErrModelIQUpstreamUnavailable = infraerrors.New(
		http.StatusBadGateway,
		"MODEL_IQ_UPSTREAM_UNAVAILABLE",
		"model IQ ranking is temporarily unavailable",
	)
	ErrModelIQRefreshRateLimited = infraerrors.New(
		http.StatusTooManyRequests,
		"MODEL_IQ_REFRESH_RATE_LIMITED",
		"model IQ ranking refresh was requested too recently",
	)
)

// ModelIQView is the complete user-facing response. It deliberately contains
// only the fields required to render the model IQ comparison page.
type ModelIQView struct {
	MonitoredAt string      `json:"monitored_at"`
	Status      string      `json:"status"`
	ModelIQ     ModelIQData `json:"model_iq"`
	FetchedAt   time.Time   `json:"fetched_at"`
	Stale       bool        `json:"stale"`
}

type ModelIQData struct {
	Comparisons map[string]ModelIQComparison `json:"comparisons"`
}

type ModelIQComparison struct {
	Label           string                      `json:"label"`
	Model           string                      `json:"model"`
	ReasoningEffort string                      `json:"reasoning_effort"`
	Latest          ModelIQLatestMeasurement    `json:"latest"`
	RecentDays      []ModelIQHistoryMeasurement `json:"recent_days"`
}

type ModelIQLatestMeasurement struct {
	Date              string  `json:"date"`
	Score             float64 `json:"score"`
	Status            string  `json:"status"`
	Passed            int64   `json:"passed"`
	Tasks             int64   `json:"tasks"`
	Invalid           int64   `json:"invalid"`
	TotalTokens       int64   `json:"total_tokens"`
	InputTokens       int64   `json:"input_tokens"`
	CachedInputTokens int64   `json:"cached_input_tokens"`
	OutputTokens      int64   `json:"output_tokens"`
	WallSeconds       float64 `json:"wall_seconds"`
	WallTimeHuman     string  `json:"wall_time_human"`
	Model             string  `json:"model"`
	ReasoningEffort   string  `json:"reasoning_effort"`
	ValidTasks        int64   `json:"valid_tasks"`
	CostUSD           float64 `json:"cost_usd"`
}

type ModelIQHistoryMeasurement struct {
	Date              string  `json:"date"`
	Score             float64 `json:"score"`
	Status            string  `json:"status"`
	Passed            int64   `json:"passed"`
	Tasks             int64   `json:"tasks"`
	Invalid           int64   `json:"invalid"`
	TotalTokens       int64   `json:"total_tokens"`
	InputTokens       int64   `json:"input_tokens"`
	CachedInputTokens int64   `json:"cached_input_tokens"`
	OutputTokens      int64   `json:"output_tokens"`
	WallSeconds       float64 `json:"wall_seconds"`
	WallTimeHuman     string  `json:"wall_time_human"`
}

type ModelIQService struct {
	enabled    bool
	baseURL    string
	apiToken   string
	cacheTTL   time.Duration
	retryDelay time.Duration
	httpClient *http.Client
	now        func() time.Time

	cacheMu    sync.RWMutex
	cached     *ModelIQView
	retryAfter time.Time
	fetchMu    sync.Mutex

	refreshInterval time.Duration
	startOnce       sync.Once
	stopOnce        sync.Once
	stop            context.CancelFunc
	wg              sync.WaitGroup
}

func NewModelIQService(cfg *config.Config) *ModelIQService {
	radarConfig := config.CodexRadarConfig{}
	if cfg != nil {
		radarConfig = cfg.CodexRadar
	}

	timeout := radarConfig.Timeout
	if timeout <= 0 {
		timeout = modelIQDefaultTimeout
	}
	cacheTTL := radarConfig.CacheTTL
	if cacheTTL <= 0 {
		cacheTTL = modelIQDefaultCacheTTL
	}
	retryDelay := time.Minute
	if cacheTTL < retryDelay {
		retryDelay = cacheTTL
	}

	return &ModelIQService{
		enabled:    radarConfig.Enabled,
		baseURL:    strings.TrimSpace(radarConfig.BaseURL),
		apiToken:   strings.TrimSpace(radarConfig.APIToken),
		cacheTTL:   cacheTTL,
		retryDelay: retryDelay,
		httpClient: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		now:             time.Now,
		refreshInterval: modelIQRefreshInterval,
	}
}

// Start warms the cache immediately and refreshes it independently of page traffic.
func (s *ModelIQService) Start() {
	if !s.configured() {
		return
	}
	if err := validateModelIQBaseURL(s.baseURL); err != nil {
		return
	}

	s.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.stop = cancel
		s.wg.Add(1)
		go s.refreshLoop(ctx)
	})
}

// Stop terminates the background refresh loop and any in-flight refresh request.
func (s *ModelIQService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.stop != nil {
			s.stop()
		}
	})
	s.wg.Wait()
}

func (s *ModelIQService) refreshLoop(ctx context.Context) {
	defer s.wg.Done()
	s.refreshScheduled(ctx)

	interval := s.refreshInterval
	if interval <= 0 {
		interval = modelIQRefreshInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.refreshScheduled(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (s *ModelIQService) refreshScheduled(ctx context.Context) {
	s.fetchMu.Lock()
	fresh, err := s.fetchAndCacheLocked(ctx)
	s.fetchMu.Unlock()
	if err != nil {
		if ctx.Err() == nil {
			s.markRefreshFailed()
			logger.LegacyPrintf("service.model_iq", "[ModelIQ] scheduled refresh failed: %v", err)
		}
		return
	}
	logger.LegacyPrintf(
		"service.model_iq",
		"[ModelIQ] scheduled refresh completed (comparisons=%d, monitored_at=%s)",
		len(fresh.ModelIQ.Comparisons),
		fresh.MonitoredAt,
	)
}

func (s *ModelIQService) configured() bool {
	return s != nil && s.enabled && s.baseURL != "" && s.apiToken != "" && s.httpClient != nil
}

// Get returns a fresh in-memory snapshot when possible. If an expired snapshot
// exists and Codex Radar is unavailable, the last successful data is returned
// with stale=true instead of exposing upstream details to the client.
func (s *ModelIQService) Get(ctx context.Context) (*ModelIQView, error) {
	if !s.configured() {
		return nil, ErrModelIQNotConfigured
	}
	if err := validateModelIQBaseURL(s.baseURL); err != nil {
		return nil, ErrModelIQNotConfigured.WithCause(err)
	}
	if cached := s.cachedForRequest(); cached != nil {
		return cached, nil
	}

	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()

	if cached := s.cachedForRequest(); cached != nil {
		return cached, nil
	}

	fresh, err := s.fetchAndCacheLocked(ctx)
	if err != nil {
		if cached := s.markRefreshFailed(); cached != nil {
			return cached, nil
		}
		return nil, ErrModelIQUpstreamUnavailable.WithCause(err)
	}

	return cloneModelIQView(fresh, false), nil
}

// Refresh bypasses the normal cache while coalescing manual refreshes that
// arrive within a short window. Scheduled and concurrent successful fetches
// also satisfy the cooldown, preventing duplicate upstream requests.
func (s *ModelIQService) Refresh(ctx context.Context) (*ModelIQView, error) {
	if !s.configured() {
		return nil, ErrModelIQNotConfigured
	}
	if err := validateModelIQBaseURL(s.baseURL); err != nil {
		return nil, ErrModelIQNotConfigured.WithCause(err)
	}

	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()

	if cached := s.cachedWithin(modelIQManualCooldown); cached != nil {
		return cached, nil
	}
	if cached, limited := s.cachedDuringRetryBackoff(); limited {
		if cached != nil {
			return cached, nil
		}
		return nil, ErrModelIQRefreshRateLimited
	}

	fresh, err := s.fetchAndCacheLocked(ctx)
	if err != nil {
		s.markRefreshFailed()
		return nil, ErrModelIQUpstreamUnavailable.WithCause(err)
	}
	return cloneModelIQView(fresh, false), nil
}

// fetchAndCacheLocked requires fetchMu to be held by the caller.
func (s *ModelIQService) fetchAndCacheLocked(ctx context.Context) (*ModelIQView, error) {
	fresh, err := s.fetch(ctx)
	if err != nil {
		return nil, err
	}
	s.cacheMu.Lock()
	s.cached = fresh
	s.retryAfter = time.Time{}
	s.cacheMu.Unlock()
	return fresh, nil
}

func (s *ModelIQService) fetch(ctx context.Context) (*ModelIQView, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create codex radar request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiToken)
	req.Header.Set("User-Agent", "sub2api-model-iq/1.0")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request codex radar: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("codex radar returned HTTP %d", resp.StatusCode)
	}
	if !isJSONMediaType(resp.Header.Get("Content-Type")) {
		return nil, fmt.Errorf("codex radar returned a non-JSON response")
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, modelIQMaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read codex radar response: %w", err)
	}
	if len(body) > modelIQMaxResponseBytes {
		return nil, fmt.Errorf("codex radar response exceeds %d bytes", modelIQMaxResponseBytes)
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 || !json.Valid(body) {
		return nil, fmt.Errorf("codex radar returned invalid JSON")
	}

	var upstream struct {
		MonitoredAt string      `json:"monitored_at"`
		Status      string      `json:"status"`
		ModelIQ     ModelIQData `json:"model_iq"`
	}
	if err := json.Unmarshal(body, &upstream); err != nil {
		return nil, fmt.Errorf("decode codex radar response: %w", err)
	}
	if err := validateModelIQData(upstream.ModelIQ); err != nil {
		return nil, err
	}

	return &ModelIQView{
		MonitoredAt: upstream.MonitoredAt,
		Status:      upstream.Status,
		ModelIQ:     upstream.ModelIQ,
		FetchedAt:   s.now().UTC(),
		Stale:       false,
	}, nil
}

func (s *ModelIQService) cachedForRequest() *ModelIQView {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	if s.cached == nil {
		return nil
	}
	now := s.now()
	if now.Sub(s.cached.FetchedAt) < s.cacheTTL {
		return cloneModelIQView(s.cached, false)
	}
	if !s.retryAfter.IsZero() && now.Before(s.retryAfter) {
		return cloneModelIQView(s.cached, true)
	}
	return nil
}

func (s *ModelIQService) cachedWithin(maxAge time.Duration) *ModelIQView {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	if s.cached == nil || s.now().Sub(s.cached.FetchedAt) >= maxAge {
		return nil
	}
	return cloneModelIQView(s.cached, false)
}

func (s *ModelIQService) cachedDuringRetryBackoff() (*ModelIQView, bool) {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	if s.retryAfter.IsZero() || !s.now().Before(s.retryAfter) {
		return nil, false
	}
	return cloneModelIQView(s.cached, true), true
}

func (s *ModelIQService) markRefreshFailed() *ModelIQView {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.retryAfter = s.now().Add(s.retryDelay)
	if s.cached == nil {
		return nil
	}
	return cloneModelIQView(s.cached, true)
}

func cloneModelIQView(view *ModelIQView, stale bool) *ModelIQView {
	if view == nil {
		return nil
	}
	cloned := *view
	cloned.Stale = stale
	return &cloned
}

func isJSONMediaType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func validateModelIQBaseURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed == nil {
		return fmt.Errorf("codex radar base URL is invalid")
	}
	if !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("codex radar base URL must be an absolute HTTPS URL without userinfo")
	}
	return nil
}

func validateModelIQData(data ModelIQData) error {
	if len(data.Comparisons) == 0 {
		return fmt.Errorf("codex radar response has no model IQ comparisons")
	}
	for key, comparison := range data.Comparisons {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(comparison.Label) == "" || strings.TrimSpace(comparison.Model) == "" {
			return fmt.Errorf("codex radar response contains an invalid comparison")
		}
	}
	return nil
}
