package modeltrace

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

const (
	testPublicKey = "langfuse-public"
	testSecretKey = "langfuse-secret"
)

type fakeOTLPServer struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []*collectortracepb.ExportTraceServiceRequest
	errors   []string
}

func newFakeOTLPServer(t *testing.T) *fakeOTLPServer {
	t.Helper()
	fake := &fakeOTLPServer{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			fake.recordError(fmt.Sprintf("read body: %v", err))
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		req := &collectortracepb.ExportTraceServiceRequest{}
		if err := proto.Unmarshal(body, req); err != nil {
			fake.recordError(fmt.Sprintf("decode protobuf: %v", err))
			http.Error(w, "decode protobuf", http.StatusBadRequest)
			return
		}

		fake.mu.Lock()
		fake.requests = append(fake.requests, req)
		if r.Method != http.MethodPost {
			fake.errors = append(fake.errors, fmt.Sprintf("method=%s", r.Method))
		}
		if r.URL.Path != "/api/public/otel/v1/traces" {
			fake.errors = append(fake.errors, fmt.Sprintf("path=%s", r.URL.Path))
		}
		wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(testPublicKey+":"+testSecretKey))
		if got := r.Header.Get("Authorization"); got != wantAuth {
			fake.errors = append(fake.errors, fmt.Sprintf("authorization=%q", got))
		}
		if got := r.Header.Get("x-langfuse-ingestion-version"); got != "4" {
			fake.errors = append(fake.errors, fmt.Sprintf("ingestion-version=%q", got))
		}
		fake.mu.Unlock()

		response, _ := proto.Marshal(&collectortracepb.ExportTraceServiceResponse{})
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(response)
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeOTLPServer) recordError(message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errors = append(f.errors, message)
}

func (f *fakeOTLPServer) snapshot() ([]*collectortracepb.ExportTraceServiceRequest, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*collectortracepb.ExportTraceServiceRequest(nil), f.requests...), append([]string(nil), f.errors...)
}

func TestModelTraceOTLPChatCompletions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := newFakeOTLPServer(t)
	manager, err := NewManager(context.Background(), config.ModelTracingConfig{
		Enabled:          true,
		Endpoint:         fake.server.URL + "/api/public/otel",
		PublicKey:        testPublicKey,
		SecretKey:        testSecretKey,
		PromptMaxBytes:   4096,
		ResponseMaxBytes: 4096,
	})
	require.NoError(t, err)

	const (
		requestID = "request-otel-1"
		userID    = int64(42)
		apiKeyID  = int64(73)
		groupID   = int64(99)
	)
	requestBody := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`)
	responseBody := []byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hi"}}]}`)

	router := gin.New()
	router.POST("/v1/chat/completions",
		func(c *gin.Context) {
			c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{ID: apiKeyID, GroupID: int64Pointer(groupID)})
			c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: userID})
			c.Next()
		},
		manager.Middleware(),
		func(c *gin.Context) {
			got, readErr := io.ReadAll(c.Request.Body)
			require.NoError(t, readErr)
			require.JSONEq(t, string(requestBody), string(got))
			c.Data(http.StatusOK, "application/json", responseBody)
		},
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Request-ID", requestID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, string(responseBody), w.Body.String())

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, manager.Shutdown(shutdownCtx))

	requests, serverErrors := fake.snapshot()
	require.Empty(t, serverErrors)
	require.Len(t, requests, 1, "one OTLP export should contain the request Trace")
	spans := exportedSpans(requests)
	require.Len(t, spans, 2)

	root := spanNamed(t, spans, rootSpanName)
	generation := spanNamed(t, spans, generationSpanName)
	require.Len(t, root.TraceId, 16)
	require.NotEqual(t, make([]byte, 16), root.TraceId)
	require.Equal(t, root.TraceId, generation.TraceId)
	require.Empty(t, root.ParentSpanId)
	require.Equal(t, root.SpanId, generation.ParentSpanId)

	rootAttrs := attributesByKey(root.Attributes)
	require.Equal(t, rootSpanName, stringAttribute(t, rootAttrs, "langfuse.trace.name"))
	require.Equal(t, requestID, stringAttribute(t, rootAttrs, "langfuse.trace.metadata.request_id"))
	require.Equal(t, fmt.Sprint(userID), stringAttribute(t, rootAttrs, "langfuse.user.id"))
	require.Equal(t, apiKeyID, intAttribute(t, rootAttrs, "langfuse.trace.metadata.api_key_id"))
	require.Equal(t, groupID, intAttribute(t, rootAttrs, "langfuse.trace.metadata.group_id"))
	require.JSONEq(t, string(requestBody), stringAttribute(t, rootAttrs, "langfuse.observation.input"))
	require.JSONEq(t, string(responseBody), stringAttribute(t, rootAttrs, "langfuse.observation.output"))

	generationAttrs := attributesByKey(generation.Attributes)
	require.Equal(t, "generation", stringAttribute(t, generationAttrs, "langfuse.observation.type"))
	require.Equal(t, "gpt-test", stringAttribute(t, generationAttrs, "langfuse.observation.model.name"))
	require.JSONEq(t, string(requestBody), stringAttribute(t, generationAttrs, "langfuse.observation.input"))
	require.JSONEq(t, string(responseBody), stringAttribute(t, generationAttrs, "langfuse.observation.output"))
}

func TestModelTraceDisabledWithoutTarget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := newFakeOTLPServer(t)

	for _, tc := range []struct {
		name string
		cfg  config.ModelTracingConfig
	}{
		{
			name: "disabled",
			cfg: config.ModelTracingConfig{
				Enabled:   false,
				Endpoint:  fake.server.URL + "/api/public/otel",
				PublicKey: testPublicKey,
				SecretKey: testSecretKey,
			},
		},
		{
			name: "no endpoint",
			cfg: config.ModelTracingConfig{
				Enabled:   true,
				PublicKey: testPublicKey,
				SecretKey: testSecretKey,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, err := NewManager(context.Background(), tc.cfg)
			require.NoError(t, err)
			require.False(t, manager.Enabled())

			router := gin.New()
			router.POST("/v1/chat/completions", manager.Middleware(), func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"ok": true})
			})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-test"}`)))
			require.Equal(t, http.StatusOK, w.Code)

			shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			require.NoError(t, manager.Shutdown(shutdownCtx))
		})
	}

	requests, serverErrors := fake.snapshot()
	require.Empty(t, serverErrors)
	require.Empty(t, requests, "disabled or targetless tracing must not issue OTLP requests")
}

func int64Pointer(value int64) *int64 { return &value }

func exportedSpans(requests []*collectortracepb.ExportTraceServiceRequest) []*tracepb.Span {
	var spans []*tracepb.Span
	for _, request := range requests {
		for _, resourceSpans := range request.ResourceSpans {
			for _, scopeSpans := range resourceSpans.ScopeSpans {
				spans = append(spans, scopeSpans.Spans...)
			}
		}
	}
	return spans
}

func spanNamed(t *testing.T, spans []*tracepb.Span, name string) *tracepb.Span {
	t.Helper()
	for _, span := range spans {
		if span.Name == name {
			return span
		}
	}
	t.Fatalf("span %q not found", name)
	return nil
}

func attributesByKey(attributes []*commonpb.KeyValue) map[string]*commonpb.AnyValue {
	result := make(map[string]*commonpb.AnyValue, len(attributes))
	for _, attribute := range attributes {
		result[attribute.Key] = attribute.Value
	}
	return result
}

func stringAttribute(t *testing.T, attributes map[string]*commonpb.AnyValue, key string) string {
	t.Helper()
	value, ok := attributes[key]
	require.True(t, ok, "attribute %q is missing", key)
	_, ok = value.Value.(*commonpb.AnyValue_StringValue)
	require.True(t, ok, "attribute %q is not a string", key)
	return value.GetStringValue()
}

func intAttribute(t *testing.T, attributes map[string]*commonpb.AnyValue, key string) int64 {
	t.Helper()
	value, ok := attributes[key]
	require.True(t, ok, "attribute %q is missing", key)
	_, ok = value.Value.(*commonpb.AnyValue_IntValue)
	require.True(t, ok, "attribute %q is not an integer", key)
	return value.GetIntValue()
}
