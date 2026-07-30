package modeltrace_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/modeltrace"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"
)

// 验收：原始字符串包含非法字节时，capture 边界将其替换为 U+FFFD，
// 输出始终为有效 UTF-8，合法内容不丢失。修复前非法字节原样透出导致
// OTLP 序列化失败、整批 spans 丢失。
func TestIssue51RawInvalidUTF8IsNormalized(t *testing.T) {
	raw := []byte("hello\xff\xe4\xb8\x96\xe7\x95\x8c") // hello + <孤立0xFF> + 世界
	got := modeltrace.TestingCaptureModelContent(raw, len(raw), 4096, modeltrace.TestingCapturePolicy{})
	require.True(t, utf8.ValidString(got), "输出必须是有效 UTF-8: %q", got)
	require.Contains(t, got, "hello")
	require.Contains(t, got, "世界")
	require.Contains(t, got, "\uFFFD")
}

func TestIssue51OmittedExportSettingsUseCompatibleRuntimeDefaults(t *testing.T) {
	manager, err := modeltrace.NewManager(context.Background(), config.ModelTracingConfig{})
	require.NoError(t, err)
	got := manager.Config()
	require.Equal(t, 60, got.ExportTimeoutSeconds)
	require.Equal(t, config.ModelTracingExportRetryConfig{
		Enabled: true, InitialIntervalSeconds: 5, MaxIntervalSeconds: 30, MaxElapsedTimeSeconds: 55,
	}, got.ExportRetry)
	require.Equal(t, 256, got.ExportQueueSize)
	require.Equal(t, 16, got.ExportBatchSize)
	require.Equal(t, 1000, got.ExportBatchTimeoutMs)
}

// 验收：从真实 HTTP middleware 到 OTLP/HTTP exporter 的完整路径中，所有
// string attributes 都必须在 protobuf 序列化前规范化为有效 UTF-8。
func TestIssue51InvalidDynamicAttributeExportsThroughOTLP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := newFakeOTLPServer(t)
	manager, err := modeltrace.NewManager(context.Background(), config.ModelTracingConfig{
		Enabled: true, Endpoint: fake.server.URL + "/api/public/otel",
		PublicKey: testPublicKey, SecretKey: testSecretKey,
		PromptMaxBytes: 4096, ResponseMaxBytes: 4096,
	})
	require.NoError(t, err)

	router := gin.New()
	router.POST("/v1/chat/completions", manager.CandidateMiddleware(), func(c *gin.Context) {
		apiKey := &service.APIKey{ID: 51, UserID: 52, User: &service.User{ID: 52}}
		servermiddleware.SetOpsFallbackAPIKey(c, apiKey)
		c.Set(string(servermiddleware.ContextKeyAPIKey), apiKey)
		c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 52})
		c.Data(http.StatusOK, "application/json", []byte(`{"id":"ok","output":"中文😀"}`))
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-test","messages":[{"role":"user","content":"中文😀"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Request-ID", "request-\xff")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	require.Equal(t, http.StatusOK, response.Code)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, manager.Shutdown(shutdownCtx))
	requests, serverErrors := fake.snapshot()
	require.Empty(t, serverErrors)
	require.Len(t, requests, 1, "invalid dynamic attribute must not discard the OTLP batch")
	spans := exportedSpans(requests)
	require.Len(t, spans, 1)
	root := spanNamed(t, spans, modeltrace.TestingRootSpanName)
	assertValidUTF8Attributes(t, root.Attributes)
	require.Equal(t, "request-\uFFFD", stringAttribute(t, attributesByKey(root.Attributes), "langfuse.trace.metadata.request_id"))
}

func TestIssue51ExternalLangfuseSmoke(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("ISSUE51_SMOKE_OTLP_ENDPOINT"))
	if endpoint == "" {
		t.Skip("ISSUE51_SMOKE_OTLP_ENDPOINT is not set")
	}
	sessionID := strings.TrimSpace(os.Getenv("ISSUE51_SMOKE_SESSION_ID"))
	if sessionID == "" {
		sessionID = "issue51-smoke"
	}
	manager, err := modeltrace.NewManager(context.Background(), config.ModelTracingConfig{
		Enabled: true, Endpoint: endpoint,
		PublicKey: "pk-lf-issue51-smoke", SecretKey: "sk-lf-issue51-smoke",
		PromptMaxBytes: 4096, ResponseMaxBytes: 4096,
		ExportTimeoutSeconds: 60,
		ExportRetry: config.ModelTracingExportRetryConfig{
			Enabled: true, InitialIntervalSeconds: 1, MaxIntervalSeconds: 5, MaxElapsedTimeSeconds: 50,
		},
		ExportQueueSize: 16, ExportBatchSize: 1, ExportBatchTimeoutMs: 10,
	})
	require.NoError(t, err)

	requestBody := []byte(`{"model":"gpt-smoke","session_id":"` + sessionID + `","input":[{"type":"message","role":"user","id":"u1","content":[{"type":"input_text","text":"第一轮：你好😀，查看 https://user:pass@example.com/private?token=secret"}]},{"type":"message","role":"assistant","id":"a1","content":[{"type":"output_text","text":"第一轮答复"}]},{"type":"message","role":"user","id":"u2","content":[{"type":"input_text","text":"第二轮：继续验证中文与 emoji 🚀"}]}]}`)
	responseBody := []byte(`{"id":"resp-issue51","output":[{"type":"message","role":"assistant","id":"a2","content":[{"type":"output_text","text":"第二轮答复完成✅"}]}]}`)
	router := gin.New()
	router.POST("/v1/responses",
		manager.CandidateMiddleware(),
		func(c *gin.Context) {
			apiKey := &service.APIKey{ID: 51, UserID: 52, User: &service.User{ID: 52}}
			servermiddleware.SetOpsFallbackAPIKey(c, apiKey)
			c.Set(string(servermiddleware.ContextKeyAPIKey), apiKey)
			c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 52})
			c.Next()
		},
		func(c *gin.Context) {
			_, readErr := io.ReadAll(c.Request.Body)
			require.NoError(t, readErr)
			c.Data(http.StatusOK, "application/json", responseBody)
		},
	)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)

	require.Eventually(t, func() bool {
		stats := modeltrace.TestingManagerExportStats(manager)
		return stats.Ended > 0 && stats.Exported == stats.Ended && stats.Failed == 0
	}, 55*time.Second, 100*time.Millisecond)
	stats := modeltrace.TestingManagerExportStats(manager)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, manager.Shutdown(shutdownCtx))
	t.Logf("session_id=%s response_status=%d stats=%+v", sessionID, response.Code, stats)
}

func assertValidUTF8Attributes(t *testing.T, attrs []*commonpb.KeyValue) {
	t.Helper()
	for _, attr := range attrs {
		assertValidUTF8Value(t, attr.Key, attr.Value)
	}
}

func assertValidUTF8Value(t *testing.T, key string, value *commonpb.AnyValue) {
	t.Helper()
	switch typed := value.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		require.True(t, utf8.ValidString(typed.StringValue), "attribute %q contains invalid UTF-8: %q", key, typed.StringValue)
	case *commonpb.AnyValue_ArrayValue:
		for _, item := range typed.ArrayValue.Values {
			assertValidUTF8Value(t, key, item)
		}
	case *commonpb.AnyValue_KvlistValue:
		assertValidUTF8Attributes(t, typed.KvlistValue.Values)
	}
}

// 验收：多字节字符恰好跨越截断边界时，输出保持有效 UTF-8 且不超过配置字节数。
func TestIssue51TruncateAtMultibyteBoundary(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		limit int
		want  string
	}{
		{name: "one byte", raw: "AB", limit: 1, want: "A"},
		{name: "inside two byte rune", raw: "A¢B", limit: 2, want: "A"},
		{name: "after two byte rune", raw: "A¢B", limit: 3, want: "A¢"},
		{name: "inside three byte rune", raw: "A世B", limit: 3, want: "A"},
		{name: "after three byte rune", raw: "A世B", limit: 4, want: "A世"},
		{name: "inside four byte rune", raw: "A😀B", limit: 4, want: "A"},
		{name: "after four byte rune", raw: "A😀B", limit: 5, want: "A😀"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := modeltrace.TestingCaptureModelContent([]byte(test.raw), len([]byte(test.raw)), test.limit, modeltrace.TestingCapturePolicy{})
			captured := capturedPrefix(got)
			require.Equal(t, test.want, captured)
			require.True(t, utf8.ValidString(captured), "截断后必须有效 UTF-8: %q", captured)
			require.LessOrEqual(t, len([]byte(captured)), test.limit)
		})
	}
}

// 验收：replacement 后仍满足最大字节限制，且不切断多字节字符。
func TestIssue51ReplacementRespectsByteLimit(t *testing.T) {
	raw := []byte("ab\xff\xff\xff\xe4\xb8\x96\xe7\x95\x8c")
	limit := 10
	got := modeltrace.TestingCaptureModelContent(raw, len(raw), limit, modeltrace.TestingCapturePolicy{})
	captured := strings.Split(got, "[truncated:")[0]
	require.True(t, utf8.ValidString(captured))
	require.LessOrEqual(t, len([]byte(captured)), limit)
}

// 验收：流式分片在 UTF-8 字符中间断开（导入的原始片段含孤立首字节），
// 经 capture 边界规范化后输出有效 UTF-8。
func TestIssue51StreamingShardMidRune(t *testing.T) {
	shard := []byte("delta:\xe4") // 3 字节中文被切成只剩首字节
	got := modeltrace.TestingCaptureModelContent(shard, len(shard), 4096, modeltrace.TestingCapturePolicy{})
	require.True(t, utf8.ValidString(got), "中层断开的分片必须被规范化: %q", got)
	require.Contains(t, got, "delta:")
}

// 验收：大型 prompt/response 在规范化后仍受字节限制且不切断 rune。
func TestIssue51LargePromptAndResponseAttributesRemainValidAndBounded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := newFakeOTLPServer(t)
	manager, err := modeltrace.NewManager(context.Background(), config.ModelTracingConfig{
		Enabled: true, Endpoint: fake.server.URL + "/api/public/otel",
		PublicKey: testPublicKey, SecretKey: testSecretKey,
		PromptMaxBytes: 64, ResponseMaxBytes: 64,
	})
	require.NoError(t, err)

	prompt := append(bytes.Repeat([]byte("你"), 100), []byte("\xffprompt")...)
	responseBody := append(bytes.Repeat([]byte("界"), 100), []byte("\xffresponse")...)
	router := gin.New()
	router.POST("/v1/chat/completions",
		manager.CandidateMiddleware(),
		func(c *gin.Context) {
			apiKey := &service.APIKey{ID: 51, UserID: 52, User: &service.User{ID: 52}}
			servermiddleware.SetOpsFallbackAPIKey(c, apiKey)
			c.Set(string(servermiddleware.ContextKeyAPIKey), apiKey)
			c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 52})
			c.Next()
		},
		func(c *gin.Context) {
			_, readErr := io.ReadAll(c.Request.Body)
			require.NoError(t, readErr)
			c.Data(http.StatusOK, "application/json", responseBody)
		},
	)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(prompt))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(httptest.NewRecorder(), request)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, manager.Shutdown(shutdownCtx))
	requests, serverErrors := fake.snapshot()
	require.Empty(t, serverErrors)
	root := spanNamed(t, exportedSpans(requests), modeltrace.TestingRootSpanName)
	assertValidUTF8Attributes(t, root.Attributes)
	attrs := attributesByKey(root.Attributes)
	for _, key := range []string{"langfuse.observation.input", "langfuse.observation.output"} {
		t.Run(key, func(t *testing.T) {
			value := stringAttribute(t, attrs, key)
			require.True(t, utf8.ValidString(value))
			require.LessOrEqual(t, len([]byte(capturedPrefix(value))), 64)
			require.Contains(t, value, "[truncated:")
		})
	}
}

func capturedPrefix(value string) string {
	return strings.SplitN(value, "[truncated:", 2)[0]
}

// 验收：真实 OTLP/HTTP Collector 连续两次返回可重试的 503 后恢复，
// exporter 在预算内完成第三次发送，业务响应不等待重试且 failed_spans 不增长。
func TestIssue51CollectorRetryRecoversThenExportSucceeds(t *testing.T) {
	collector := newSequencedOTLPServer(t, http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusOK)
	manager := newIssue51RetryManager(t, collector.server.URL, 5)

	started := time.Now()
	status := serveIssue51BusinessRequest(manager)
	require.Equal(t, http.StatusAccepted, status)
	require.Less(t, time.Since(started), 500*time.Millisecond, "business response must not wait for exporter retries")

	require.Eventually(t, func() bool {
		attempts, accepted, serverErrors := collector.snapshot()
		return attempts == 3 && len(accepted) == 1 && len(serverErrors) == 0
	}, 8*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool {
		return modeltrace.TestingManagerExportStats(manager).Exported == 1
	}, time.Second, 10*time.Millisecond)
	stats := modeltrace.TestingManagerExportStats(manager)
	require.Equal(t, modeltrace.TestingExportStatsSnapshot{Ended: 1, Attempted: 1, Exported: 1}, stats)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, manager.Shutdown(shutdownCtx))
}

// 验收：Collector 持续 503 直至重试预算耗尽，业务请求仍返回成功；
// 终态只计一个 dropped span，并准确归类为 collector_refused。
func TestIssue51CollectorRetryBudgetExhaustedIsClassifiedAndFailOpen(t *testing.T) {
	collector := newSequencedOTLPServer(t, http.StatusServiceUnavailable)
	manager := newIssue51RetryManager(t, collector.server.URL, 2)

	started := time.Now()
	status := serveIssue51BusinessRequest(manager)
	require.Equal(t, http.StatusAccepted, status)
	require.Less(t, time.Since(started), 500*time.Millisecond, "business response must fail open")

	require.Eventually(t, func() bool {
		return modeltrace.TestingManagerExportStats(manager).Failed == 1
	}, 7*time.Second, 20*time.Millisecond)
	attempts, accepted, serverErrors := collector.snapshot()
	require.GreaterOrEqual(t, attempts, 2, "exporter must retry before exhausting its budget")
	require.Empty(t, accepted)
	require.Empty(t, serverErrors)
	require.Equal(t, modeltrace.TestingExportStatsSnapshot{
		Ended: 1, Attempted: 1, Failed: 1, PendingOrDropped: 0, FailedCollectorRefused: 1,
	}, modeltrace.TestingManagerExportStats(manager))

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, manager.Shutdown(shutdownCtx))
}

func TestIssue51CollectorConnectionRefusedRecoversWithinRetryBudget(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := probe.Addr().String()
	require.NoError(t, probe.Close())

	manager := newIssue51RetryManager(t, "http://"+address, 5)
	status := serveIssue51BusinessRequest(manager)
	require.Equal(t, http.StatusAccepted, status)
	require.Never(t, func() bool {
		return modeltrace.TestingManagerExportStats(manager).Exported > 0
	}, 250*time.Millisecond, 20*time.Millisecond)

	listener, err := net.Listen("tcp", address)
	require.NoError(t, err)
	accepted := make(chan struct{}, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		request := &collectortracepb.ExportTraceServiceRequest{}
		if readErr != nil || proto.Unmarshal(body, request) != nil {
			http.Error(w, "invalid OTLP request", http.StatusBadRequest)
			return
		}
		accepted <- struct{}{}
		response, _ := proto.Marshal(&collectortracepb.ExportTraceServiceResponse{})
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(response)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	require.Eventually(t, func() bool {
		return len(accepted) == 1 && modeltrace.TestingManagerExportStats(manager).Exported == 1
	}, 4*time.Second, 20*time.Millisecond)
	require.Equal(t, modeltrace.TestingExportStatsSnapshot{Ended: 1, Attempted: 1, Exported: 1}, modeltrace.TestingManagerExportStats(manager))

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, manager.Shutdown(shutdownCtx))
}

func TestIssue51CollectorConnectionRefusedBudgetExhaustedIsClassifiedAsTimeout(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := probe.Addr().String()
	require.NoError(t, probe.Close())

	manager := newIssue51RetryManager(t, "http://"+address, 2)
	status := serveIssue51BusinessRequest(manager)
	require.Equal(t, http.StatusAccepted, status)

	require.Eventually(t, func() bool {
		return modeltrace.TestingManagerExportStats(manager).Failed == 1
	}, 7*time.Second, 20*time.Millisecond)
	require.Equal(t, modeltrace.TestingExportStatsSnapshot{
		Ended: 1, Attempted: 1, Failed: 1, PendingOrDropped: 0, FailedTimeout: 1,
	}, modeltrace.TestingManagerExportStats(manager))

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, manager.Shutdown(shutdownCtx))
}

func newIssue51RetryManager(t *testing.T, serverURL string, maxElapsedSeconds int) *modeltrace.Manager {
	t.Helper()
	manager, err := modeltrace.NewManager(context.Background(), config.ModelTracingConfig{
		Enabled: true, Endpoint: serverURL + "/api/public/otel",
		PublicKey: testPublicKey, SecretKey: testSecretKey,
		PromptMaxBytes: 4096, ResponseMaxBytes: 4096,
		ExportTimeoutSeconds: 10,
		ExportRetry: config.ModelTracingExportRetryConfig{
			Enabled: true, InitialIntervalSeconds: 1, MaxIntervalSeconds: 1, MaxElapsedTimeSeconds: maxElapsedSeconds,
		},
		ExportQueueSize: 16, ExportBatchSize: 1, ExportBatchTimeoutMs: 10,
	})
	require.NoError(t, err)
	return manager
}

func serveIssue51BusinessRequest(manager *modeltrace.Manager) int {
	router := gin.New()
	router.POST("/v1/chat/completions", manager.CandidateMiddleware(), func(c *gin.Context) {
		apiKey := &service.APIKey{ID: 51, UserID: 52, User: &service.User{ID: 52}}
		servermiddleware.SetOpsFallbackAPIKey(c, apiKey)
		c.Set(string(servermiddleware.ContextKeyAPIKey), apiKey)
		c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 52})
		c.JSON(http.StatusAccepted, gin.H{"status": "accepted"})
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-test"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	return response.Code
}

type sequencedOTLPServer struct {
	server *httptest.Server

	mu       sync.Mutex
	statuses []int
	attempts int
	accepted []*collectortracepb.ExportTraceServiceRequest
	errors   []string
}

func newSequencedOTLPServer(t *testing.T, statuses ...int) *sequencedOTLPServer {
	t.Helper()
	require.NotEmpty(t, statuses)
	collector := &sequencedOTLPServer{statuses: append([]int(nil), statuses...)}
	collector.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			collector.recordError("read body: " + err.Error())
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		request := &collectortracepb.ExportTraceServiceRequest{}
		if err := proto.Unmarshal(body, request); err != nil {
			collector.recordError("decode protobuf: " + err.Error())
			http.Error(w, "decode protobuf", http.StatusBadRequest)
			return
		}

		collector.mu.Lock()
		collector.attempts++
		index := min(collector.attempts-1, len(collector.statuses)-1)
		status := collector.statuses[index]
		if r.Method != http.MethodPost {
			collector.errors = append(collector.errors, "method="+r.Method)
		}
		if r.URL.Path != "/api/public/otel/v1/traces" {
			collector.errors = append(collector.errors, "path="+r.URL.Path)
		}
		if status == http.StatusOK {
			collector.accepted = append(collector.accepted, request)
		}
		collector.mu.Unlock()

		if status != http.StatusOK {
			http.Error(w, http.StatusText(status), status)
			return
		}
		response, _ := proto.Marshal(&collectortracepb.ExportTraceServiceResponse{})
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(response)
	}))
	t.Cleanup(collector.server.Close)
	return collector
}

func (s *sequencedOTLPServer) recordError(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errors = append(s.errors, message)
}

func (s *sequencedOTLPServer) snapshot() (int, []*collectortracepb.ExportTraceServiceRequest, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts, append([]*collectortracepb.ExportTraceServiceRequest(nil), s.accepted...), append([]string(nil), s.errors...)
}
