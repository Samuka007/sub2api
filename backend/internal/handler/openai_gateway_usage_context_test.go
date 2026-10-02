package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSubmitUsageRecordTaskCopiesRequestContext(t *testing.T) {
	parent := context.WithValue(context.Background(), ctxkey.ClientRequestID, "client-request-123")
	parent = context.WithValue(parent, ctxkey.RequestID, "request-456")

	var gotClientRequestID string
	var gotRequestID string
	h := &GatewayHandler{}
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		gotClientRequestID, _ = ctx.Value(ctxkey.ClientRequestID).(string)
		gotRequestID, _ = ctx.Value(ctxkey.RequestID).(string)
	})

	require.Equal(t, "client-request-123", gotClientRequestID)
	require.Equal(t, "request-456", gotRequestID)
}

func TestOpenAISubmitUsageRecordTaskCopiesRequestContext(t *testing.T) {
	parent := context.WithValue(context.Background(), ctxkey.ClientRequestID, "openai-client-request-123")
	parent = context.WithValue(parent, ctxkey.RequestID, "openai-request-456")

	var gotClientRequestID string
	var gotRequestID string
	h := &OpenAIGatewayHandler{}
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		gotClientRequestID, _ = ctx.Value(ctxkey.ClientRequestID).(string)
		gotRequestID, _ = ctx.Value(ctxkey.RequestID).(string)
	})

	require.Equal(t, "openai-client-request-123", gotClientRequestID)
	require.Equal(t, "openai-request-456", gotRequestID)
}

type delayedUsageSchedulerCache struct {
	service.SchedulerCache
	accounts []*service.Account
}

func (c *delayedUsageSchedulerCache) GetSnapshot(context.Context, service.SchedulerBucket) ([]*service.Account, bool, error) {
	return c.accounts, true, nil
}

func (c *delayedUsageSchedulerCache) GetAccount(_ context.Context, accountID int64) (*service.Account, error) {
	for _, account := range c.accounts {
		if account != nil && account.ID == accountID {
			return account, nil
		}
	}
	return nil, nil
}

type delayedUsageGroupRepository struct {
	service.GroupRepository
	group *service.Group
}

func (r *delayedUsageGroupRepository) GetByID(context.Context, int64) (*service.Group, error) {
	return r.group, nil
}

func (r *delayedUsageGroupRepository) GetByIDLite(context.Context, int64) (*service.Group, error) {
	return r.group, nil
}

type delayedUsageConcurrencyCache struct {
	service.ConcurrencyCache
}

func (*delayedUsageConcurrencyCache) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	return true, nil
}

func (*delayedUsageConcurrencyCache) ReleaseAccountSlot(context.Context, int64, string) error {
	return nil
}

func (*delayedUsageConcurrencyCache) AcquireUserSlot(context.Context, int64, int, string) (bool, error) {
	return true, nil
}

func (*delayedUsageConcurrencyCache) ReleaseUserSlot(context.Context, int64, string) error {
	return nil
}

type delayedUsageHTTPUpstream struct{}

func (*delayedUsageHTTPUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"x-request-id": []string{"usage-context-request"},
		},
		Body: io.NopCloser(strings.NewReader(`{"id":"msg_usage_context","type":"message","role":"assistant","model":"claude-3-5-sonnet-latest","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}}`)),
	}, nil
}

func (u *delayedUsageHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

type delayedUsageLogRepository struct {
	service.UsageLogRepository
	created chan service.UsageLog
}

func (r *delayedUsageLogRepository) Create(_ context.Context, usageLog *service.UsageLog) (bool, error) {
	r.created <- *usageLog
	return true, nil
}

func TestGatewayMessagesUsageKeepsRequestedModelCapturedBeforeAsyncWorker(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		requestedModel    = "claude-3-5-sonnet-latest"
		firstPublicModel  = "public-first"
		secondPublicModel = "public-second"
	)
	groupID := int64(9200)
	group := &service.Group{
		ID:       groupID,
		Hydrated: true,
		Platform: service.PlatformAnthropic,
		Status:   service.StatusActive,
	}
	account := &service.Account{
		ID:          9201,
		Name:        "usage-context-account",
		Platform:    service.PlatformAnthropic,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "upstream-key",
			"base_url": "https://api.anthropic.com",
		},
		Extra: map[string]any{
			"anthropic_passthrough": true,
		},
		AccountGroups: []service.AccountGroup{{AccountID: 9201, GroupID: groupID}},
	}
	apiKey := &service.APIKey{
		ID:      9202,
		UserID:  9203,
		GroupID: &groupID,
		Group:   group,
		Status:  service.StatusActive,
		User: &service.User{
			ID:          9203,
			Concurrency: 10,
			Balance:     100,
		},
	}

	usageRepo := &delayedUsageLogRepository{created: make(chan service.UsageLog, 1)}
	schedulerCache := &delayedUsageSchedulerCache{accounts: []*service.Account{account}}
	groupRepo := &delayedUsageGroupRepository{group: group}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.MaxLineSize = 1 << 20
	schedulerSnapshot := service.NewSchedulerSnapshotService(schedulerCache, nil, nil, groupRepo, cfg)
	concurrencyService := service.NewConcurrencyService(&delayedUsageConcurrencyCache{})
	billingCacheService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCacheService.Stop)
	deferredService := service.NewDeferredService(nil, nil, time.Minute)
	gatewayService := service.NewGatewayService(
		nil,
		groupRepo,
		usageRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
		schedulerSnapshot,
		concurrencyService,
		service.NewBillingService(cfg, nil),
		&service.RateLimitService{},
		billingCacheService,
		nil,
		&delayedUsageHTTPUpstream{},
		deferredService,
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
		nil,
	)

	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
		WorkerCount:           1,
		QueueSize:             1,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "drop",
		OverflowSamplePercent: 0,
		AutoScaleEnabled:      false,
	})
	t.Cleanup(pool.Stop)
	workerStarted := make(chan struct{})
	releaseWorker := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseWorker) }) }
	t.Cleanup(release)
	pool.Submit(func(context.Context) {
		close(workerStarted)
		<-releaseWorker
	})
	select {
	case <-workerStarted:
	case <-time.After(time.Second):
		t.Fatal("usage worker blocker did not start")
	}

	h := &GatewayHandler{
		gatewayService:        gatewayService,
		billingCacheService:   billingCacheService,
		usageRecordWorkerPool: pool,
		concurrencyHelper:     NewConcurrencyHelper(concurrencyService, SSEPingFormatClaude, 0),
		maxAccountSwitches:    1,
		cfg:                   cfg,
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	requestContext := context.WithValue(context.Background(), ctxkey.Group, group)
	requestContext = service.WithCompositeRouteDecision(requestContext, service.CompositeRouteDecision{
		Matched:        true,
		PublicModel:    firstPublicModel,
		TargetPlatform: service.PlatformAnthropic,
		UpstreamModel:  requestedModel,
	})
	requestBody := []byte(`{"model":"claude-3-5-sonnet-latest","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(requestBody)).WithContext(requestContext)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})

	h.Messages(c)
	require.Equal(t, http.StatusOK, recorder.Code)

	mutatedContext := service.WithCompositeRouteDecision(c.Request.Context(), service.CompositeRouteDecision{
		Matched:        true,
		PublicModel:    secondPublicModel,
		TargetPlatform: service.PlatformAnthropic,
		UpstreamModel:  requestedModel,
	})
	c.Request = c.Request.WithContext(mutatedContext)
	release()

	var usageLog service.UsageLog
	select {
	case usageLog = <-usageRepo.created:
	case <-time.After(time.Second):
		t.Fatal("queued usage record did not execute")
	}
	require.Equal(t, firstPublicModel, usageLog.RequestedModel)
	require.NotNil(t, usageLog.ModelMappingChain)
	require.Equal(t, firstPublicModel+"→"+requestedModel, *usageLog.ModelMappingChain)
}
