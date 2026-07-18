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
)

const (
	modelIQMaxResponseBytes = 512 * 1024
	modelIQDefaultTimeout   = 15 * time.Second
	modelIQDefaultCacheTTL  = 5 * time.Minute
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
		now: time.Now,
	}
}

// Get returns a fresh in-memory snapshot when possible. If an expired snapshot
// exists and Codex Radar is unavailable, the last successful data is returned
// with stale=true instead of exposing upstream details to the client.
func (s *ModelIQService) Get(ctx context.Context) (*ModelIQView, error) {
	if s == nil || !s.enabled || s.baseURL == "" || s.apiToken == "" || s.httpClient == nil {
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

	fresh, err := s.fetch(ctx)
	if err != nil {
		if cached := s.markRefreshFailed(); cached != nil {
			return cached, nil
		}
		return nil, ErrModelIQUpstreamUnavailable.WithCause(err)
	}

	s.cacheMu.Lock()
	s.cached = fresh
	s.retryAfter = time.Time{}
	s.cacheMu.Unlock()
	return cloneModelIQView(fresh, false), nil
}

func (s *ModelIQService) fetch(ctx context.Context) (*ModelIQView, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create Codex Radar request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiToken)
	req.Header.Set("User-Agent", "sub2api-model-iq/1.0")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request Codex Radar: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Codex Radar returned HTTP %d", resp.StatusCode)
	}
	if !isJSONMediaType(resp.Header.Get("Content-Type")) {
		return nil, fmt.Errorf("Codex Radar returned a non-JSON response")
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, modelIQMaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Codex Radar response: %w", err)
	}
	if len(body) > modelIQMaxResponseBytes {
		return nil, fmt.Errorf("Codex Radar response exceeds %d bytes", modelIQMaxResponseBytes)
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 || !json.Valid(body) {
		return nil, fmt.Errorf("Codex Radar returned invalid JSON")
	}

	var upstream struct {
		MonitoredAt string      `json:"monitored_at"`
		Status      string      `json:"status"`
		ModelIQ     ModelIQData `json:"model_iq"`
	}
	if err := json.Unmarshal(body, &upstream); err != nil {
		return nil, fmt.Errorf("decode Codex Radar response: %w", err)
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

func (s *ModelIQService) markRefreshFailed() *ModelIQView {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.cached == nil {
		return nil
	}
	s.retryAfter = s.now().Add(s.retryDelay)
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
		return fmt.Errorf("Codex Radar base URL is invalid")
	}
	if !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("Codex Radar base URL must be an absolute HTTPS URL without userinfo")
	}
	return nil
}

func validateModelIQData(data ModelIQData) error {
	if len(data.Comparisons) == 0 {
		return fmt.Errorf("Codex Radar response has no model IQ comparisons")
	}
	for key, comparison := range data.Comparisons {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(comparison.Label) == "" || strings.TrimSpace(comparison.Model) == "" {
			return fmt.Errorf("Codex Radar response contains an invalid comparison")
		}
	}
	return nil
}
