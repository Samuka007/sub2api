package modeltrace_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/modeltrace"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// BenchmarkModelTraceRequestOverhead measures only the synchronous gateway cost.
// The enabled cases use the production BatchSpanProcessor limits with a local
// discard exporter, so no network latency is included in the request path.
func BenchmarkModelTraceRequestOverhead(b *testing.B) {
	b.Run("disabled", func(b *testing.B) { benchmarkModelTraceMode(b, false) })
	b.Run("enabled", func(b *testing.B) { benchmarkModelTraceMode(b, true) })
}

func BenchmarkModelTraceDisabled(b *testing.B) { benchmarkModelTraceMode(b, false) }

func BenchmarkModelTraceEnabled(b *testing.B) { benchmarkModelTraceMode(b, true) }

func BenchmarkModelTraceCaptureLargeJSON(b *testing.B) {
	for _, size := range []int{1 << 20, 4 << 20} {
		size := size
		b.Run(fmt.Sprintf("plain/%dMiB", size>>20), func(b *testing.B) {
			benchmarkCaptureJSON(b, size, false)
		})
		b.Run(fmt.Sprintf("sensitive/%dMiB", size>>20), func(b *testing.B) {
			benchmarkCaptureJSON(b, size, true)
		})
	}
}

func benchmarkCaptureJSON(b *testing.B, size int, sensitive bool) {
	content := strings.Repeat("x", size)
	if sensitive {
		content = `https://user:password@example.test/path?token=canary#fragment` + content
	}
	raw := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":` + fmt.Sprintf("%q", content) + `}]}`)
	if !json.Valid(raw) {
		b.Fatal("benchmark fixture must be valid JSON")
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got := modeltrace.TestingCaptureModelContent(raw, len(raw), modeltrace.TestingDefaultPromptBytes, modeltrace.TestingCapturePolicy{})
		if sensitive && strings.Contains(got, "password") {
			b.Fatal("credential leaked from captured URL")
		}
	}
}

func benchmarkModelTraceMode(b *testing.B, enabled bool) {
	gin.SetMode(gin.TestMode)
	longPromptSize := modeltrace.TestingDefaultPromptBytes - 128
	longBody := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"` + strings.Repeat("x", longPromptSize) + `"}]}`)
	if !json.Valid(longBody) {
		b.Fatal("long-context benchmark fixture must be valid JSON")
	}
	cases := []struct {
		name        string
		requestBody []byte
		contentType string
		response    []byte
	}{
		{
			name:        "non_stream",
			requestBody: []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`),
			contentType: "application/json",
			response:    []byte(`{"id":"chatcmpl-benchmark","choices":[{"message":{"content":"hello"}}]}`),
		},
		{
			name:        "sse",
			requestBody: []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}],"stream":true}`),
			contentType: "text/event-stream",
			response:    []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n"),
		},
		{
			name:        "long_context",
			requestBody: longBody,
			contentType: "application/json",
			response:    []byte(`{"id":"chatcmpl-long","choices":[{"message":{"content":"ok"}}]}`),
		},
	}
	for _, tc := range cases {
		tc := tc
		b.Run(tc.name, func(b *testing.B) {
			manager := newBenchmarkManager(b, enabled)
			router := benchmarkRouter(manager, tc.contentType, tc.response)
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.requestBody) + len(tc.response)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(tc.requestBody))
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				if recorder.Code != http.StatusOK || !bytes.Equal(recorder.Body.Bytes(), tc.response) {
					b.Fatalf("response changed: status=%d bytes=%d", recorder.Code, recorder.Body.Len())
				}
			}
		})
	}
}

type benchmarkDiscardExporter struct{}

func (benchmarkDiscardExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return nil
}
func (benchmarkDiscardExporter) Shutdown(context.Context) error { return nil }

func newBenchmarkManager(b *testing.B, enabled bool) *modeltrace.Manager {
	b.Helper()
	cfg := config.ModelTracingConfig{
		Enabled:          enabled,
		PromptMaxBytes:   modeltrace.TestingDefaultPromptBytes,
		ResponseMaxBytes: modeltrace.TestingDefaultResponseBytes,
		MediaMaxBytes:    modeltrace.TestingDefaultMediaBytes,
	}
	generation := modeltrace.TestingNewGeneration(modeltrace.TestingGenerationConfig{
		Cfg:         cfg,
		Source:      modeltrace.ConfigSourceDeployment,
		Fingerprint: modeltrace.TestingGenerationFingerprint(cfg, modeltrace.ConfigSourceDeployment, 0),
	})
	if enabled {
		provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(
			benchmarkDiscardExporter{},
			sdktrace.WithMaxQueueSize(modeltrace.TestingDefaultMaxQueueSize),
			sdktrace.WithMaxExportBatchSize(modeltrace.TestingDefaultMaxExportBatch),
			sdktrace.WithBatchTimeout(modeltrace.TestingDefaultBatchTimeout),
			sdktrace.WithExportTimeout(modeltrace.TestingDefaultExportTimeout),
		))
		generation.BindTracerProvider(provider)
	}
	manager := modeltrace.TestingNewManagerWithActive(generation)
	b.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			b.Errorf("shutdown benchmark trace provider: %v", err)
		}
	})
	return manager
}

func benchmarkRouter(manager *modeltrace.Manager, contentType string, response []byte) *gin.Engine {
	router := gin.New()
	router.POST("/v1/chat/completions",
		manager.CandidateMiddleware(),
		func(c *gin.Context) {
			groupID := int64(19)
			servermiddleware.SetOpsFallbackAPIKey(c, &service.APIKey{
				ID: 71, UserID: 73, User: &service.User{ID: 73},
				GroupID: &groupID, Group: &service.Group{ID: groupID},
			})
			c.Next()
		},
		func(c *gin.Context) {
			_, _ = io.Copy(io.Discard, c.Request.Body)
			c.Header("Content-Type", contentType)
			_, _ = c.Writer.Write(response)
			if contentType == "text/event-stream" {
				c.Writer.Flush()
			}
		},
	)
	return router
}
