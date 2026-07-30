package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	responsesBillingChainCorrelationID = "codex-thread-123"
	responsesBillingChainTimeout       = 10 * time.Second
)

type responsesBillingChainRepo struct {
	service.UsageBillingRepository

	mu       sync.Mutex
	commands []service.UsageBillingCommand
	applied  map[string]string
	charged  float64
}

func newResponsesBillingChainRepo() *responsesBillingChainRepo {
	return &responsesBillingChainRepo{applied: make(map[string]string)}
}

func (r *responsesBillingChainRepo) Apply(_ context.Context, cmd *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	if cmd == nil {
		return nil, service.ErrUsageBillingRequestIDRequired
	}
	cloned := *cmd
	cloned.Normalize()

	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, cloned)
	if fingerprint, exists := r.applied[cloned.RequestID]; exists {
		if fingerprint != cloned.RequestFingerprint {
			return nil, service.ErrUsageBillingRequestConflict
		}
		return &service.UsageBillingApplyResult{Applied: false}, nil
	}
	r.applied[cloned.RequestID] = cloned.RequestFingerprint
	r.charged += cloned.BalanceCost
	return &service.UsageBillingApplyResult{Applied: true}, nil
}

func (r *responsesBillingChainRepo) snapshot() ([]service.UsageBillingCommand, int, float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]service.UsageBillingCommand(nil), r.commands...), len(r.applied), r.charged
}

type responsesBillingChainHTTPUpstream struct {
	service.HTTPUpstream
	calls  int
	stream bool
}

func (u *responsesBillingChainHTTPUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	response := fmt.Sprintf(`{"id":"resp_http_%d","object":"response","status":"completed","model":"gpt-5.4","usage":{"input_tokens":%d,"output_tokens":1,"total_tokens":%d}}`, u.calls, u.calls+1, u.calls+2)
	contentType := "application/json"
	body := response
	if u.stream {
		contentType = "text/event-stream"
		body = fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

type responsesBillingChainUserRepo struct {
	service.UserRepository
}

func (responsesBillingChainUserRepo) GetByID(_ context.Context, id int64) (*service.User, error) {
	return &service.User{ID: id, Status: service.StatusActive, Balance: 100}, nil
}

func TestOpenAIResponsesHTTPBillingChainUsesExecutionIDsAcrossCorrelatedTurns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	testCases := []struct {
		name     string
		endpoint string
		stream   bool
	}{
		{name: "responses_json", endpoint: "/openai/v1/responses"},
		{name: "responses_sse", endpoint: "/openai/v1/responses", stream: true},
		{name: "responses_compact", endpoint: "/openai/v1/responses/compact"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			billingRepo := newResponsesBillingChainRepo()
			usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 2)}
			upstream := &responsesBillingChainHTTPUpstream{stream: tc.stream}
			h, apiKey := newResponsesBillingChainHandler(t, billingRepo, usageRepo, upstream, "")
			router := newResponsesBillingChainRouter(h, apiKey)

			for turn := 1; turn <= 2; turn++ {
				body := fmt.Sprintf(`{"model":"gpt-5.4","input":"turn %d","stream":%t}`, turn, tc.stream)
				req := httptest.NewRequest(http.MethodPost, tc.endpoint, bytes.NewBufferString(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Client-Request-ID", responsesBillingChainCorrelationID)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Equal(t, responsesBillingChainCorrelationID, rec.Header().Get("X-Client-Request-ID"))
				wantID := fmt.Sprintf("resp_http_%d", turn)
				if tc.stream {
					require.Contains(t, rec.Body.String(), wantID)
				} else {
					require.Equal(t, wantID, gjson.Get(rec.Body.String(), "id").String())
				}
			}

			assertResponsesBillingChain(t, billingRepo, usageRepo.created, []string{"resp_http_1", "resp_http_2"})
		})
	}
}

func TestOpenAIResponsesWebSocketBillingChainUsesExecutionIDsAcrossCorrelatedTurns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstreamDone := make(chan error, 1)
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			upstreamDone <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for turn := 1; turn <= 2; turn++ {
			readCtx, cancelRead := context.WithTimeout(r.Context(), responsesBillingChainTimeout)
			_, _, readErr := conn.Read(readCtx)
			cancelRead()
			if readErr != nil {
				upstreamDone <- readErr
				return
			}
			event := fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_ws_%d","model":"gpt-5.4","usage":{"input_tokens":%d,"output_tokens":1,"total_tokens":%d}}}`, turn, turn+1, turn+2)
			writeCtx, cancelWrite := context.WithTimeout(r.Context(), responsesBillingChainTimeout)
			writeErr := conn.Write(writeCtx, coderws.MessageText, []byte(event))
			cancelWrite()
			if writeErr != nil {
				upstreamDone <- writeErr
				return
			}
		}
		upstreamDone <- nil
	}))
	defer upstreamServer.Close()

	billingRepo := newResponsesBillingChainRepo()
	usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 2)}
	h, apiKey := newResponsesBillingChainHandler(t, billingRepo, usageRepo, nil, upstreamServer.URL)
	router := newResponsesBillingChainRouter(h, apiKey)
	handlerServer := httptest.NewServer(router)
	defer handlerServer.Close()

	headers := http.Header{"X-Client-Request-ID": []string{responsesBillingChainCorrelationID}}
	dialCtx, cancelDial := context.WithTimeout(context.Background(), responsesBillingChainTimeout)
	clientConn, response, err := coderws.Dial(
		dialCtx,
		"ws"+strings.TrimPrefix(handlerServer.URL, "http")+"/openai/v1/responses",
		&coderws.DialOptions{HTTPHeader: headers, CompressionMode: coderws.CompressionContextTakeover},
	)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()
	require.Equal(t, responsesBillingChainCorrelationID, response.Header.Get("X-Client-Request-ID"))

	for turn := 1; turn <= 2; turn++ {
		writeCtx, cancelWrite := context.WithTimeout(context.Background(), responsesBillingChainTimeout)
		err = clientConn.Write(writeCtx, coderws.MessageText, []byte(fmt.Sprintf(`{"type":"response.create","model":"gpt-5.4","input":"turn %d"}`, turn)))
		cancelWrite()
		require.NoError(t, err)

		readCtx, cancelRead := context.WithTimeout(context.Background(), responsesBillingChainTimeout)
		_, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())
		require.Equal(t, fmt.Sprintf("resp_ws_%d", turn), gjson.GetBytes(event, "response.id").String())
	}
	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case upstreamErr := <-upstreamDone:
		require.NoError(t, upstreamErr)
	case <-time.After(responsesBillingChainTimeout):
		t.Fatal("等待上游完成两个 WebSocket turn 超时")
	}

	assertResponsesBillingChain(t, billingRepo, usageRepo.created, []string{"resp_ws_1", "resp_ws_2"})
}

func newResponsesBillingChainHandler(
	t *testing.T,
	billingRepo service.UsageBillingRepository,
	usageRepo service.UsageLogRepository,
	httpUpstream service.HTTPUpstream,
	websocketBaseURL string,
) (*OpenAIGatewayHandler, *service.APIKey) {
	t.Helper()
	groupID := int64(4310)
	if websocketBaseURL == "" {
		websocketBaseURL = "https://api.example.test"
	}
	account := service.Account{
		ID:          9310,
		Name:        "responses-billing-chain",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": websocketBaseURL},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
			"openai_apikey_responses_websockets_v2_mode":    service.OpenAIWSIngressModePassthrough,
		},
	}
	accountRepo := &openAIWSUsageHandlerAccountRepoStub{account: account}
	cfg := &config.Config{}
	cfg.RunMode = config.RunModeStandard
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 10
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 10
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 10
	cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 10

	billingService := service.NewBillingService(cfg, nil)
	userRepo := responsesBillingChainUserRepo{}
	billingCacheService := service.NewBillingCacheService(nil, userRepo, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCacheService.Stop)
	gatewayService := service.NewOpenAIGatewayService(
		accountRepo,
		usageRepo,
		billingRepo,
		userRepo,
		nil,
		nil,
		nil,
		cfg,
		nil,
		nil,
		billingService,
		nil,
		billingCacheService,
		httpUpstream,
		&service.DeferredService{},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	cache := &concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	}
	h := &OpenAIGatewayHandler{
		gatewayService:      gatewayService,
		billingCacheService: billingCacheService,
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
		maxAccountSwitches:  1,
	}
	apiKey := &service.APIKey{
		ID:      8310,
		UserID:  7310,
		GroupID: &groupID,
		Status:  service.StatusActive,
		User:    &service.User{ID: 7310, Status: service.StatusActive, Balance: 100},
		Group: &service.Group{
			ID:             groupID,
			Platform:       service.PlatformOpenAI,
			Status:         service.StatusActive,
			RateMultiplier: 1,
			Hydrated:       true,
		},
	}
	return h, apiKey
}

func newResponsesBillingChainRouter(h *OpenAIGatewayHandler, apiKey *service.APIKey) *gin.Engine {
	router := gin.New()
	router.Use(middleware.ClientRequestID())
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.User.ID, Concurrency: 1})
		c.Next()
	})
	router.POST("/openai/v1/responses", h.Responses)
	router.POST("/openai/v1/responses/compact", h.Responses)
	router.GET("/openai/v1/responses", h.ResponsesWebSocket)
	return router
}

func assertResponsesBillingChain(
	t *testing.T,
	billingRepo *responsesBillingChainRepo,
	usageLogs <-chan *service.UsageLog,
	wantExecutionIDs []string,
) {
	t.Helper()
	commands, applied, charged := billingRepo.snapshot()
	require.Len(t, commands, len(wantExecutionIDs))
	require.Equal(t, len(wantExecutionIDs), applied, "每个 Responses turn 必须形成独立幂等计费")
	require.Positive(t, charged, "standard 模式必须产生实际余额扣费")

	seen := make(map[string]struct{}, len(commands))
	for index, command := range commands {
		require.Equal(t, wantExecutionIDs[index], command.RequestID)
		require.NotEqual(t, responsesBillingChainCorrelationID, command.RequestID)
		require.Positive(t, command.BalanceCost)
		_, duplicate := seen[command.RequestID]
		require.False(t, duplicate, "不同 turn 不得共享 billing request ID")
		seen[command.RequestID] = struct{}{}
	}

	for _, wantID := range wantExecutionIDs {
		select {
		case usageLog := <-usageLogs:
			require.Equal(t, wantID, usageLog.RequestID)
			require.Positive(t, usageLog.ActualCost)
		case <-time.After(3 * time.Second):
			t.Fatalf("等待 usage log %s 超时", wantID)
		}
	}
}
