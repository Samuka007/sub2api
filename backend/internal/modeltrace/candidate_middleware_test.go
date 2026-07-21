package modeltrace

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func TestModelTraceAnonymousFailureSuppressed(t *testing.T) {
	requests := exerciseCandidateIdentityMatrix(t)
	roots := spansNamed(exportedSpans(requests), rootSpanName)

	require.Len(t, roots, 2, "unknown credentials must not allocate a Trace")
	require.NotContains(t, spanRequestIDs(roots), "anonymous")
}

func TestModelTraceRecognizedAuthFailure(t *testing.T) {
	requests := exerciseCandidateIdentityMatrix(t)
	roots := spansNamed(exportedSpans(requests), rootSpanName)

	require.Contains(t, spanRequestIDs(roots), "recognized-disabled")
}

func TestModelTraceNoUpstreamAttempt(t *testing.T) {
	requests := exerciseCandidateIdentityMatrix(t)
	spans := exportedSpans(requests)
	generations := spansNamed(spans, generationSpanName)

	require.Len(t, generations, 1, "rejected identity must keep only the root Trace")
	require.Equal(t, "accepted", spanRequestID(generations[0]))
}

func exerciseCandidateIdentityMatrix(t *testing.T) []*collectortracepb.ExportTraceServiceRequest {
	t.Helper()
	gin.SetMode(gin.TestMode)
	fake := newFakeOTLPServer(t)
	manager, err := NewManager(context.Background(), config.ModelTracingConfig{
		Enabled: true, Endpoint: fake.server.URL + "/api/public/otel",
		PublicKey: testPublicKey, SecretKey: testSecretKey,
		PromptMaxBytes: 4096, ResponseMaxBytes: 4096,
	})
	require.NoError(t, err)

	user := &service.User{ID: 42, Status: service.StatusActive}
	active := &service.APIKey{ID: 73, UserID: user.ID, Status: service.StatusActive, User: user}
	disabled := &service.APIKey{ID: 74, UserID: user.ID, Status: service.StatusDisabled, User: user}

	router := gin.New()
	router.POST("/candidate",
		manager.CandidateMiddleware(func(*gin.Context) bool { return true }),
		func(c *gin.Context) {
			switch c.GetHeader("x-test-identity") {
			case "anonymous":
				c.AbortWithStatus(http.StatusUnauthorized)
			case "recognized-disabled":
				servermiddleware.NotifyAPIKeyResolved(c, disabled)
				c.AbortWithStatus(http.StatusUnauthorized)
			case "accepted":
				servermiddleware.NotifyAPIKeyResolved(c, active)
				servermiddleware.NotifyAPIKeyAccepted(c, active)
				c.Next()
			}
		},
		func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) },
	)

	for _, identity := range []string{"anonymous", "recognized-disabled", "accepted"} {
		req := httptest.NewRequest(http.MethodPost, "/candidate", bytes.NewBufferString(`{"model":"gpt-test"}`))
		req.Header.Set("x-test-identity", identity)
		req.Header.Set("X-Client-Request-ID", identity)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, manager.Shutdown(shutdownCtx))
	requests, serverErrors := fake.snapshot()
	require.Empty(t, serverErrors)
	return requests
}

func spansNamed(spans []*tracepb.Span, name string) []*tracepb.Span {
	out := make([]*tracepb.Span, 0, len(spans))
	for _, span := range spans {
		if span.Name == name {
			out = append(out, span)
		}
	}
	return out
}

func spanRequestIDs(spans []*tracepb.Span) []string {
	out := make([]string, 0, len(spans))
	for _, span := range spans {
		out = append(out, spanRequestID(span))
	}
	return out
}

func spanRequestID(span *tracepb.Span) string {
	attrs := attributesByKey(span.Attributes)
	if value, ok := attrs["langfuse.trace.metadata.request_id"]; ok {
		return value.Value.GetStringValue()
	}
	if value, ok := attrs["langfuse.observation.metadata.request_id"]; ok {
		return value.Value.GetStringValue()
	}
	return ""
}
