package service

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	quotaRecoveryExhaustionPercent = 100.0
	quotaRecoveryCheckTimeout      = 35 * time.Second
	quotaRecoveryResetTolerance    = 30 * time.Second
	openAISparkMeteredFeature      = "codex_bengalfox"
)

const (
	QuotaRecoverySourceOpenAI      = "openai_wham_usage"
	QuotaRecoverySourceAnthropic   = "anthropic_oauth_usage"
	QuotaRecoverySourceUnsupported = "unsupported"
)

// QuotaRecoveryState is a fail-closed assessment of whether a rate-limited
// account can be recovered. Available is the only state that authorizes a
// caller to clear a matching rate-limit observation.
type QuotaRecoveryState string

const (
	QuotaRecoveryAvailable QuotaRecoveryState = "available"
	QuotaRecoveryExhausted QuotaRecoveryState = "exhausted"
	QuotaRecoveryUnknown   QuotaRecoveryState = "unknown"
)

// QuotaRecoveryCheckResult contains the provider-neutral result and evidence
// metadata needed by a recovery runner. Unknown is never authoritative and
// must not trigger recovery.
type QuotaRecoveryCheckResult struct {
	Verdict       QuotaRecoveryState
	Authoritative bool
	CheckedAt     time.Time
	Source        string
	Reason        string
}

// OpenAIQuotaUsageReader is the read-only dependency used for OpenAI OAuth
// quota checks. *OpenAIQuotaService satisfies this interface.
type OpenAIQuotaUsageReader interface {
	QueryUsage(ctx context.Context, accountID int64) (*OpenAIQuotaUsage, error)
}

// AnthropicOAuthUsageReader is the read-only dependency used for Anthropic
// OAuth quota checks. *AccountUsageService satisfies this interface without
// invoking an inference endpoint.
type AnthropicOAuthUsageReader interface {
	QueryAnthropicOAuthUsage(ctx context.Context, account *Account) (*ClaudeUsageResponse, error)
}

// QuotaRecoveryChecker converts provider-specific quota responses into a
// conservative three-state decision. It never mutates account state.
type QuotaRecoveryChecker struct {
	openAI    OpenAIQuotaUsageReader
	anthropic AnthropicOAuthUsageReader
}

func NewQuotaRecoveryChecker(
	openAI OpenAIQuotaUsageReader,
	anthropic AnthropicOAuthUsageReader,
) *QuotaRecoveryChecker {
	return &QuotaRecoveryChecker{
		openAI:    openAI,
		anthropic: anthropic,
	}
}

// Check queries only authoritative, read-only quota endpoints. Unsupported
// providers/account types, incomplete responses, and query failures are
// deliberately reported as unknown.
func (c *QuotaRecoveryChecker) Check(ctx context.Context, account *Account) QuotaRecoveryCheckResult {
	if account == nil || account.ID <= 0 {
		return unknownQuotaRecovery(QuotaRecoverySourceUnsupported, "invalid_account")
	}
	if err := ctx.Err(); err != nil {
		return unknownQuotaRecovery(quotaRecoverySourceForAccount(account), "context_done")
	}
	if account.Type != AccountTypeOAuth {
		return unknownQuotaRecovery(QuotaRecoverySourceUnsupported, "unsupported_account")
	}

	switch account.Platform {
	case PlatformOpenAI:
		return c.checkOpenAI(ctx, account)
	case PlatformAnthropic:
		return c.checkAnthropic(ctx, account)
	default:
		return unknownQuotaRecovery(QuotaRecoverySourceUnsupported, "unsupported_account")
	}
}

func (c *QuotaRecoveryChecker) checkOpenAI(ctx context.Context, account *Account) QuotaRecoveryCheckResult {
	if c == nil || c.openAI == nil {
		return unknownQuotaRecovery(QuotaRecoverySourceOpenAI, "reader_unavailable")
	}

	queryCtx, cancel := context.WithTimeout(ctx, quotaRecoveryCheckTimeout)
	defer cancel()
	usage, err := c.openAI.QueryUsage(queryCtx, account.ID)
	if queryCtx.Err() != nil {
		return unknownQuotaRecovery(QuotaRecoverySourceOpenAI, "context_done")
	}
	if err != nil {
		return unknownQuotaRecovery(QuotaRecoverySourceOpenAI, "query_failed")
	}
	return classifyOpenAIQuotaRecovery(account, usage)
}

func (c *QuotaRecoveryChecker) checkAnthropic(ctx context.Context, account *Account) QuotaRecoveryCheckResult {
	if c == nil || c.anthropic == nil {
		return unknownQuotaRecovery(QuotaRecoverySourceAnthropic, "reader_unavailable")
	}

	queryCtx, cancel := context.WithTimeout(ctx, quotaRecoveryCheckTimeout)
	defer cancel()
	usage, err := c.anthropic.QueryAnthropicOAuthUsage(queryCtx, account)
	if queryCtx.Err() != nil {
		return unknownQuotaRecovery(QuotaRecoverySourceAnthropic, "context_done")
	}
	if err != nil {
		return unknownQuotaRecovery(QuotaRecoverySourceAnthropic, "query_failed")
	}
	return classifyAnthropicQuotaRecovery(account, usage)
}

func classifyOpenAIQuotaRecovery(account *Account, usage *OpenAIQuotaUsage) QuotaRecoveryCheckResult {
	if usage == nil {
		return unknownQuotaRecovery(QuotaRecoverySourceOpenAI, "missing_quota_data")
	}

	if account.QuotaDimensionOrDefault() != QuotaDimensionSpark {
		return classifyOpenAIRateLimits(account.RateLimitResetAt, usage.FetchedAt, []*OpenAIRateLimit{usage.RateLimit})
	}

	limits := make([]*OpenAIRateLimit, 0, 1)
	for i := range usage.AdditionalRateLimits {
		additional := &usage.AdditionalRateLimits[i]
		if additional.MeteredFeature == openAISparkMeteredFeature {
			limits = append(limits, additional.RateLimit)
		}
	}
	if len(limits) == 0 {
		return unknownQuotaRecovery(QuotaRecoverySourceOpenAI, "missing_quota_data")
	}
	return classifyOpenAIRateLimits(account.RateLimitResetAt, usage.FetchedAt, limits)
}

func classifyOpenAIRateLimits(persistedResetAt *time.Time, fetchedAt int64, limits []*OpenAIRateLimit) QuotaRecoveryCheckResult {
	if len(limits) == 0 {
		return unknownQuotaRecovery(QuotaRecoverySourceOpenAI, "missing_quota_data")
	}

	exhausted := false
	providerResets := make([]time.Time, 0, len(limits)*2)
	for _, limit := range limits {
		if limit == nil || !limit.hasQuotaRecoveryEvidence() || limit.PrimaryWindow == nil {
			return unknownQuotaRecovery(QuotaRecoverySourceOpenAI, "missing_quota_data")
		}
		windows := []*OpenAIRateLimitWindow{limit.PrimaryWindow}
		if limit.SecondaryWindow != nil {
			windows = append(windows, limit.SecondaryWindow)
		}
		for _, window := range windows {
			if !validOpenAIQuotaWindow(window) {
				return unknownQuotaRecovery(QuotaRecoverySourceOpenAI, "invalid_quota_data")
			}
			if window.UsedPercent >= quotaRecoveryExhaustionPercent {
				exhausted = true
			}
			if resetAt, ok := openAIQuotaWindowResetAt(window, fetchedAt); ok {
				providerResets = append(providerResets, resetAt)
			}
		}
		if !limit.Allowed || limit.LimitReached {
			exhausted = true
		}
	}

	if exhausted {
		return authoritativeQuotaRecovery(QuotaRecoveryExhausted, QuotaRecoverySourceOpenAI, "quota_exhausted")
	}
	if !matchesPersistedQuotaReset(persistedResetAt, providerResets) {
		return unknownQuotaRecovery(QuotaRecoverySourceOpenAI, "quota_window_mismatch")
	}
	return authoritativeQuotaRecovery(QuotaRecoveryAvailable, QuotaRecoverySourceOpenAI, "quota_available")
}

func validOpenAIQuotaWindow(window *OpenAIRateLimitWindow) bool {
	return window != nil &&
		window.hasQuotaRecoveryEvidence() &&
		window.hasValidQuotaRecoveryReset() &&
		window.LimitWindowSeconds > 0 &&
		validQuotaUtilization(window.UsedPercent)
}

func openAIQuotaWindowResetAt(window *OpenAIRateLimitWindow, fetchedAt int64) (time.Time, bool) {
	if window == nil {
		return time.Time{}, false
	}
	if window.ResetAt > 0 && (!window.decodedFromJSON || window.resetAtPresent) {
		resetUnix := window.ResetAt
		if resetUnix > 1e11 {
			resetUnix /= 1000
		}
		return time.Unix(resetUnix, 0).UTC(), true
	}
	if fetchedAt <= 0 || window.ResetAfterSeconds < 0 ||
		(window.decodedFromJSON && !window.resetAfterSecondsPresent) {
		return time.Time{}, false
	}
	return time.Unix(fetchedAt, 0).Add(time.Duration(window.ResetAfterSeconds) * time.Second).UTC(), true
}

func classifyAnthropicQuotaRecovery(account *Account, usage *ClaudeUsageResponse) QuotaRecoveryCheckResult {
	if usage == nil {
		return unknownQuotaRecovery(QuotaRecoverySourceAnthropic, "missing_quota_data")
	}

	exhausted := false
	providerResets := make([]time.Time, 0, 2)
	accountWindows := []*ClaudeUsageWindow{&usage.FiveHour, &usage.SevenDay}
	for _, window := range accountWindows {
		resetsAt := strings.TrimSpace(window.ResetsAt)
		if resetsAt == "" {
			return unknownQuotaRecovery(QuotaRecoverySourceAnthropic, "missing_quota_data")
		}
		if !window.hasQuotaRecoveryUtilization() {
			return unknownQuotaRecovery(QuotaRecoverySourceAnthropic, "missing_quota_data")
		}
		if !validQuotaUtilization(window.Utilization) {
			return unknownQuotaRecovery(QuotaRecoverySourceAnthropic, "invalid_quota_data")
		}
		resetAt, err := parseTime(resetsAt)
		if err != nil {
			return unknownQuotaRecovery(QuotaRecoverySourceAnthropic, "invalid_quota_data")
		}
		providerResets = append(providerResets, resetAt)
		if window.Utilization >= quotaRecoveryExhaustionPercent {
			exhausted = true
		}
	}

	// The account-wide 429 path persists only 5h/7d. Sonnet and Fable are
	// model-specific windows, so their exhaustion must not hold an account-wide
	// block. A present-but-malformed optional object still makes the response
	// unsuitable as authoritative recovery evidence.
	for _, window := range []*ClaudeUsageWindow{usage.SevenDaySonnet, usage.SevenDayOverageIncluded} {
		if window == nil {
			continue
		}
		resetsAt := strings.TrimSpace(window.ResetsAt)
		if resetsAt == "" {
			return unknownQuotaRecovery(QuotaRecoverySourceAnthropic, "missing_quota_data")
		}
		if !window.hasQuotaRecoveryUtilization() {
			return unknownQuotaRecovery(QuotaRecoverySourceAnthropic, "missing_quota_data")
		}
		if !validQuotaUtilization(window.Utilization) {
			return unknownQuotaRecovery(QuotaRecoverySourceAnthropic, "invalid_quota_data")
		}
		if _, err := parseTime(resetsAt); err != nil {
			return unknownQuotaRecovery(QuotaRecoverySourceAnthropic, "invalid_quota_data")
		}
	}

	if exhausted {
		return authoritativeQuotaRecovery(QuotaRecoveryExhausted, QuotaRecoverySourceAnthropic, "quota_exhausted")
	}
	if account == nil || !matchesPersistedQuotaReset(account.RateLimitResetAt, providerResets) {
		return unknownQuotaRecovery(QuotaRecoverySourceAnthropic, "quota_window_mismatch")
	}
	return authoritativeQuotaRecovery(QuotaRecoveryAvailable, QuotaRecoverySourceAnthropic, "quota_available")
}

func matchesPersistedQuotaReset(persistedResetAt *time.Time, providerResets []time.Time) bool {
	if persistedResetAt == nil || persistedResetAt.IsZero() {
		return false
	}
	for _, providerReset := range providerResets {
		if providerReset.IsZero() {
			continue
		}
		delta := providerReset.Sub(*persistedResetAt)
		if delta < 0 {
			delta = -delta
		}
		if delta <= quotaRecoveryResetTolerance {
			return true
		}
	}
	return false
}

func validQuotaUtilization(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func authoritativeQuotaRecovery(verdict QuotaRecoveryState, source, reason string) QuotaRecoveryCheckResult {
	return QuotaRecoveryCheckResult{
		Verdict:       verdict,
		Authoritative: true,
		CheckedAt:     time.Now().UTC(),
		Source:        source,
		Reason:        reason,
	}
}

func unknownQuotaRecovery(source, reason string) QuotaRecoveryCheckResult {
	return QuotaRecoveryCheckResult{
		Verdict:   QuotaRecoveryUnknown,
		CheckedAt: time.Now().UTC(),
		Source:    source,
		Reason:    reason,
	}
}

func quotaRecoverySourceForAccount(account *Account) string {
	if account == nil {
		return QuotaRecoverySourceUnsupported
	}
	switch account.Platform {
	case PlatformOpenAI:
		return QuotaRecoverySourceOpenAI
	case PlatformAnthropic:
		return QuotaRecoverySourceAnthropic
	default:
		return QuotaRecoverySourceUnsupported
	}
}

// QueryAnthropicOAuthUsage exposes the side-effect-free Anthropic usage path
// needed by QuotaRecoveryChecker. Unlike GetUsage, it does not update passive
// snapshots, clear account errors, add local statistics, or send inference.
func (s *AccountUsageService) QueryAnthropicOAuthUsage(ctx context.Context, account *Account) (*ClaudeUsageResponse, error) {
	if s == nil || s.usageFetcher == nil {
		return nil, fmt.Errorf("anthropic usage reader is not configured")
	}
	if account == nil || account.Platform != PlatformAnthropic || account.Type != AccountTypeOAuth {
		return nil, fmt.Errorf("account is not an Anthropic OAuth account")
	}
	if account.IsTLSFingerprintEnabled() && s.tlsFPProfileService == nil {
		return nil, fmt.Errorf("TLS fingerprint profile service is not configured")
	}
	return s.fetchOAuthUsageRaw(ctx, account)
}

var _ OpenAIQuotaUsageReader = (*OpenAIQuotaService)(nil)
var _ AnthropicOAuthUsageReader = (*AccountUsageService)(nil)
