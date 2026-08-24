//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func newGatewayRecordUsageServiceForTest(usageRepo UsageLogRepository, userRepo UserRepository, subRepo UserSubscriptionRepository) *GatewayService {
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1.1
	return NewGatewayService(
		nil,
		nil,
		usageRepo,
		nil,
		userRepo,
		subRepo,
		nil,
		nil,
		cfg,
		nil,
		nil,
		NewBillingService(cfg, nil),
		nil,
		&BillingCacheService{},
		nil,
		nil,
		&DeferredService{},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil, // userPlatformQuotaRepo
	)
}

func newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo UsageLogRepository, billingRepo UsageBillingRepository, userRepo UserRepository, subRepo UserSubscriptionRepository) *GatewayService {
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)
	svc.usageBillingRepo = billingRepo
	return svc
}

type openAIRecordUsageBestEffortLogRepoStub struct {
	UsageLogRepository

	bestEffortErr   error
	createErr       error
	bestEffortCalls int
	createCalls     int
	lastLog         *UsageLog
	lastCtxErr      error
}

func (s *openAIRecordUsageBestEffortLogRepoStub) CreateBestEffort(ctx context.Context, log *UsageLog) error {
	s.bestEffortCalls++
	s.lastLog = log
	s.lastCtxErr = ctx.Err()
	return s.bestEffortErr
}

func (s *openAIRecordUsageBestEffortLogRepoStub) Create(ctx context.Context, log *UsageLog) (bool, error) {
	s.createCalls++
	s.lastLog = log
	s.lastCtxErr = ctx.Err()
	return false, s.createErr
}

func TestGatewayServiceRecordUsage_BillingUsesDetachedContext(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: false, err: context.DeadlineExceeded}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	quotaSvc := &openAIRecordUsageAPIKeyQuotaStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	reqCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := svc.RecordUsage(reqCtx, &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "gateway_detached_ctx",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey: &APIKey{
			ID:    501,
			Quota: 100,
		},
		User:          &User{ID: 601},
		Account:       &Account{ID: 701},
		APIKeyService: quotaSvc,
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 1, userRepo.deductCalls)
	require.NoError(t, userRepo.lastCtxErr)
	require.Equal(t, 1, quotaSvc.quotaCalls)
	require.NoError(t, quotaSvc.lastQuotaCtxErr)
}

func TestGatewayServiceRecordUsage_BillingFingerprintIncludesRequestPayloadHash(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	payloadHash := HashUsageRequestPayload([]byte(`{"messages":[{"role":"user","content":"hello"}]}`))
	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "gateway_payload_hash",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:             &APIKey{ID: 501, Quota: 100},
		User:               &User{ID: 601},
		Account:            &Account{ID: 701},
		RequestPayloadHash: payloadHash,
	})
	require.NoError(t, err)
	require.NotNil(t, billingRepo.lastCmd)
	require.Equal(t, payloadHash, billingRepo.lastCmd.RequestPayloadHash)
}

func TestGatewayServiceRecordUsage_BillingFingerprintFallsBackToContextRequestID(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "req-local-123")
	err := svc.RecordUsage(ctx, &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "gateway_payload_fallback",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 501, Quota: 100},
		User:    &User{ID: 601},
		Account: &Account{ID: 701},
	})
	require.NoError(t, err)
	require.NotNil(t, billingRepo.lastCmd)
	require.Equal(t, "local:req-local-123", billingRepo.lastCmd.RequestPayloadHash)
}

func TestGatewayServiceRecordUsage_BillsWhenReportedUsageExceedsModelLimit(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	svc.cfg.Default.RateMultiplier = 1

	pricingData, err := (&PricingService{}).parsePricingData([]byte(`{
		"claude-fable-5": {
			"input_cost_per_token": 0.00001,
			"output_cost_per_token": 0.00005,
			"max_input_tokens": 1000000,
			"max_output_tokens": 128000,
			"litellm_provider": "anthropic",
			"mode": "chat"
		}
	}`))
	require.NoError(t, err)
	svc.billingService.pricingService = &PricingService{pricingData: pricingData}

	err = svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "issue-33-anomalous-usage",
			Usage: ClaudeUsage{
				InputTokens:  7_489_444,
				OutputTokens: 41_378,
			},
			Model:    "claude-fable-5",
			Duration: 165 * time.Second,
		},
		APIKey: &APIKey{ID: 501, Quota: 100},
		User:   &User{ID: 601},
		Account: &Account{
			ID:       701,
			Platform: PlatformAnthropic,
			Type:     AccountTypeAPIKey,
			Credentials: map[string]any{
				"base_url": "https://custom-anthropic.example.com",
			},
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, billingRepo.calls, "usage exceeding provider model limits is still billed")
	require.NotNil(t, usageRepo.lastLog, "usage log must remain persisted for review")
	require.Equal(t, 7_489_444, usageRepo.lastLog.InputTokens)
	require.Equal(t, 41_378, usageRepo.lastLog.OutputTokens)
	require.InDelta(t, 76.96334, usageRepo.lastLog.TotalCost, 1e-9)
	require.Positive(t, usageRepo.lastLog.ActualCost, "usage exceeding provider model limits is still billed")
}

func TestGatewayServiceRecordUsage_RecordsOnlyAvailableBalanceAsActualCost(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	newBalance := 0.0
	charged := 0.001
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{
		Applied:            true,
		NewBalance:         &newBalance,
		BalanceCharged:     &charged,
		BalanceOverdrafted: true,
	}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "balance-capped-at-zero",
			Usage: ClaudeUsage{
				InputTokens:  1_000,
				OutputTokens: 100,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 501, Quota: 100},
		User:    &User{ID: 601, Balance: charged},
		Account: &Account{ID: 701, Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
	})

	require.NoError(t, err)
	require.Equal(t, 1, billingRepo.calls)
	require.NotNil(t, usageRepo.lastLog)
	require.Greater(t, usageRepo.lastLog.TotalCost, charged)
	require.InDelta(t, charged, usageRepo.lastLog.ActualCost, 1e-12)
}

func TestGatewayServiceRecordUsage_PreservesRequestedAndUpstreamModels(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	mappedModel := "claude-sonnet-4-20250514"

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID:     "gateway_models_split",
			Usage:         ClaudeUsage{InputTokens: 10, OutputTokens: 6},
			Model:         "claude-sonnet-4",
			UpstreamModel: mappedModel,
			Duration:      time.Second,
		},
		APIKey:  &APIKey{ID: 501, Quota: 100},
		User:    &User{ID: 601},
		Account: &Account{ID: 701},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, "claude-sonnet-4", usageRepo.lastLog.Model)
	require.Equal(t, "claude-sonnet-4", usageRepo.lastLog.RequestedModel)
	require.NotNil(t, usageRepo.lastLog.UpstreamModel)
	require.Equal(t, mappedModel, *usageRepo.lastLog.UpstreamModel)
}

func TestGatewayServiceRecordUsage_PreservesChannelMappedUpstreamModel(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID:     "gateway_channel_mapping_models",
			Usage:         ClaudeUsage{InputTokens: 10, OutputTokens: 6},
			Model:         "gpt-5.6-terra",
			UpstreamModel: "gpt-5.6-terra",
			Duration:      time.Second,
		},
		APIKey:  &APIKey{ID: 501, Quota: 100},
		User:    &User{ID: 601},
		Account: &Account{ID: 701},
		ChannelUsageFields: ChannelUsageFields{
			OriginalModel:      "gpt-5.6-sol",
			ChannelMappedModel: "gpt-5.6-terra",
		},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, "gpt-5.6-sol", usageRepo.lastLog.RequestedModel)
	require.Equal(t, "gpt-5.6-terra", usageRepo.lastLog.Model)
	require.NotNil(t, usageRepo.lastLog.UpstreamModel)
	require.Equal(t, "gpt-5.6-terra", *usageRepo.lastLog.UpstreamModel)
}

func TestGatewayServiceRecordUsage_PreservesLoopedChannelAndAccountUpstreamModel(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID:     "gateway_looped_mapping_models",
			Usage:         ClaudeUsage{InputTokens: 10, OutputTokens: 6},
			Model:         "gpt-5.6-terra",
			UpstreamModel: "gpt-5.6-sol",
			Duration:      time.Second,
		},
		APIKey:  &APIKey{ID: 501, Quota: 100},
		User:    &User{ID: 601},
		Account: &Account{ID: 701},
		ChannelUsageFields: ChannelUsageFields{
			OriginalModel:      "gpt-5.6-sol",
			ChannelMappedModel: "gpt-5.6-terra",
		},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, "gpt-5.6-sol", usageRepo.lastLog.RequestedModel)
	require.Equal(t, "gpt-5.6-terra", usageRepo.lastLog.Model)
	require.NotNil(t, usageRepo.lastLog.UpstreamModel)
	require.Equal(t, "gpt-5.6-sol", *usageRepo.lastLog.UpstreamModel)
}

func TestGatewayServiceRecordUsage_EmptyImageSizeDefaultsBeforeBillingAndPersistence(t *testing.T) {
	imagePrice2K := 0.19
	groupID := int64(901)
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID:      "gateway_image_default_size",
			Model:          "gemini-image",
			ImageCount:     1,
			ImageInputSize: "auto",
			Duration:       time.Second,
		},
		APIKey: &APIKey{
			ID:      801,
			GroupID: i64p(groupID),
			Group: &Group{
				ID:             groupID,
				RateMultiplier: 1.0,
				ImagePrice2K:   &imagePrice2K,
			},
		},
		User:    &User{ID: 601},
		Account: &Account{ID: 701},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, 1, usageRepo.lastLog.ImageCount)
	require.NotNil(t, usageRepo.lastLog.ImageSize)
	require.Equal(t, ImageBillingSize2K, *usageRepo.lastLog.ImageSize)
	require.NotNil(t, usageRepo.lastLog.ImageInputSize)
	require.Equal(t, "auto", *usageRepo.lastLog.ImageInputSize)
	require.NotNil(t, usageRepo.lastLog.ImageSizeSource)
	require.Equal(t, ImageSizeSourceDefault, *usageRepo.lastLog.ImageSizeSource)
	require.InDelta(t, 0.19, usageRepo.lastLog.TotalCost, 1e-12)
	require.InDelta(t, 0.19, usageRepo.lastLog.ActualCost, 1e-12)
}

func TestGatewayServiceRecordUsage_PeakRateAffectsTokenModeImageOutputTokens(t *testing.T) {
	groupID := int64(902)
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{})
	svc.resolver = newOpenAITokenImageChannelPricingResolverForTest(t, groupID, "gemini-image")

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID:  "gateway_peak_image_tokens",
			Model:      "gemini-image",
			ImageCount: 1,
			Usage: ClaudeUsage{
				InputTokens:       1000,
				OutputTokens:      600,
				ImageOutputTokens: 100,
			},
			Duration: time.Second,
		},
		APIKey: &APIKey{
			ID:      802,
			GroupID: i64p(groupID),
			Group: &Group{
				ID:                 groupID,
				RateMultiplier:     1.0,
				SubscriptionType:   SubscriptionTypeSubscription,
				PeakRateEnabled:    true,
				PeakStart:          "00:00",
				PeakEnd:            "23:59",
				PeakRateMultiplier: 3.0,
			},
		},
		User:    &User{ID: 602},
		Account: &Account{ID: 702},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.NotNil(t, usageRepo.lastLog.BillingMode)
	require.Equal(t, string(BillingModeToken), *usageRepo.lastLog.BillingMode)
	require.Equal(t, 3.0, usageRepo.lastLog.RateMultiplier)

	textInput := 1000 * 3e-6
	textOutput := 500 * 15e-6
	imageOutput := 100 * 15e-6
	expectedActual := (textInput + textOutput + imageOutput) * 3.0

	require.InDelta(t, textInput+textOutput+imageOutput, usageRepo.lastLog.TotalCost, 1e-12)
	require.InDelta(t, imageOutput, usageRepo.lastLog.ImageOutputCost, 1e-12)
	require.InDelta(t, expectedActual, usageRepo.lastLog.ActualCost, 1e-12)
	require.InDelta(t, expectedActual, userRepo.lastAmount, 1e-12)
}

func TestGatewayServiceRecordUsage_TimePricingUsesPricingAt(t *testing.T) {
	groupID := int64(904)
	requestStart := time.Date(2024, time.January, 2, 2, 0, 0, 0, time.UTC) // 上海 10:00
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{})
	svc.resolver = newOpenAITokenImageChannelPricingResolverWithTimeForTest(t, groupID, "gpt-5.1", &ChannelTimePricing{
		Timezone: "Asia/Shanghai",
		Periods:  []ChannelTimePricingPeriod{{StartTime: "09:00", EndTime: "12:00", Multiplier: 2}},
	})

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "gateway_time_pricing_request_start",
			Model:     "gpt-5.1",
			Usage:     ClaudeUsage{InputTokens: 1000, OutputTokens: 500},
		},
		APIKey: &APIKey{ID: 804, GroupID: i64p(groupID), Group: &Group{
			ID: groupID, RateMultiplier: 0.8, SubscriptionType: SubscriptionTypeSubscription,
		}},
		User:      &User{ID: 604},
		Account:   &Account{ID: 704},
		PricingAt: requestStart,
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	baseCost := 1000*3e-6 + 500*15e-6
	require.InDelta(t, baseCost*2, usageRepo.lastLog.TotalCost, 1e-12)
	require.InDelta(t, baseCost*2*0.8, usageRepo.lastLog.ActualCost, 1e-12)
	require.InDelta(t, 0.8, usageRepo.lastLog.RateMultiplier, 1e-12)
}
func TestGatewayServiceRecordUsage_UsesExplicitPricingAtForPeakRate(t *testing.T) {
	for _, platform := range []string{PlatformAnthropic, PlatformGemini, PlatformGrok, PlatformAntigravity} {
		t.Run(platform, func(t *testing.T) {
			groupID := int64(903)
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			userRepo := &openAIRecordUsageUserRepoStub{}
			svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{})
			svc.resolver = newOpenAITokenImageChannelPricingResolverForTest(t, groupID, "gemini-image")

			pricingAt := time.Date(2026, time.January, 1, 0, 30, 0, 0, time.UTC)
			err := svc.RecordUsage(context.Background(), &RecordUsageInput{
				Result: &ForwardResult{
					RequestID:  "gateway_explicit_pricing_at_" + platform,
					Model:      "gemini-image",
					ImageCount: 1,
					Usage: ClaudeUsage{
						InputTokens:       1000,
						OutputTokens:      600,
						ImageOutputTokens: 100,
					},
				},
				APIKey: &APIKey{
					ID:      803,
					GroupID: i64p(groupID),
					Group: &Group{
						ID:                 groupID,
						Platform:           platform,
						RateMultiplier:     1.0,
						SubscriptionType:   SubscriptionTypeSubscription,
						PeakRateEnabled:    true,
						PeakStart:          "00:00",
						PeakEnd:            "01:00",
						PeakRateMultiplier: 3.0,
					},
				},
				User:      &User{ID: 603},
				Account:   &Account{ID: 703, Platform: platform},
				PricingAt: pricingAt,
			})

			require.NoError(t, err)
			require.NotNil(t, usageRepo.lastLog)
			require.Equal(t, 3.0, usageRepo.lastLog.RateMultiplier)
		})
	}
}

func TestGatewayServiceRecordUsage_UsageLogWriteErrorDoesNotSkipBilling(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: false, err: MarkUsageLogCreateNotPersisted(context.Canceled)}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	quotaSvc := &openAIRecordUsageAPIKeyQuotaStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "gateway_not_persisted",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey: &APIKey{
			ID:    503,
			Quota: 100,
		},
		User:          &User{ID: 603},
		Account:       &Account{ID: 703},
		APIKeyService: quotaSvc,
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 1, userRepo.deductCalls)
	require.Equal(t, 1, quotaSvc.quotaCalls)
}

func TestGatewayServiceRecordUsageWithLongContext_BillingUsesDetachedContext(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: false, err: context.DeadlineExceeded}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	quotaSvc := &openAIRecordUsageAPIKeyQuotaStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	reqCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := svc.RecordUsageWithLongContext(reqCtx, &RecordUsageLongContextInput{
		Result: &ForwardResult{
			RequestID: "gateway_long_context_detached_ctx",
			Usage: ClaudeUsage{
				InputTokens:  12,
				OutputTokens: 8,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey: &APIKey{
			ID:    502,
			Quota: 100,
		},
		User:                  &User{ID: 602},
		Account:               &Account{ID: 702},
		LongContextThreshold:  200000,
		LongContextMultiplier: 2,
		APIKeyService:         quotaSvc,
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 1, userRepo.deductCalls)
	require.NoError(t, userRepo.lastCtxErr)
	require.Equal(t, 1, quotaSvc.quotaCalls)
	require.NoError(t, quotaSvc.lastQuotaCtxErr)
}

func TestGatewayServiceRecordUsage_UsesFallbackRequestIDForUsageLog(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "gateway-local-fallback")
	err := svc.RecordUsage(ctx, &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 504},
		User:    &User{ID: 604},
		Account: &Account{ID: 704},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, "local:gateway-local-fallback", usageRepo.lastLog.RequestID)
}

func TestGatewayServiceRecordUsage_PrefersClientRequestIDOverUpstreamRequestID(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "client-stable-123")
	ctx = context.WithValue(ctx, ctxkey.RequestID, "req-local-ignored")
	err := svc.RecordUsage(ctx, &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "upstream-volatile-456",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 506},
		User:    &User{ID: 606},
		Account: &Account{ID: 706},
	})

	require.NoError(t, err)
	require.NotNil(t, billingRepo.lastCmd)
	require.Equal(t, "client:client-stable-123", billingRepo.lastCmd.RequestID)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, "client:client-stable-123", usageRepo.lastLog.RequestID)
}

func TestGatewayServiceRecordUsage_HTTPResponsesUsesDistinctBillingIDsForTurnsInSameThread(t *testing.T) {
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(&openAIRecordUsageLogRepoStub{}, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "codex-thread-123")

	responseIDs := []string{"msg_gateway_turn_1", "msg_gateway_turn_2"}
	resolved := make([]string, 0, len(responseIDs))
	for i, responseID := range responseIDs {
		err := svc.RecordUsage(ctx, &RecordUsageInput{
			Result: &ForwardResult{
				RequestID:  "anthropic-transport-" + responseID,
				ResponseID: responseID,
				Usage:      ClaudeUsage{InputTokens: 10 + i, OutputTokens: 6},
				Model:      "claude-sonnet-4",
				Duration:   time.Second,
			},
			APIKey:          &APIKey{ID: 506},
			User:            &User{ID: 606},
			Account:         &Account{ID: 706},
			InboundEndpoint: openAIResponsesEndpoint,
			RequestPayloadHash: HashUsageRequestPayload(
				[]byte("turn-" + responseID),
			),
		})
		require.NoError(t, err)
		require.NotNil(t, billingRepo.lastCmd)
		resolved = append(resolved, billingRepo.lastCmd.RequestID)
	}

	require.Equal(t, responseIDs, resolved)
	require.NotEqual(t, resolved[0], resolved[1])
}

func TestGatewayServiceRecordUsage_HTTPResponsesFallsBackToUpstreamRequestID(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "codex-thread-123")
	ctx = context.WithValue(ctx, ctxkey.RequestID, "gateway-turn-456")
	err := svc.RecordUsage(ctx, &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "anthropic-request-789",
			Usage:     ClaudeUsage{InputTokens: 10, OutputTokens: 6},
			Model:     "claude-sonnet-4",
			Duration:  time.Second,
		},
		APIKey:          &APIKey{ID: 507},
		User:            &User{ID: 607},
		Account:         &Account{ID: 707},
		InboundEndpoint: openAIResponsesCompactEndpoint,
	})

	require.NoError(t, err)
	require.NotNil(t, billingRepo.lastCmd)
	require.Equal(t, "anthropic-request-789", billingRepo.lastCmd.RequestID)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, "anthropic-request-789", usageRepo.lastLog.RequestID)
}

func TestResolveUsageBillingRequestIDForEndpoint_HTTPResponsesIgnoresClientCorrelationIDs(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "codex-thread-123")
	ctx = context.WithValue(ctx, ctxkey.RequestID, "caller-request-456")

	got := resolveUsageBillingRequestIDForEndpoint(ctx, openAIResponsesEndpoint, &ForwardResult{})

	require.True(t, strings.HasPrefix(got, "generated:"), got)
	require.NotEqual(t, "client:codex-thread-123", got)
	require.NotEqual(t, "local:caller-request-456", got)
}

func TestResolveUsageBillingRequestIDForEndpoint_HTTPResponsesRetryKeepsExecutionID(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "codex-thread-123")

	first := resolveUsageBillingRequestIDForEndpoint(ctx, openAIResponsesEndpoint, &ForwardResult{
		ResponseID: "msg_same_gateway_turn_456",
		RequestID:  "transport-attempt-1",
	})
	second := resolveUsageBillingRequestIDForEndpoint(ctx, openAIResponsesEndpoint, &ForwardResult{
		ResponseID: "msg_same_gateway_turn_456",
		RequestID:  "transport-attempt-2",
	})

	require.Equal(t, "msg_same_gateway_turn_456", first)
	require.Equal(t, first, second)
}

func TestResolveUsageBillingRequestIDForEndpoint_NonResponsesRetainsContextFirstBehavior(t *testing.T) {
	upstreamRequestID := strings.Repeat("upstream-", 10)
	result := &ForwardResult{RequestID: upstreamRequestID}
	clientCtx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "client-first")

	first := resolveUsageBillingRequestIDForEndpoint(clientCtx, "/v1/messages", result)
	second := resolveUsageBillingRequestIDForEndpoint(context.Background(), "/v1/messages", result)

	require.Equal(t, "client:client-first", first)
	require.Equal(t, normalizeUsageBillingRequestID(upstreamRequestID), second)
	require.NotEqual(t, first, second)
}

func TestNormalizeUsageBillingRequestID_BoundsAndCollisionResistance(t *testing.T) {
	short := strings.Repeat("s", usageBillingRequestIDMaxLength)
	require.Equal(t, short, normalizeUsageBillingRequestID(" "+short+" "))

	sharedPrefix := strings.Repeat("x", usageBillingRequestIDMaxLength)
	first := normalizeUsageBillingRequestID(sharedPrefix + "a")
	second := normalizeUsageBillingRequestID(sharedPrefix + "b")

	require.Equal(t, first, normalizeUsageBillingRequestID(sharedPrefix+"a"))
	require.NotEqual(t, first, second)
	require.True(t, strings.HasPrefix(first, "sha256:"), first)
	require.LessOrEqual(t, len(first), usageBillingRequestIDMaxLength)

	cmd := &UsageBillingCommand{RequestID: " " + sharedPrefix + "a "}
	cmd.Normalize()
	require.Equal(t, first, cmd.RequestID)
}

func TestResolveUsageBillingRequestIDForEndpoint_HTTPResponsesConcurrentRetryKeepsGeneratedExecutionID(t *testing.T) {
	result := &ForwardResult{}
	const retries = 32
	resolved := make(chan string, retries)

	var wg sync.WaitGroup
	for range retries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resolved <- resolveUsageBillingRequestIDForEndpoint(context.Background(), openAIResponsesEndpoint, result)
		}()
	}
	wg.Wait()
	close(resolved)

	var first string
	for requestID := range resolved {
		if first == "" {
			first = requestID
		}
		require.Equal(t, first, requestID)
	}
	require.True(t, strings.HasPrefix(first, "generated:"), first)
}

func TestResolveUsageBillingRequestIDForEndpoint_HTTPResponsesGeneratesDistinctIDsPerExecution(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "codex-thread-123")

	first := resolveUsageBillingRequestIDForEndpoint(ctx, openAIResponsesEndpoint, &ForwardResult{})
	second := resolveUsageBillingRequestIDForEndpoint(ctx, openAIResponsesEndpoint, &ForwardResult{})

	require.NotEqual(t, first, second)
	require.NotEqual(t, "client:codex-thread-123", first)
	require.NotEqual(t, "client:codex-thread-123", second)
}

func TestGatewayServiceRecordUsage_HTTPResponsesNormalizesOversizedExecutionIDs(t *testing.T) {
	oversizedID := strings.Repeat("execution-", 10)
	tests := []struct {
		name     string
		endpoint string
		result   *ForwardResult
	}{
		{
			name:     "response ID",
			endpoint: openAIResponsesEndpoint,
			result:   &ForwardResult{ResponseID: oversizedID, RequestID: "transport-fallback"},
		},
		{
			name:     "upstream request ID",
			endpoint: openAIResponsesCompactEndpoint,
			result:   &ForwardResult{RequestID: oversizedID},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{}
			billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
			svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
			tt.result.Usage = ClaudeUsage{InputTokens: 10, OutputTokens: 6}
			tt.result.Model = "claude-sonnet-4"
			tt.result.Duration = time.Second

			err := svc.RecordUsage(context.Background(), &RecordUsageInput{
				Result:          tt.result,
				APIKey:          &APIKey{ID: 507},
				User:            &User{ID: 607},
				Account:         &Account{ID: 707},
				InboundEndpoint: tt.endpoint,
			})

			require.NoError(t, err)
			require.NotNil(t, billingRepo.lastCmd)
			require.NotNil(t, usageRepo.lastLog)
			require.Equal(t, normalizeUsageBillingRequestID(oversizedID), billingRepo.lastCmd.RequestID)
			require.Equal(t, billingRepo.lastCmd.RequestID, usageRepo.lastLog.RequestID)
			require.LessOrEqual(t, len(usageRepo.lastLog.RequestID), usageBillingRequestIDMaxLength)
		})
	}
}

func TestGatewayServiceRecordUsage_HTTPResponsesBillingRetryKeepsGeneratedExecutionID(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{}
	billingRepo := &openAIRecordUsageBillingRepoStub{err: errors.New("temporary billing failure")}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	input := &RecordUsageInput{
		Result: &ForwardResult{
			Usage:    ClaudeUsage{InputTokens: 10, OutputTokens: 6},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:          &APIKey{ID: 507},
		User:            &User{ID: 607},
		Account:         &Account{ID: 707},
		InboundEndpoint: openAIResponsesEndpoint,
	}

	require.Error(t, svc.RecordUsage(context.Background(), input))
	require.NotNil(t, billingRepo.lastCmd)
	first := billingRepo.lastCmd.RequestID
	require.True(t, strings.HasPrefix(first, "generated:"), first)

	billingRepo.err = nil
	billingRepo.result = &UsageBillingApplyResult{Applied: true}
	require.NoError(t, svc.RecordUsage(context.Background(), input))
	require.Equal(t, first, billingRepo.lastCmd.RequestID)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, first, usageRepo.lastLog.RequestID)
}

func TestGatewayServiceRecordUsage_GeneratesRequestIDWhenAllSourcesMissing(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 507},
		User:    &User{ID: 607},
		Account: &Account{ID: 707},
	})

	require.NoError(t, err)
	require.NotNil(t, billingRepo.lastCmd)
	require.True(t, strings.HasPrefix(billingRepo.lastCmd.RequestID, "generated:"))
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, billingRepo.lastCmd.RequestID, usageRepo.lastLog.RequestID)
}

func TestGatewayServiceRecordUsage_DroppedUsageLogFallsBackToSyncCreate(t *testing.T) {
	// 计费成功后 best-effort 写入被丢弃（队列超时）时必须同步兜底，
	// 否则出现“已扣费但无 usage_log”的对账缺口（issue #3656）。
	usageRepo := &openAIRecordUsageBestEffortLogRepoStub{
		bestEffortErr: MarkUsageLogCreateDropped(errors.New("usage log best-effort queue full")),
	}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "gateway_drop_usage_log",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 508},
		User:    &User{ID: 608},
		Account: &Account{ID: 708},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.bestEffortCalls)
	require.Equal(t, 1, usageRepo.createCalls)
	// 兜底调用使用的 ctx 必须仍然存活，不能带着已死的 ctx 走过场。
	require.NoError(t, usageRepo.lastCtxErr)
}

func TestGatewayServiceRecordUsage_BillingErrorWritesUnsettledUsageLog(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{}
	billingErr := errors.New("billing tx failed")
	billingRepo := &openAIRecordUsageBillingRepoStub{err: billingErr}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo)

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "gateway_billing_fail",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 505},
		User:    &User{ID: 605},
		Account: &Account{ID: 705},
	})

	require.ErrorIs(t, err, billingErr)
	require.Equal(t, 1, billingRepo.calls)
	require.Equal(t, 1, usageRepo.calls)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, 10, usageRepo.lastLog.InputTokens)
	require.Equal(t, 6, usageRepo.lastLog.OutputTokens)
	require.Greater(t, usageRepo.lastLog.InputCost, 0.0)
	require.Greater(t, usageRepo.lastLog.OutputCost, 0.0)
	require.Greater(t, usageRepo.lastLog.TotalCost, 0.0)
	require.Zero(t, usageRepo.lastLog.ActualCost)
}

func TestGatewayServiceRecordUsage_ReasoningEffortPersisted(t *testing.T) {
	usageRepo := &openAIRecordUsageBestEffortLogRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	effort := "max"
	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "effort_test",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 5,
			},
			Model:           "claude-opus-4-6",
			Duration:        time.Second,
			ReasoningEffort: &effort,
		},
		APIKey:  &APIKey{ID: 1},
		User:    &User{ID: 1},
		Account: &Account{ID: 1},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.NotNil(t, usageRepo.lastLog.ReasoningEffort)
	require.Equal(t, "max", *usageRepo.lastLog.ReasoningEffort)
}

func TestGatewayServiceRecordUsage_ReasoningEffortNil(t *testing.T) {
	usageRepo := &openAIRecordUsageBestEffortLogRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "no_effort_test",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 5,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 1},
		User:    &User{ID: 1},
		Account: &Account{ID: 1},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Nil(t, usageRepo.lastLog.ReasoningEffort)
}

// newGatewayRecordUsageServiceWithResolverForTest mirrors production wiring for
// token billing: a pricing resolver plus a grouped API key select the unified
// billing path, which is the only one that honours the service tier.
func newGatewayRecordUsageServiceWithResolverForTest(usageRepo UsageLogRepository) (*GatewayService, *APIKey) {
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	svc.resolver = NewModelPricingResolver(nil, svc.billingService)
	groupID := int64(7)
	return svc, &APIKey{ID: 1, GroupID: &groupID, Group: &Group{ID: groupID, RateMultiplier: 1.0}}
}

func TestGatewayServiceRecordUsage_FastSpeedDowngradedByUpstreamResponse(t *testing.T) {
	usageRepo := &openAIRecordUsageBestEffortLogRepoStub{}
	svc, apiKey := newGatewayRecordUsageServiceWithResolverForTest(usageRepo)

	tier := "fast"
	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID:                   "fast_downgraded_test",
			Usage:                       ClaudeUsage{InputTokens: 100, OutputTokens: 50},
			Model:                       "claude-opus-5",
			Duration:                    time.Second,
			ServiceTier:                 &tier,
			UpstreamResponseServiceTier: "standard",
		},
		APIKey:  apiKey,
		User:    &User{ID: 1},
		Account: &Account{ID: 1, Platform: PlatformAnthropic},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.NotNil(t, usageRepo.lastLog.ServiceTier)
	require.Equal(t, "standard", *usageRepo.lastLog.ServiceTier)

	tokens := UsageTokens{InputTokens: 100, OutputTokens: 50}
	standardCost, err := svc.billingService.CalculateCost("claude-opus-5", tokens, 1.0)
	require.NoError(t, err)
	fastCost, err := svc.billingService.CalculateCostWithServiceTier("claude-opus-5", tokens, 1.0, "fast")
	require.NoError(t, err)
	require.Greater(t, fastCost.TotalCost, standardCost.TotalCost, "fast mode must carry a premium for the test to be meaningful")
	require.InDelta(t, standardCost.TotalCost, usageRepo.lastLog.TotalCost, 1e-10)
}

func TestGatewayServiceRecordUsage_FastSpeedHonouredKeepsPremium(t *testing.T) {
	usageRepo := &openAIRecordUsageBestEffortLogRepoStub{}
	svc, apiKey := newGatewayRecordUsageServiceWithResolverForTest(usageRepo)

	tier := "fast"
	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID:                   "fast_honoured_test",
			Usage:                       ClaudeUsage{InputTokens: 100, OutputTokens: 50},
			Model:                       "claude-opus-5",
			Duration:                    time.Second,
			ServiceTier:                 &tier,
			UpstreamResponseServiceTier: "fast",
		},
		APIKey:  apiKey,
		User:    &User{ID: 1},
		Account: &Account{ID: 1, Platform: PlatformAnthropic},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, "fast", *usageRepo.lastLog.ServiceTier)

	fastCost, err := svc.billingService.CalculateCostWithServiceTier("claude-opus-5", UsageTokens{InputTokens: 100, OutputTokens: 50}, 1.0, "fast")
	require.NoError(t, err)
	require.InDelta(t, fastCost.TotalCost, usageRepo.lastLog.TotalCost, 1e-10)
}
