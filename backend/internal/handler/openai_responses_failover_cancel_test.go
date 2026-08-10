//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// openAIResponsesFailoverCancelUpstream 固定返回 HTTP 520，可在首次上游调用时
// 触发回调（用于模拟“上游在途期间客户端断开”）。
type openAIResponsesFailoverCancelUpstream struct {
	service.HTTPUpstream
	mu         sync.Mutex
	accountIDs []int64
	onFirstDo  func()
}

func (u *openAIResponsesFailoverCancelUpstream) Do(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.accountIDs = append(u.accountIDs, accountID)
	first := len(u.accountIDs) == 1
	u.mu.Unlock()
	if first && u.onFirstDo != nil {
		u.onFirstDo()
	}
	return &http.Response{
		StatusCode: 520,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(bytes.NewBufferString("<html>520: unknown error</html>")),
	}, nil
}

func (u *openAIResponsesFailoverCancelUpstream) calls() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.accountIDs...)
}

type openAIResponsesStreamingCancelUpstream struct {
	service.HTTPUpstream
	mu                  sync.Mutex
	accountIDs          []int64
	started             chan struct{}
	release             chan struct{}
	terminalBoundaryErr chan error
	semanticOutput      bool
	terminalEvent       bool
}

func (u *openAIResponsesStreamingCancelUpstream) Do(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.accountIDs = append(u.accountIDs, accountID)
	u.mu.Unlock()

	reader, writer := io.Pipe()
	go func() {
		defer func() { _ = writer.Close() }()
		if u.terminalEvent {
			_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_terminal\",\"usage\":{\"input_tokens\":3,\"output_tokens\":5}}}\n"))
			_, _ = writer.Write([]byte("event: response.completed\n"))
			close(u.started)
			<-u.release
			_, err := writer.Write([]byte("\n"))
			if u.terminalBoundaryErr != nil {
				u.terminalBoundaryErr <- err
			}
			return
		}
		firstEvent := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_abandoned\"}}\n\n"
		if u.semanticOutput {
			firstEvent = "data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_abandoned\",\"delta\":\"partial\"}\n\n"
		}
		_, _ = writer.Write([]byte(firstEvent))
		close(u.started)
		<-u.release
		_, _ = writer.Write([]byte(":\n\n"))
	}()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"req_abandoned"}},
		Body:       reader,
	}, nil
}

func (u *openAIResponsesStreamingCancelUpstream) calls() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.accountIDs...)
}

type openAIResponsesSignalingWriter struct {
	gin.ResponseWriter
	match   []byte
	written chan struct{}
	once    sync.Once
}

func (w *openAIResponsesSignalingWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	if bytes.Contains(data, w.match) {
		w.once.Do(func() { close(w.written) })
	}
	return n, err
}

func newOpenAIResponsesFailoverTestHandler(t *testing.T, upstream service.HTTPUpstream) *OpenAIGatewayHandler {
	t.Helper()
	accounts := []service.Account{
		{
			ID:          1,
			Name:        "responses-account-1",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeOAuth,
			Status:      service.StatusActive,
			Schedulable: true,
			Concurrency: 0,
			Priority:    0,
			Credentials: map[string]any{"access_token": "token-1"},
		},
		{
			ID:          2,
			Name:        "responses-account-2",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeOAuth,
			Status:      service.StatusActive,
			Schedulable: true,
			Concurrency: 0,
			Priority:    1,
			Credentials: map[string]any{"access_token": "token-2"},
		},
	}
	accountRepo := openAIImagesFailoverAccountRepo{accounts: accounts}
	cfg := &config.Config{RunMode: config.RunModeSimple, Gateway: config.GatewayConfig{OpenAIFirstOutputTimeoutSeconds: 30}}
	gatewayService := service.NewOpenAIGatewayService(
		accountRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
		nil,
		nil,
		nil,
		nil,
		nil,
		upstream,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	billingService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingService.Stop)
	concurrencyService := service.NewConcurrencyService(nil)
	handler := NewOpenAIGatewayHandler(
		gatewayService,
		concurrencyService,
		billingService,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil,
		nil,
		nil,
		nil,
		cfg,
	)
	handler.maxAccountSwitches = 10
	return handler
}

func newOpenAIResponsesFailoverTestContext(t *testing.T, ctx context.Context) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	groupID := int64(3131)
	body := []byte(`{"model":"gpt-5.1","stream":false,"input":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      99,
		GroupID: &groupID,
		Group: &service.Group{
			ID:       groupID,
			Platform: service.PlatformOpenAI,
		},
		User: &service.User{ID: 100},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100, Concurrency: 0})
	return c, rec
}

// TestOpenAIGatewayHandlerResponses_FailoverAbortsWhenClientDisconnected 复现
// #4257：客户端在上游请求在途期间断开，上游随后返回可 failover 的 520。
// 期望：不再用已取消的 context 重新选号（不触达账号 2）、不把取消误报成
// 502 账号耗尽、请求按 499 归类。
func TestOpenAIGatewayHandlerResponses_FailoverAbortsWhenClientDisconnected(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	upstream := &openAIResponsesFailoverCancelUpstream{onFirstDo: cancel}
	handler := newOpenAIResponsesFailoverTestHandler(t, upstream)
	c, rec := newOpenAIResponsesFailoverTestContext(t, ctx)

	handler.Responses(c)

	require.Equal(t, []int64{1}, upstream.calls(), "客户端断开后不应再切换到账号 2")
	require.Equal(t, statusClientClosedRequest, c.Writer.Status(), "应按 499 归类")
	require.Zero(t, rec.Body.Len(), "不应写入 502 错误响应体")

	_, hasFinalUpstreamErr := c.Get(service.OpsUpstreamStatusCodeKey)
	require.False(t, hasFinalUpstreamErr, "不应记录 failover 耗尽的上游错误终态")

	// 真实发生过的 520 应保留 failover 事件（service 层在返回 failover 错误前记录）
	rawEvents, ok := c.Get(service.OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*service.OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "failover", events[0].Kind)
	require.Equal(t, 520, events[0].UpstreamStatusCode)
}

func TestOpenAIGatewayHandlerResponses_ClientDisconnectBeforeSemanticOutputReturns499(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ctx, cancel := context.WithCancel(context.Background())
	upstream := &openAIResponsesStreamingCancelUpstream{started: make(chan struct{}), release: make(chan struct{})}
	handler := newOpenAIResponsesFailoverTestHandler(t, upstream)
	c, rec := newOpenAIResponsesFailoverTestContext(t, ctx)
	body := []byte(`{"model":"gpt-5.1","stream":true,"input":"hello"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")

	done := make(chan struct{})
	go func() {
		handler.Responses(c)
		close(done)
	}()

	select {
	case <-upstream.started:
	case <-time.After(time.Second):
		t.Fatal("upstream stream did not start")
	}
	cancel()
	close(upstream.release)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not stop after the client disconnected")
	}

	require.Equal(t, []int64{1}, upstream.calls(), "客户端断开后不应发起第二个上游请求")
	require.Equal(t, statusClientClosedRequest, c.Writer.Status(), "客户端断开应按 499 归类")
	require.Zero(t, rec.Body.Len(), "不应向已断开的客户端补写 502 响应")
}

func TestOpenAIGatewayHandlerResponses_ClientDisconnectAfterSemanticOutputStopsUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ctx, cancel := context.WithCancel(context.Background())
	upstream := &openAIResponsesStreamingCancelUpstream{
		started:        make(chan struct{}),
		release:        make(chan struct{}),
		semanticOutput: true,
	}
	defer close(upstream.release)
	handler := newOpenAIResponsesFailoverTestHandler(t, upstream)
	c, rec := newOpenAIResponsesFailoverTestContext(t, ctx)
	body := []byte(`{"model":"gpt-5.1","stream":true,"input":"hello"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	outputWritten := make(chan struct{})
	c.Writer = &openAIResponsesSignalingWriter{
		ResponseWriter: c.Writer,
		match:          []byte("partial"),
		written:        outputWritten,
	}

	done := make(chan struct{})
	go func() {
		handler.Responses(c)
		close(done)
	}()

	select {
	case <-outputWritten:
	case <-time.After(time.Second):
		t.Fatal("semantic output was not delivered before the client disconnected")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler continued draining after the client disconnected")
	}

	require.Equal(t, []int64{1}, upstream.calls(), "客户端断开后不应发起第二个上游请求")
	require.Equal(t, http.StatusOK, c.Writer.Status(), "语义输出已提交后不能覆盖响应状态")
	require.Contains(t, rec.Body.String(), "partial")
	require.NotContains(t, rec.Body.String(), "response.completed")
}

func TestOpenAIGatewayHandlerResponses_ClientDisconnectAfterTerminalBeforeCommitReturns499(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ctx, cancel := context.WithCancel(context.Background())
	upstream := &openAIResponsesStreamingCancelUpstream{
		started:             make(chan struct{}),
		release:             make(chan struct{}),
		terminalBoundaryErr: make(chan error, 1),
		terminalEvent:       true,
	}
	handler := newOpenAIResponsesFailoverTestHandler(t, upstream)
	c, rec := newOpenAIResponsesFailoverTestContext(t, ctx)
	body := []byte(`{"model":"gpt-5.1","stream":true,"input":"hello"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")

	done := make(chan struct{})
	go func() {
		handler.Responses(c)
		close(done)
	}()

	select {
	case <-upstream.started:
	case <-time.After(time.Second):
		t.Fatal("terminal event was not processed before the client disconnected")
	}
	cancel()
	close(upstream.release)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not finish after terminal usage was drained")
	}

	select {
	case err := <-upstream.terminalBoundaryErr:
		require.NoError(t, err, "terminal 后断连必须继续读取 usage 边界，而不是提前关闭 upstream body")
	case <-time.After(time.Second):
		t.Fatal("terminal usage boundary was not drained")
	}
	require.Equal(t, []int64{1}, upstream.calls())
	require.Equal(t, statusClientClosedRequest, c.Writer.Status(), "未提交响应时应按 499 归类")
	require.Empty(t, rec.Body.String(), "客户端断开后不得写出已缓冲的 terminal event")
}

// TestOpenAIGatewayHandlerResponses_FailoverContinuesForConnectedClient 回归
// 守卫：客户端在线时 failover 行为不变——切换到账号 2，两个账号都 520 后按
// 耗尽返回 502。
func TestOpenAIGatewayHandlerResponses_FailoverContinuesForConnectedClient(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &openAIResponsesFailoverCancelUpstream{}
	handler := newOpenAIResponsesFailoverTestHandler(t, upstream)
	c, rec := newOpenAIResponsesFailoverTestContext(t, nil)

	handler.Responses(c)

	require.Equal(t, []int64{1, 2}, upstream.calls(), "在线客户端应正常切换账号")
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, "upstream_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
}
