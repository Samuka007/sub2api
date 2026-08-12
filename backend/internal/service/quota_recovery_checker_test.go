package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"
)

var (
	quotaRecoveryTestFiveHourReset = time.Date(2030, 1, 1, 5, 0, 0, 0, time.UTC)
	quotaRecoveryTestSevenDayReset = time.Date(2030, 1, 8, 0, 0, 0, 0, time.UTC)
)

type quotaRecoveryOpenAIReaderStub struct {
	usage     *OpenAIQuotaUsage
	err       error
	calls     int
	accountID int64
}

func (s *quotaRecoveryOpenAIReaderStub) QueryUsageStrict(_ context.Context, accountID int64) (*OpenAIQuotaUsage, error) {
	s.calls++
	s.accountID = accountID
	return s.usage, s.err
}

type quotaRecoveryOpenAIResetClientStub struct {
	usages          []*OpenAIQuotaUsage
	queryErrors     []error
	queryCalls      int
	resetErr        error
	resetCalls      int
	resetAccountIDs []int64
	resetRequestIDs []string
}

func (s *quotaRecoveryOpenAIResetClientStub) QueryUsageStrict(_ context.Context, _ int64) (*OpenAIQuotaUsage, error) {
	call := s.queryCalls
	s.queryCalls++
	if call < len(s.queryErrors) && s.queryErrors[call] != nil {
		return nil, s.queryErrors[call]
	}
	if len(s.usages) == 0 {
		return nil, nil
	}
	if call >= len(s.usages) {
		call = len(s.usages) - 1
	}
	return s.usages[call], nil
}

type quotaRecoveryOpenAIQueryModeStub struct {
	usage           *OpenAIQuotaUsage
	strictErr       error
	bestEffortCalls int
	strictCalls     int
}

func (s *quotaRecoveryOpenAIQueryModeStub) QueryUsage(_ context.Context, _ int64) (*OpenAIQuotaUsage, error) {
	s.bestEffortCalls++
	return s.usage, nil
}

func (s *quotaRecoveryOpenAIQueryModeStub) QueryUsageStrict(_ context.Context, _ int64) (*OpenAIQuotaUsage, error) {
	s.strictCalls++
	return nil, s.strictErr
}

func (s *quotaRecoveryOpenAIResetClientStub) ResetCreditWithRequestID(
	_ context.Context,
	accountID int64,
	requestID string,
) (*OpenAIQuotaResetResult, error) {
	s.resetCalls++
	s.resetAccountIDs = append(s.resetAccountIDs, accountID)
	s.resetRequestIDs = append(s.resetRequestIDs, requestID)
	if s.resetErr != nil {
		return nil, s.resetErr
	}
	return &OpenAIQuotaResetResult{Code: "success", WindowsReset: 2}, nil
}

type quotaRecoveryAnthropicReaderStub struct {
	usage   *ClaudeUsageResponse
	err     error
	calls   int
	account *Account
}

func (s *quotaRecoveryAnthropicReaderStub) QueryAnthropicOAuthUsage(_ context.Context, account *Account) (*ClaudeUsageResponse, error) {
	s.calls++
	s.account = account
	return s.usage, s.err
}

func TestQuotaRecoveryChecker_OpenAIGlobal(t *testing.T) {
	tests := []struct {
		name  string
		usage *OpenAIQuotaUsage
		err   error
		want  QuotaRecoveryState
	}{
		{
			name:  "all reported windows available",
			usage: &OpenAIQuotaUsage{RateLimit: openAIRateLimitForRecovery(true, false, 35, quotaRecoveryFloat(99.9))},
			want:  QuotaRecoveryAvailable,
		},
		{
			name:  "primary window exhausted",
			usage: &OpenAIQuotaUsage{RateLimit: openAIRateLimitForRecovery(true, false, 100, nil)},
			want:  QuotaRecoveryExhausted,
		},
		{
			name:  "secondary window exhausted",
			usage: &OpenAIQuotaUsage{RateLimit: openAIRateLimitForRecovery(true, false, 12, quotaRecoveryFloat(100))},
			want:  QuotaRecoveryExhausted,
		},
		{
			name:  "upstream disallows account",
			usage: &OpenAIQuotaUsage{RateLimit: openAIRateLimitForRecovery(false, false, 20, nil)},
			want:  QuotaRecoveryExhausted,
		},
		{
			name:  "upstream reports limit reached",
			usage: &OpenAIQuotaUsage{RateLimit: openAIRateLimitForRecovery(true, true, 20, nil)},
			want:  QuotaRecoveryExhausted,
		},
		{
			name: "query error",
			err:  errors.New("upstream unavailable"),
			want: QuotaRecoveryUnknown,
		},
		{
			name: "nil response",
			want: QuotaRecoveryUnknown,
		},
		{
			name:  "missing rate limit",
			usage: &OpenAIQuotaUsage{},
			want:  QuotaRecoveryUnknown,
		},
		{
			name:  "missing primary window",
			usage: &OpenAIQuotaUsage{RateLimit: &OpenAIRateLimit{Allowed: true}},
			want:  QuotaRecoveryUnknown,
		},
		{
			name: "malformed primary window",
			usage: &OpenAIQuotaUsage{RateLimit: &OpenAIRateLimit{
				Allowed:       true,
				PrimaryWindow: &OpenAIRateLimitWindow{UsedPercent: 10},
			}},
			want: QuotaRecoveryUnknown,
		},
		{
			name: "invalid utilization",
			usage: &OpenAIQuotaUsage{RateLimit: &OpenAIRateLimit{
				Allowed:       true,
				PrimaryWindow: openAIWindowForRecovery(math.NaN(), 18_000),
			}},
			want: QuotaRecoveryUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := &quotaRecoveryOpenAIReaderStub{usage: tt.usage, err: tt.err}
			checker := NewQuotaRecoveryChecker(reader, nil)
			account := &Account{
				ID:               41,
				Platform:         PlatformOpenAI,
				Type:             AccountTypeOAuth,
				RateLimitResetAt: quotaRecoveryTime(quotaRecoveryTestFiveHourReset),
			}

			got := checker.Check(context.Background(), account)

			assertQuotaRecoveryState(t, got, tt.want)
			if reader.calls != 1 || reader.accountID != account.ID {
				t.Fatalf("QueryUsageStrict calls = %d, account ID = %d", reader.calls, reader.accountID)
			}
		})
	}
}

func TestQuotaRecoveryChecker_OpenAIUsesStrictUsageQuery(t *testing.T) {
	reader := &quotaRecoveryOpenAIQueryModeStub{
		usage: &OpenAIQuotaUsage{
			RateLimit: openAIRateLimitForRecovery(true, false, 0, nil),
		},
		strictErr: errors.New("reset-credit details unavailable"),
	}
	account := &Account{
		ID:               43,
		Platform:         PlatformOpenAI,
		Type:             AccountTypeOAuth,
		RateLimitResetAt: quotaRecoveryTime(quotaRecoveryTestFiveHourReset),
	}

	got := NewQuotaRecoveryChecker(reader, nil).Check(context.Background(), account)

	assertQuotaRecoveryState(t, got, QuotaRecoveryUnknown)
	if got.Reason != "query_failed" {
		t.Fatalf("reason = %q, want query_failed", got.Reason)
	}
	if reader.strictCalls != 1 || reader.bestEffortCalls != 0 {
		t.Fatalf("query calls: strict=%d best-effort=%d", reader.strictCalls, reader.bestEffortCalls)
	}
}

func TestQuotaRecoveryChecker_OpenAIJSONUsedPercentPresence(t *testing.T) {
	tests := []struct {
		name               string
		includeUsedPercent bool
		usedPercent        any
		want               QuotaRecoveryState
	}{
		{
			name: "missing used_percent is unknown",
			want: QuotaRecoveryUnknown,
		},
		{
			name:               "null used_percent is unknown",
			includeUsedPercent: true,
			usedPercent:        nil,
			want:               QuotaRecoveryUnknown,
		},
		{
			name:               "explicit zero used_percent is available",
			includeUsedPercent: true,
			usedPercent:        0,
			want:               QuotaRecoveryAvailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			window := map[string]any{
				"limit_window_seconds": 18_000,
				"reset_after_seconds":  60,
				"reset_at":             quotaRecoveryTestFiveHourReset.Unix(),
			}
			if tt.includeUsedPercent {
				window["used_percent"] = tt.usedPercent
			}
			payload, err := json.Marshal(map[string]any{
				"rate_limit": map[string]any{
					"allowed":        true,
					"limit_reached":  false,
					"primary_window": window,
				},
			})
			if err != nil {
				t.Fatalf("marshal quota payload: %v", err)
			}

			var usage OpenAIQuotaUsage
			if err := json.Unmarshal(payload, &usage); err != nil {
				t.Fatalf("unmarshal quota payload: %v", err)
			}
			reader := &quotaRecoveryOpenAIReaderStub{usage: &usage}
			account := &Account{
				ID:               44,
				Platform:         PlatformOpenAI,
				Type:             AccountTypeOAuth,
				RateLimitResetAt: quotaRecoveryTime(quotaRecoveryTestFiveHourReset),
			}

			assertQuotaRecoveryState(t, NewQuotaRecoveryChecker(reader, nil).Check(context.Background(), account), tt.want)
		})
	}
}

func TestQuotaRecoveryChecker_OpenAIJSONEnvelopePresence(t *testing.T) {
	tests := []struct {
		name                string
		includeAllowed      bool
		allowed             any
		includeLimitReached bool
		limitReached        any
		want                QuotaRecoveryState
	}{
		{
			name:                "missing allowed is unknown",
			includeLimitReached: true,
			limitReached:        false,
			want:                QuotaRecoveryUnknown,
		},
		{
			name:                "null allowed is unknown",
			includeAllowed:      true,
			allowed:             nil,
			includeLimitReached: true,
			limitReached:        false,
			want:                QuotaRecoveryUnknown,
		},
		{
			name:           "missing limit_reached is unknown",
			includeAllowed: true,
			allowed:        true,
			want:           QuotaRecoveryUnknown,
		},
		{
			name:                "null limit_reached is unknown",
			includeAllowed:      true,
			allowed:             true,
			includeLimitReached: true,
			limitReached:        nil,
			want:                QuotaRecoveryUnknown,
		},
		{
			name:                "explicit envelope fields are available",
			includeAllowed:      true,
			allowed:             true,
			includeLimitReached: true,
			limitReached:        false,
			want:                QuotaRecoveryAvailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rateLimit := map[string]any{
				"primary_window": map[string]any{
					"used_percent":         0,
					"limit_window_seconds": 18_000,
					"reset_after_seconds":  60,
					"reset_at":             quotaRecoveryTestFiveHourReset.Unix(),
				},
			}
			if tt.includeAllowed {
				rateLimit["allowed"] = tt.allowed
			}
			if tt.includeLimitReached {
				rateLimit["limit_reached"] = tt.limitReached
			}
			payload, err := json.Marshal(map[string]any{"rate_limit": rateLimit})
			if err != nil {
				t.Fatalf("marshal quota payload: %v", err)
			}

			var usage OpenAIQuotaUsage
			if err := json.Unmarshal(payload, &usage); err != nil {
				t.Fatalf("unmarshal quota payload: %v", err)
			}
			reader := &quotaRecoveryOpenAIReaderStub{usage: &usage}
			account := &Account{
				ID:               45,
				Platform:         PlatformOpenAI,
				Type:             AccountTypeOAuth,
				RateLimitResetAt: quotaRecoveryTime(quotaRecoveryTestFiveHourReset),
			}

			assertQuotaRecoveryState(t, NewQuotaRecoveryChecker(reader, nil).Check(context.Background(), account), tt.want)
		})
	}
}

func TestQuotaRecoveryChecker_OpenAIJSONResetPresenceAndValidity(t *testing.T) {
	fetchedAt := quotaRecoveryTestFiveHourReset.Add(-time.Minute)
	tests := []struct {
		name              string
		includeResetAt    bool
		resetAt           any
		includeResetAfter bool
		resetAfter        any
		persistedResetAt  time.Time
		want              QuotaRecoveryState
	}{
		{name: "missing reset fields is unknown", persistedResetAt: fetchedAt, want: QuotaRecoveryUnknown},
		{name: "null reset_at is unknown", includeResetAt: true, resetAt: nil, persistedResetAt: fetchedAt, want: QuotaRecoveryUnknown},
		{name: "null reset_after is unknown", includeResetAfter: true, resetAfter: nil, persistedResetAt: fetchedAt, want: QuotaRecoveryUnknown},
		{name: "zero reset_at cannot fall back to absent reset_after", includeResetAt: true, resetAt: 0, persistedResetAt: fetchedAt, want: QuotaRecoveryUnknown},
		{name: "negative reset_at cannot fall back to absent reset_after", includeResetAt: true, resetAt: -1, persistedResetAt: fetchedAt, want: QuotaRecoveryUnknown},
		{name: "negative reset_after is unknown", includeResetAfter: true, resetAfter: -1, persistedResetAt: fetchedAt, want: QuotaRecoveryUnknown},
		{name: "valid reset_at is available", includeResetAt: true, resetAt: quotaRecoveryTestFiveHourReset.Unix(), persistedResetAt: quotaRecoveryTestFiveHourReset, want: QuotaRecoveryAvailable},
		{name: "valid reset_after is available", includeResetAfter: true, resetAfter: 60, persistedResetAt: quotaRecoveryTestFiveHourReset, want: QuotaRecoveryAvailable},
		{name: "invalid reset_at makes mixed reset evidence unknown", includeResetAt: true, resetAt: 0, includeResetAfter: true, resetAfter: 60, persistedResetAt: quotaRecoveryTestFiveHourReset, want: QuotaRecoveryUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			window := map[string]any{
				"used_percent":         0,
				"limit_window_seconds": 18_000,
			}
			if tt.includeResetAt {
				window["reset_at"] = tt.resetAt
			}
			if tt.includeResetAfter {
				window["reset_after_seconds"] = tt.resetAfter
			}
			payload, err := json.Marshal(map[string]any{
				"fetched_at": fetchedAt.Unix(),
				"rate_limit": map[string]any{
					"allowed":        true,
					"limit_reached":  false,
					"primary_window": window,
				},
			})
			if err != nil {
				t.Fatalf("marshal quota payload: %v", err)
			}

			var usage OpenAIQuotaUsage
			if err := json.Unmarshal(payload, &usage); err != nil {
				t.Fatalf("unmarshal quota payload: %v", err)
			}
			reader := &quotaRecoveryOpenAIReaderStub{usage: &usage}
			account := &Account{
				ID:               46,
				Platform:         PlatformOpenAI,
				Type:             AccountTypeOAuth,
				RateLimitResetAt: &tt.persistedResetAt,
			}

			assertQuotaRecoveryState(t, NewQuotaRecoveryChecker(reader, nil).Check(context.Background(), account), tt.want)
		})
	}
}

func TestQuotaRecoveryChecker_OpenAIRequiresMatchingAccountWindow(t *testing.T) {
	matchingUsage := &OpenAIQuotaUsage{
		FetchedAt: quotaRecoveryTestFiveHourReset.Add(-time.Minute).Unix(),
		RateLimit: openAIRateLimitForRecovery(true, false, 20, nil),
	}
	derivedUsage := &OpenAIQuotaUsage{
		FetchedAt: quotaRecoveryTestFiveHourReset.Add(-time.Minute).Unix(),
		RateLimit: &OpenAIRateLimit{
			Allowed: true,
			PrimaryWindow: &OpenAIRateLimitWindow{
				UsedPercent:        20,
				LimitWindowSeconds: 18_000,
				ResetAfterSeconds:  60,
			},
		},
	}
	tests := []struct {
		name    string
		usage   *OpenAIQuotaUsage
		resetAt *time.Time
		want    QuotaRecoveryState
	}{
		{
			name:    "persisted reset matches provider reset_at",
			usage:   matchingUsage,
			resetAt: quotaRecoveryTime(quotaRecoveryTestFiveHourReset),
			want:    QuotaRecoveryAvailable,
		},
		{
			name:    "reset_after derived reset matches",
			usage:   derivedUsage,
			resetAt: quotaRecoveryTime(quotaRecoveryTestFiveHourReset),
			want:    QuotaRecoveryAvailable,
		},
		{
			name:    "small reset timing difference is accepted",
			usage:   matchingUsage,
			resetAt: quotaRecoveryTime(quotaRecoveryTestFiveHourReset.Add(30 * time.Second)),
			want:    QuotaRecoveryAvailable,
		},
		{
			name:    "fallback reset does not match provider window",
			usage:   matchingUsage,
			resetAt: quotaRecoveryTime(quotaRecoveryTestFiveHourReset.Add(-10 * time.Minute)),
			want:    QuotaRecoveryUnknown,
		},
		{
			name:  "missing persisted reset",
			usage: matchingUsage,
			want:  QuotaRecoveryUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := &quotaRecoveryOpenAIReaderStub{usage: tt.usage}
			account := &Account{
				ID:               43,
				Platform:         PlatformOpenAI,
				Type:             AccountTypeOAuth,
				RateLimitResetAt: tt.resetAt,
			}
			assertQuotaRecoveryState(t, NewQuotaRecoveryChecker(reader, nil).Check(context.Background(), account), tt.want)
		})
	}
}

func TestQuotaRecoveryChecker_OpenAIResetCreditRecovery(t *testing.T) {
	limitedAt := quotaRecoveryTestFiveHourReset.Add(-time.Hour)
	account := &Account{
		ID:               47,
		Platform:         PlatformOpenAI,
		Type:             AccountTypeOAuth,
		RateLimitedAt:    &limitedAt,
		RateLimitResetAt: quotaRecoveryTime(quotaRecoveryTestFiveHourReset),
	}
	exhaustedUsage := &OpenAIQuotaUsage{
		FetchedAt: quotaRecoveryTestFiveHourReset.Add(-time.Minute).Unix(),
		RateLimit: openAIRateLimitForRecovery(false, true, 100, quotaRecoveryFloat(100)),
		RateLimitResetCredits: &OpenAIRateLimitResetCredits{
			AvailableCount: 1,
		},
	}
	postResetUsage := &OpenAIQuotaUsage{
		FetchedAt: quotaRecoveryTestFiveHourReset.Unix(),
		RateLimit: openAIRateLimitForRecovery(true, false, 0, quotaRecoveryFloat(0)),
	}
	postResetWindow := quotaRecoveryTestFiveHourReset.Add(7 * 24 * time.Hour).Unix()
	postResetUsage.RateLimit.PrimaryWindow.ResetAt = postResetWindow
	postResetUsage.RateLimit.SecondaryWindow.ResetAt = postResetWindow
	client := &quotaRecoveryOpenAIResetClientStub{usages: []*OpenAIQuotaUsage{exhaustedUsage, postResetUsage}}

	got := NewQuotaRecoveryChecker(client, nil).Check(context.Background(), account)

	assertQuotaRecoveryState(t, got, QuotaRecoveryAvailable)
	if got.Reason != "quota_reset_credit_recovered" {
		t.Fatalf("decision reason = %q, want quota_reset_credit_recovered", got.Reason)
	}
	if client.queryCalls != 2 || client.resetCalls != 1 {
		t.Fatalf("query calls = %d, reset calls = %d", client.queryCalls, client.resetCalls)
	}
	if client.resetAccountIDs[0] != account.ID || len(client.resetRequestIDs[0]) != 36 {
		t.Fatalf("unexpected reset call: account IDs = %v, request IDs = %v", client.resetAccountIDs, client.resetRequestIDs)
	}
}

func TestQuotaRecoveryChecker_OpenAIResetCreditFailuresStayLimited(t *testing.T) {
	limitedAt := quotaRecoveryTestFiveHourReset.Add(-time.Hour)
	baseAccount := Account{
		ID:               48,
		Platform:         PlatformOpenAI,
		Type:             AccountTypeOAuth,
		RateLimitedAt:    &limitedAt,
		RateLimitResetAt: quotaRecoveryTime(quotaRecoveryTestFiveHourReset),
	}
	exhaustedUsage := func(credits int) *OpenAIQuotaUsage {
		return &OpenAIQuotaUsage{
			FetchedAt: quotaRecoveryTestFiveHourReset.Add(-time.Minute).Unix(),
			RateLimit: openAIRateLimitForRecovery(false, true, 100, quotaRecoveryFloat(100)),
			RateLimitResetCredits: &OpenAIRateLimitResetCredits{
				AvailableCount: credits,
			},
		}
	}

	t.Run("no reset credit remains exhausted", func(t *testing.T) {
		client := &quotaRecoveryOpenAIResetClientStub{usages: []*OpenAIQuotaUsage{exhaustedUsage(0)}}
		got := NewQuotaRecoveryChecker(client, nil).Check(context.Background(), &baseAccount)
		assertQuotaRecoveryState(t, got, QuotaRecoveryExhausted)
		if client.resetCalls != 0 || client.queryCalls != 1 {
			t.Fatalf("query calls = %d, reset calls = %d", client.queryCalls, client.resetCalls)
		}
	})

	t.Run("mismatched quota window does not consume credit", func(t *testing.T) {
		account := baseAccount
		account.RateLimitResetAt = quotaRecoveryTime(quotaRecoveryTestFiveHourReset.Add(-10 * time.Minute))
		client := &quotaRecoveryOpenAIResetClientStub{usages: []*OpenAIQuotaUsage{exhaustedUsage(1)}}
		got := NewQuotaRecoveryChecker(client, nil).Check(context.Background(), &account)
		assertQuotaRecoveryState(t, got, QuotaRecoveryUnknown)
		if got.Reason != "quota_window_mismatch" || client.resetCalls != 0 {
			t.Fatalf("reason = %q, reset calls = %d", got.Reason, client.resetCalls)
		}
	})

	t.Run("reset failure is unknown and reuses request ID", func(t *testing.T) {
		client := &quotaRecoveryOpenAIResetClientStub{
			usages:   []*OpenAIQuotaUsage{exhaustedUsage(1)},
			resetErr: errors.New("ambiguous reset failure"),
		}
		checker := NewQuotaRecoveryChecker(client, nil)
		first := checker.Check(context.Background(), &baseAccount)
		second := checker.Check(context.Background(), &baseAccount)
		assertQuotaRecoveryState(t, first, QuotaRecoveryUnknown)
		assertQuotaRecoveryState(t, second, QuotaRecoveryUnknown)
		if first.Reason != "reset_credit_failed" || second.Reason != "reset_credit_failed" {
			t.Fatalf("reset failure reasons = %q, %q", first.Reason, second.Reason)
		}
		if client.resetCalls != 2 || client.resetRequestIDs[0] != client.resetRequestIDs[1] {
			t.Fatalf("reset request IDs = %v", client.resetRequestIDs)
		}
		laterObservation := baseAccount
		laterLimitedAt := limitedAt.Add(time.Nanosecond)
		laterObservation.RateLimitedAt = &laterLimitedAt
		laterRequestID, ok := quotaRecoveryRedeemRequestID(&laterObservation)
		if !ok || laterRequestID == client.resetRequestIDs[0] {
			t.Fatalf("later observation request ID = %q, previous = %q", laterRequestID, client.resetRequestIDs[0])
		}
	})

	t.Run("post reset query failure is unknown", func(t *testing.T) {
		client := &quotaRecoveryOpenAIResetClientStub{
			usages:      []*OpenAIQuotaUsage{exhaustedUsage(1)},
			queryErrors: []error{nil, errors.New("post-reset query failed")},
		}
		got := NewQuotaRecoveryChecker(client, nil).Check(context.Background(), &baseAccount)
		assertQuotaRecoveryState(t, got, QuotaRecoveryUnknown)
		if got.Reason != "post_reset_query_failed" || client.resetCalls != 1 {
			t.Fatalf("reason = %q, reset calls = %d", got.Reason, client.resetCalls)
		}
	})
}

func TestQuotaRecoveryChecker_OpenAISparkDoesNotConsumeGlobalResetCredit(t *testing.T) {
	limitedAt := quotaRecoveryTestFiveHourReset.Add(-time.Hour)
	parentID := int64(49)
	account := &Account{
		ID:               50,
		Platform:         PlatformOpenAI,
		Type:             AccountTypeOAuth,
		ParentAccountID:  &parentID,
		QuotaDimension:   QuotaDimensionSpark,
		RateLimitedAt:    &limitedAt,
		RateLimitResetAt: quotaRecoveryTime(quotaRecoveryTestFiveHourReset),
	}
	usage := &OpenAIQuotaUsage{
		RateLimit: openAIRateLimitForRecovery(true, false, 10, nil),
		AdditionalRateLimits: []OpenAIAdditionalRateLimit{{
			MeteredFeature: openAISparkMeteredFeature,
			RateLimit:      openAIRateLimitForRecovery(false, true, 100, nil),
		}},
		RateLimitResetCredits: &OpenAIRateLimitResetCredits{AvailableCount: 1},
	}
	client := &quotaRecoveryOpenAIResetClientStub{usages: []*OpenAIQuotaUsage{usage}}

	got := NewQuotaRecoveryChecker(client, nil).Check(context.Background(), account)

	assertQuotaRecoveryState(t, got, QuotaRecoveryExhausted)
	if client.resetCalls != 0 {
		t.Fatalf("reset calls = %d, want 0", client.resetCalls)
	}
}

func TestQuotaRecoveryChecker_OpenAISparkUsesOnlySparkDimension(t *testing.T) {
	tests := []struct {
		name       string
		additional []OpenAIAdditionalRateLimit
		want       QuotaRecoveryState
	}{
		{
			name: "spark available despite exhausted global quota",
			additional: []OpenAIAdditionalRateLimit{{
				MeteredFeature: openAISparkMeteredFeature,
				RateLimit:      openAIRateLimitForRecovery(true, false, 10, quotaRecoveryFloat(20)),
			}},
			want: QuotaRecoveryAvailable,
		},
		{
			name: "spark exhausted despite available global quota",
			additional: []OpenAIAdditionalRateLimit{{
				MeteredFeature: openAISparkMeteredFeature,
				RateLimit:      openAIRateLimitForRecovery(true, false, 100, nil),
			}},
			want: QuotaRecoveryExhausted,
		},
		{
			name: "missing spark dimension",
			additional: []OpenAIAdditionalRateLimit{{
				MeteredFeature: "some_other_feature",
				RateLimit:      openAIRateLimitForRecovery(true, false, 1, nil),
			}},
			want: QuotaRecoveryUnknown,
		},
		{
			name: "spark dimension missing quota",
			additional: []OpenAIAdditionalRateLimit{{
				MeteredFeature: openAISparkMeteredFeature,
			}},
			want: QuotaRecoveryUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := &quotaRecoveryOpenAIReaderStub{usage: &OpenAIQuotaUsage{
				RateLimit:            openAIRateLimitForRecovery(true, false, 100, nil),
				AdditionalRateLimits: tt.additional,
			}}
			checker := NewQuotaRecoveryChecker(reader, nil)
			account := &Account{
				ID:               42,
				Platform:         PlatformOpenAI,
				Type:             AccountTypeOAuth,
				QuotaDimension:   QuotaDimensionSpark,
				RateLimitResetAt: quotaRecoveryTime(quotaRecoveryTestFiveHourReset),
			}

			assertQuotaRecoveryState(t, checker.Check(context.Background(), account), tt.want)
		})
	}
}

func TestQuotaRecoveryChecker_AnthropicOAuth(t *testing.T) {
	available := anthropicUsageForRecovery(20, 80)
	available.SevenDaySonnet = &ClaudeUsageWindow{
		Utilization: 90,
		ResetsAt:    quotaRecoveryTestSevenDayReset.Format(time.RFC3339),
	}
	available.SevenDayOverageIncluded = &ClaudeUsageWindow{
		Utilization: 75,
		ResetsAt:    quotaRecoveryTestSevenDayReset.Format(time.RFC3339),
	}

	exhaustedSonnet := anthropicUsageForRecovery(20, 80)
	exhaustedSonnet.SevenDaySonnet = &ClaudeUsageWindow{
		Utilization: 100,
		ResetsAt:    quotaRecoveryTestSevenDayReset.Format(time.RFC3339),
	}

	exhaustedFable := anthropicUsageForRecovery(20, 80)
	exhaustedFable.SevenDayOverageIncluded = &ClaudeUsageWindow{
		Utilization: 100,
		ResetsAt:    quotaRecoveryTestSevenDayReset.Format(time.RFC3339),
	}

	missingRequired := anthropicUsageForRecovery(20, 80)
	missingRequired.SevenDay.ResetsAt = ""

	malformedOptional := anthropicUsageForRecovery(20, 80)
	malformedOptional.SevenDayOverageIncluded = &ClaudeUsageWindow{Utilization: 25}

	emptyOptional := anthropicUsageForRecovery(20, 80)
	emptyOptional.SevenDaySonnet = &ClaudeUsageWindow{}

	invalidReset := anthropicUsageForRecovery(20, 80)
	invalidReset.FiveHour.ResetsAt = "not-a-time"

	invalidUtilization := anthropicUsageForRecovery(20, 80)
	invalidUtilization.FiveHour.Utilization = math.Inf(1)

	tests := []struct {
		name  string
		usage *ClaudeUsageResponse
		err   error
		want  QuotaRecoveryState
	}{
		{name: "all relevant windows available", usage: available, want: QuotaRecoveryAvailable},
		{name: "required window exhausted", usage: anthropicUsageForRecovery(100, 80), want: QuotaRecoveryExhausted},
		{name: "Sonnet model window does not block account recovery", usage: exhaustedSonnet, want: QuotaRecoveryAvailable},
		{name: "Fable model window does not block account recovery", usage: exhaustedFable, want: QuotaRecoveryAvailable},
		{name: "required window missing", usage: missingRequired, want: QuotaRecoveryUnknown},
		{name: "partial optional window", usage: malformedOptional, want: QuotaRecoveryUnknown},
		{name: "empty optional window", usage: emptyOptional, want: QuotaRecoveryUnknown},
		{name: "invalid reset time", usage: invalidReset, want: QuotaRecoveryUnknown},
		{name: "invalid utilization", usage: invalidUtilization, want: QuotaRecoveryUnknown},
		{name: "nil response", want: QuotaRecoveryUnknown},
		{name: "query error", err: errors.New("timeout"), want: QuotaRecoveryUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := &quotaRecoveryAnthropicReaderStub{usage: tt.usage, err: tt.err}
			checker := NewQuotaRecoveryChecker(nil, reader)
			account := &Account{
				ID:               51,
				Platform:         PlatformAnthropic,
				Type:             AccountTypeOAuth,
				RateLimitResetAt: quotaRecoveryTime(quotaRecoveryTestFiveHourReset),
			}

			got := checker.Check(context.Background(), account)

			assertQuotaRecoveryState(t, got, tt.want)
			if reader.calls != 1 || reader.account != account {
				t.Fatalf("QueryAnthropicOAuthUsage calls = %d, account = %p", reader.calls, reader.account)
			}
		})
	}
}

func TestQuotaRecoveryChecker_AnthropicJSONUtilizationPresence(t *testing.T) {
	tests := []struct {
		name               string
		windowName         string
		includeUtilization bool
		utilization        any
		want               QuotaRecoveryState
	}{
		{
			name:       "missing five hour utilization is unknown",
			windowName: "five_hour",
			want:       QuotaRecoveryUnknown,
		},
		{
			name:               "null five hour utilization is unknown",
			windowName:         "five_hour",
			includeUtilization: true,
			utilization:        nil,
			want:               QuotaRecoveryUnknown,
		},
		{
			name:               "explicit zero five hour utilization is available",
			windowName:         "five_hour",
			includeUtilization: true,
			utilization:        0,
			want:               QuotaRecoveryAvailable,
		},
		{
			name:       "missing seven day utilization is unknown",
			windowName: "seven_day",
			want:       QuotaRecoveryUnknown,
		},
		{
			name:               "null seven day utilization is unknown",
			windowName:         "seven_day",
			includeUtilization: true,
			utilization:        nil,
			want:               QuotaRecoveryUnknown,
		},
		{
			name:               "explicit zero seven day utilization is available",
			windowName:         "seven_day",
			includeUtilization: true,
			utilization:        0,
			want:               QuotaRecoveryAvailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			windows := map[string]map[string]any{
				"five_hour": {
					"utilization": 20,
					"resets_at":   quotaRecoveryTestFiveHourReset.Format(time.RFC3339),
				},
				"seven_day": {
					"utilization": 80,
					"resets_at":   quotaRecoveryTestSevenDayReset.Format(time.RFC3339),
				},
			}
			delete(windows[tt.windowName], "utilization")
			if tt.includeUtilization {
				windows[tt.windowName]["utilization"] = tt.utilization
			}
			payload, err := json.Marshal(windows)
			if err != nil {
				t.Fatalf("marshal usage payload: %v", err)
			}

			var usage ClaudeUsageResponse
			if err := json.Unmarshal(payload, &usage); err != nil {
				t.Fatalf("unmarshal usage payload: %v", err)
			}
			reader := &quotaRecoveryAnthropicReaderStub{usage: &usage}
			account := &Account{
				ID:               53,
				Platform:         PlatformAnthropic,
				Type:             AccountTypeOAuth,
				RateLimitResetAt: quotaRecoveryTime(quotaRecoveryTestFiveHourReset),
			}

			assertQuotaRecoveryState(t, NewQuotaRecoveryChecker(nil, reader).Check(context.Background(), account), tt.want)
		})
	}
}

func TestQuotaRecoveryChecker_AnthropicOptionalJSONUtilizationPresence(t *testing.T) {
	tests := []struct {
		name               string
		windowName         string
		includeUtilization bool
		utilization        any
		want               QuotaRecoveryState
	}{
		{
			name:       "missing Sonnet utilization is unknown",
			windowName: "seven_day_sonnet",
			want:       QuotaRecoveryUnknown,
		},
		{
			name:               "null Sonnet utilization is unknown",
			windowName:         "seven_day_sonnet",
			includeUtilization: true,
			utilization:        nil,
			want:               QuotaRecoveryUnknown,
		},
		{
			name:               "explicit zero Sonnet utilization is available",
			windowName:         "seven_day_sonnet",
			includeUtilization: true,
			utilization:        0,
			want:               QuotaRecoveryAvailable,
		},
		{
			name:       "missing Fable utilization is unknown",
			windowName: "seven_day_overage_included",
			want:       QuotaRecoveryUnknown,
		},
		{
			name:               "null Fable utilization is unknown",
			windowName:         "seven_day_overage_included",
			includeUtilization: true,
			utilization:        nil,
			want:               QuotaRecoveryUnknown,
		},
		{
			name:               "explicit zero Fable utilization is available",
			windowName:         "seven_day_overage_included",
			includeUtilization: true,
			utilization:        0,
			want:               QuotaRecoveryAvailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			optionalWindow := map[string]any{
				"resets_at": quotaRecoveryTestSevenDayReset.Format(time.RFC3339),
			}
			if tt.includeUtilization {
				optionalWindow["utilization"] = tt.utilization
			}
			payload, err := json.Marshal(map[string]any{
				"five_hour": map[string]any{
					"utilization": 20,
					"resets_at":   quotaRecoveryTestFiveHourReset.Format(time.RFC3339),
				},
				"seven_day": map[string]any{
					"utilization": 80,
					"resets_at":   quotaRecoveryTestSevenDayReset.Format(time.RFC3339),
				},
				tt.windowName: optionalWindow,
			})
			if err != nil {
				t.Fatalf("marshal usage payload: %v", err)
			}

			var usage ClaudeUsageResponse
			if err := json.Unmarshal(payload, &usage); err != nil {
				t.Fatalf("unmarshal usage payload: %v", err)
			}
			reader := &quotaRecoveryAnthropicReaderStub{usage: &usage}
			account := &Account{
				ID:               54,
				Platform:         PlatformAnthropic,
				Type:             AccountTypeOAuth,
				RateLimitResetAt: quotaRecoveryTime(quotaRecoveryTestFiveHourReset),
			}

			assertQuotaRecoveryState(t, NewQuotaRecoveryChecker(nil, reader).Check(context.Background(), account), tt.want)
		})
	}
}

func TestQuotaRecoveryChecker_AnthropicRequiresMatchingAccountWindow(t *testing.T) {
	tests := []struct {
		name    string
		resetAt *time.Time
		want    QuotaRecoveryState
	}{
		{
			name:    "persisted reset matches five hour window",
			resetAt: quotaRecoveryTime(quotaRecoveryTestFiveHourReset),
			want:    QuotaRecoveryAvailable,
		},
		{
			name:    "persisted reset matches seven day window",
			resetAt: quotaRecoveryTime(quotaRecoveryTestSevenDayReset),
			want:    QuotaRecoveryAvailable,
		},
		{
			name:    "small reset timing difference is accepted",
			resetAt: quotaRecoveryTime(quotaRecoveryTestFiveHourReset.Add(-15 * time.Second)),
			want:    QuotaRecoveryAvailable,
		},
		{
			name:    "fallback reset does not match provider window",
			resetAt: quotaRecoveryTime(quotaRecoveryTestFiveHourReset.Add(-10 * time.Minute)),
			want:    QuotaRecoveryUnknown,
		},
		{
			name: "missing persisted reset",
			want: QuotaRecoveryUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := &quotaRecoveryAnthropicReaderStub{usage: anthropicUsageForRecovery(20, 80)}
			account := &Account{
				ID:               52,
				Platform:         PlatformAnthropic,
				Type:             AccountTypeOAuth,
				RateLimitResetAt: tt.resetAt,
			}
			assertQuotaRecoveryState(t, NewQuotaRecoveryChecker(nil, reader).Check(context.Background(), account), tt.want)
		})
	}
}

func TestClaudeUsageResponse_OptionalWindowPresence(t *testing.T) {
	var absent ClaudeUsageResponse
	if err := json.Unmarshal([]byte(`{"five_hour": {}, "seven_day": {}}`), &absent); err != nil {
		t.Fatalf("unmarshal absent optional windows: %v", err)
	}
	if absent.SevenDaySonnet != nil || absent.SevenDayOverageIncluded != nil {
		t.Fatal("absent optional windows decoded as present")
	}

	var nullWindows ClaudeUsageResponse
	if err := json.Unmarshal([]byte(`{"seven_day_sonnet": null, "seven_day_overage_included": null}`), &nullWindows); err != nil {
		t.Fatalf("unmarshal null optional windows: %v", err)
	}
	if nullWindows.SevenDaySonnet != nil || nullWindows.SevenDayOverageIncluded != nil {
		t.Fatal("null optional windows decoded as present")
	}

	var empty ClaudeUsageResponse
	if err := json.Unmarshal([]byte(`{"seven_day_sonnet": {}, "seven_day_overage_included": {}}`), &empty); err != nil {
		t.Fatalf("unmarshal empty optional windows: %v", err)
	}
	if empty.SevenDaySonnet == nil || empty.SevenDayOverageIncluded == nil {
		t.Fatal("present empty optional windows decoded as absent")
	}
}

func TestQuotaRecoveryChecker_UnsupportedAccountsFailClosed(t *testing.T) {
	openAI := &quotaRecoveryOpenAIReaderStub{}
	anthropic := &quotaRecoveryAnthropicReaderStub{}
	checker := NewQuotaRecoveryChecker(openAI, anthropic)

	accounts := []*Account{
		nil,
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeSetupToken},
		{ID: 3, Platform: PlatformGemini, Type: AccountTypeOAuth},
		{ID: 0, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
	}
	for _, account := range accounts {
		assertQuotaRecoveryState(t, checker.Check(context.Background(), account), QuotaRecoveryUnknown)
	}
	if openAI.calls != 0 || anthropic.calls != 0 {
		t.Fatalf("unsupported accounts queried readers: OpenAI=%d Anthropic=%d", openAI.calls, anthropic.calls)
	}
}

func TestQuotaRecoveryChecker_MissingReaderAndCanceledContextFailClosed(t *testing.T) {
	account := &Account{ID: 61, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	assertQuotaRecoveryState(t, NewQuotaRecoveryChecker(nil, nil).Check(context.Background(), account), QuotaRecoveryUnknown)

	reader := &quotaRecoveryOpenAIReaderStub{usage: &OpenAIQuotaUsage{
		RateLimit: openAIRateLimitForRecovery(true, false, 1, nil),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assertQuotaRecoveryState(t, NewQuotaRecoveryChecker(reader, nil).Check(ctx, account), QuotaRecoveryUnknown)
	if reader.calls != 0 {
		t.Fatalf("canceled context made %d upstream calls", reader.calls)
	}
}

type quotaRecoveryClaudeUsageFetcherStub struct {
	response *ClaudeUsageResponse
	calls    int
	opts     *ClaudeUsageFetchOptions
}

func (s *quotaRecoveryClaudeUsageFetcherStub) FetchUsage(context.Context, string, string) (*ClaudeUsageResponse, error) {
	return nil, errors.New("unexpected legacy usage call")
}

func (s *quotaRecoveryClaudeUsageFetcherStub) FetchUsageWithOptions(_ context.Context, opts *ClaudeUsageFetchOptions) (*ClaudeUsageResponse, error) {
	s.calls++
	s.opts = opts
	return s.response, nil
}

func TestAccountUsageService_QueryAnthropicOAuthUsageUsesUsageFetcherOnly(t *testing.T) {
	response := anthropicUsageForRecovery(10, 20)
	fetcher := &quotaRecoveryClaudeUsageFetcherStub{response: response}
	service := &AccountUsageService{usageFetcher: fetcher}
	account := &Account{
		ID:       71,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "test-token",
		},
	}

	got, err := service.QueryAnthropicOAuthUsage(context.Background(), account)
	if err != nil {
		t.Fatalf("QueryAnthropicOAuthUsage() error = %v", err)
	}
	if got != response {
		t.Fatal("QueryAnthropicOAuthUsage() returned a different response")
	}
	if fetcher.calls != 1 || fetcher.opts == nil {
		t.Fatalf("FetchUsageWithOptions calls = %d, opts = %v", fetcher.calls, fetcher.opts)
	}
	if fetcher.opts.AccountID != account.ID || fetcher.opts.AccessToken != "test-token" {
		t.Fatalf("unexpected fetch options: %+v", fetcher.opts)
	}
}

func openAIRateLimitForRecovery(allowed, limitReached bool, primary float64, secondary *float64) *OpenAIRateLimit {
	limit := &OpenAIRateLimit{
		Allowed:       allowed,
		LimitReached:  limitReached,
		PrimaryWindow: openAIWindowForRecovery(primary, 18_000),
	}
	if secondary != nil {
		limit.SecondaryWindow = openAIWindowForRecovery(*secondary, 604_800)
	}
	return limit
}

func openAIWindowForRecovery(usedPercent float64, windowSeconds int64) *OpenAIRateLimitWindow {
	return &OpenAIRateLimitWindow{
		UsedPercent:        usedPercent,
		LimitWindowSeconds: windowSeconds,
		ResetAfterSeconds:  60,
		ResetAt:            quotaRecoveryTestFiveHourReset.Unix(),
	}
}

func quotaRecoveryFloat(value float64) *float64 {
	return &value
}

func quotaRecoveryTime(value time.Time) *time.Time {
	return &value
}

func anthropicUsageForRecovery(fiveHour, sevenDay float64) *ClaudeUsageResponse {
	usage := &ClaudeUsageResponse{}
	usage.FiveHour.Utilization = fiveHour
	usage.FiveHour.ResetsAt = quotaRecoveryTestFiveHourReset.Format(time.RFC3339)
	usage.SevenDay.Utilization = sevenDay
	usage.SevenDay.ResetsAt = quotaRecoveryTestSevenDayReset.Format(time.RFC3339)
	return usage
}

func assertQuotaRecoveryState(t *testing.T, decision QuotaRecoveryCheckResult, want QuotaRecoveryState) {
	t.Helper()
	if decision.Verdict != want {
		t.Fatalf("decision verdict = %q (%s), want %q", decision.Verdict, decision.Reason, want)
	}
	if decision.Authoritative != (want != QuotaRecoveryUnknown) {
		t.Fatalf("decision authoritative = %t for verdict %q", decision.Authoritative, decision.Verdict)
	}
	if decision.CheckedAt.IsZero() {
		t.Fatal("decision CheckedAt is zero")
	}
	if decision.Source == "" {
		t.Fatal("decision Source is empty")
	}
}
