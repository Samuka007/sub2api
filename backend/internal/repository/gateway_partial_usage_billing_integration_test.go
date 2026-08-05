//go:build integration

package repository

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/server/routes"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const partialUsageMessagesBody = `{"model":"claude-sonnet-4","max_tokens":64,"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`

type partialUsageUpstream struct {
	mu      sync.Mutex
	request int
}

type cancelingResponseRecorder struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
	once   sync.Once
}

func (r *cancelingResponseRecorder) Write(data []byte) (int, error) {
	n, err := r.ResponseRecorder.Write(data)
	if bytes.Contains(r.Body.Bytes(), []byte(`"type":"message_delta"`)) {
		r.once.Do(r.cancel)
	}
	return n, err
}

func (u *partialUsageUpstream) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	u.mu.Lock()
	u.request++
	request := u.request
	u.mu.Unlock()

	payload := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_partial","type":"message","role":"assistant","model":"claude-sonnet-4","content":[],"usage":{"input_tokens":11,"cache_read_input_tokens":7}}}`,
		"",
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial hello"}}`,
		"",
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":null},"usage":{"output_tokens":5}}`,
		"",
		"",
	}, "\n")

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	// Announce a longer fixed body, write the observed usage and partial text, then
	// close the socket without a terminal SSE event. net/http consequently exposes
	// io.ErrUnexpectedEOF to the real HTTPUpstream response body.
	_, _ = fmt.Fprintf(rw, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nX-Request-Id: upstream-partial-%d\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", request, len(payload)+64, payload)
	_ = rw.Flush()
}

func TestGatewayMessagesPartialStreamBillsOnceAfterCancellationAndDuplicateSubmission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	client := testEntClient(t)

	upstream := &partialUsageUpstream{}
	upstreamServer := httptest.NewServer(upstream)
	t.Cleanup(upstreamServer.Close)

	group := mustCreateGroup(t, client, &service.Group{
		Name:           "partial-usage-group-" + uuid.NewString(),
		Platform:       service.PlatformAnthropic,
		RateMultiplier: 1,
	})
	group.Hydrated = true
	user := mustCreateUser(t, client, &service.User{
		Email:   "partial-usage-" + uuid.NewString() + "@example.com",
		Balance: 100,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &group.ID,
		Key:     "sk-partial-usage-" + uuid.NewString(),
	})
	apiKey.User = user
	apiKey.Group = group
	account := mustCreateAccount(t, client, &service.Account{
		Name:        "partial-usage-upstream-" + uuid.NewString(),
		Platform:    service.PlatformAnthropic,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "upstream-test-key",
			"base_url": upstreamServer.URL,
		},
		Extra: map[string]any{"anthropic_passthrough": true},
	})
	mustBindAccountToGroup(t, client, account.ID, group.ID, 1)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = integrationDB.ExecContext(cleanupCtx, `DELETE FROM usage_logs WHERE api_key_id = $1`, apiKey.ID)
		_, _ = integrationDB.ExecContext(cleanupCtx, `DELETE FROM usage_billing_dedup WHERE api_key_id = $1`, apiKey.ID)
		_, _ = integrationDB.ExecContext(cleanupCtx, `DELETE FROM account_groups WHERE account_id = $1 AND group_id = $2`, account.ID, group.ID)
		_, _ = integrationDB.ExecContext(cleanupCtx, `DELETE FROM api_keys WHERE id = $1`, apiKey.ID)
		_, _ = integrationDB.ExecContext(cleanupCtx, `DELETE FROM accounts WHERE id = $1`, account.ID)
		_, _ = integrationDB.ExecContext(cleanupCtx, `DELETE FROM users WHERE id = $1`, user.ID)
		_, _ = integrationDB.ExecContext(cleanupCtx, `DELETE FROM groups WHERE id = $1`, group.ID)
	})

	cfg := &config.Config{
		RunMode: config.RunModeStandard,
		Default: config.DefaultConfig{RateMultiplier: 1},
		Gateway: config.GatewayConfig{
			MaxBodySize:     1 << 20,
			TextMaxBodySize: 1 << 20,
			MaxLineSize:     1 << 20,
		},
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{
				AllowPrivateHosts: true,
				AllowInsecureHTTP: true,
			},
		},
	}
	accountRepo := NewAccountRepository(client, integrationDB, nil)
	groupRepo := NewGroupRepository(client, integrationDB)
	userRepo := NewUserRepository(client, integrationDB)
	usageLogRepo := NewUsageLogRepository(client, integrationDB)
	usageBillingRepo := NewUsageBillingRepository(client, integrationDB)
	billingCacheService := service.NewBillingCacheService(nil, userRepo, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCacheService.Stop)
	deferredService := service.NewDeferredService(accountRepo, nil, time.Minute)
	usagePool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
		WorkerCount:      1,
		QueueSize:        4,
		TaskTimeout:      2 * time.Second,
		OverflowPolicy:   config.UsageRecordOverflowPolicySync,
		AutoScaleEnabled: false,
	})
	t.Cleanup(usagePool.Stop)

	gatewayService := service.NewGatewayService(
		accountRepo,
		groupRepo,
		usageLogRepo,
		usageBillingRepo,
		userRepo,
		nil,
		nil,
		nil,
		cfg,
		nil,
		nil,
		service.NewBillingService(cfg, nil),
		nil,
		billingCacheService,
		nil,
		NewHTTPUpstream(cfg),
		deferredService,
		nil,
		nil,
		nil,
		nil,
		nil,
		&service.TLSFingerprintProfileService{},
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	openAIHandler := &handler.OpenAIGatewayHandler{}
	concurrencyService := service.NewConcurrencyService(nil)
	gatewayHandler := handler.NewGatewayHandler(
		gatewayService,
		nil,
		nil,
		nil,
		nil,
		concurrencyService,
		billingCacheService,
		nil,
		nil,
		usagePool,
		nil,
		nil,
		nil,
		cfg,
		nil,
	)

	router := gin.New()
	auth := servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
		c.Set(string(servermiddleware.ContextKeyAPIKey), apiKey)
		c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{
			UserID:      user.ID,
			Concurrency: 0,
		})
		c.Next()
	})
	routes.RegisterGatewayRoutes(
		router,
		&handler.Handlers{Gateway: gatewayHandler, OpenAIGateway: openAIHandler},
		auth,
		nil,
		nil,
		nil,
		nil,
		cfg,
	)

	clientRequestID := "partial-stream-" + uuid.NewString()
	for attempt := 0; attempt < 2; attempt++ {
		requestCtx, cancel := context.WithCancel(ctx)
		rec := &cancelingResponseRecorder{
			ResponseRecorder: httptest.NewRecorder(),
			cancel:           cancel,
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(partialUsageMessagesBody)).WithContext(requestCtx)
		req.Header.Set("Authorization", "Bearer "+apiKey.Key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Anthropic-Version", "2023-06-01")
		req.Header.Set("X-Client-Request-ID", clientRequestID)

		router.ServeHTTP(rec, req)

		require.ErrorIs(t, requestCtx.Err(), context.Canceled)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, clientRequestID, rec.Header().Get("X-Client-Request-ID"))
		require.Contains(t, rec.Body.String(), `"type":"message_start"`)
		require.Contains(t, rec.Body.String(), `"text":"partial hello"`)
	}

	require.Eventually(t, func() bool {
		return usagePool.Stats().CompletedTasks >= 2
	}, 5*time.Second, 10*time.Millisecond, "two detached usage tasks should reach a terminal state")

	billingRequestID := "client:" + clientRequestID
	queryCtx, cancelQuery := context.WithTimeout(ctx, 2*time.Second)
	defer cancelQuery()

	var usageLogCount int
	var inputTokens, outputTokens, cacheReadTokens int
	var totalCost, actualCost float64
	require.NoError(t, integrationDB.QueryRowContext(queryCtx, `
		SELECT COUNT(*),
		       COALESCE(MAX(input_tokens), 0),
		       COALESCE(MAX(output_tokens), 0),
		       COALESCE(MAX(cache_read_tokens), 0),
		       COALESCE(MAX(total_cost), 0)::double precision,
		       COALESCE(MAX(actual_cost), 0)::double precision
		FROM usage_logs
		WHERE request_id = $1 AND api_key_id = $2
	`, billingRequestID, apiKey.ID).Scan(
		&usageLogCount,
		&inputTokens,
		&outputTokens,
		&cacheReadTokens,
		&totalCost,
		&actualCost,
	))
	require.Equal(t, 1, usageLogCount)
	require.Equal(t, 11, inputTokens)
	require.Equal(t, 5, outputTokens)
	require.Equal(t, 7, cacheReadTokens)
	const expectedCost = 11*3e-6 + 5*15e-6 + 7*0.3e-6
	require.InDelta(t, expectedCost, totalCost, 1e-10)
	require.InDelta(t, expectedCost, actualCost, 1e-10)

	var dedupCount int
	require.NoError(t, integrationDB.QueryRowContext(queryCtx, `
		SELECT COUNT(*)
		FROM usage_billing_dedup
		WHERE request_id = $1 AND api_key_id = $2
	`, billingRequestID, apiKey.ID).Scan(&dedupCount))
	require.Equal(t, 1, dedupCount)

	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(queryCtx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.InDelta(t, 100-expectedCost, balance, 1e-10)
}
