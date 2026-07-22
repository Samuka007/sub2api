package modeltrace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/modeltrace/recording"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func TestModelTraceNoCredentialLeak(t *testing.T) {
	secrets := []string{
		"sk-openai-secret",
		"anthropic-secret",
		"bearer-secret",
		"private-key-secret",
		"camel-client-secret",
		"session-cookie-secret",
	}
	payload := map[string]any{
		"openai_api_key": secrets[0],
		"nested": map[string]any{
			"anthropic-api-key": secrets[1],
			"bearerToken":       secrets[2],
			"private_key":       secrets[3],
			"clientSecret":      secrets[4],
			"session_cookie":    secrets[5],
		},
		"messages": []any{map[string]any{
			"role":    "user",
			"content": "Explain api_key=business-example and Authorization: Bearer business-example verbatim",
		}},
	}
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	captured := captureModelContent(raw, len(raw), 4096, capturePolicy{})
	for _, secret := range secrets {
		require.NotContains(t, captured, secret)
	}
	require.Contains(t, captured, "api_key=business-example")
	require.Contains(t, captured, "Authorization: Bearer business-example")
	require.GreaterOrEqual(t, strings.Count(captured, redactedValue), len(secrets))

	truncatedJSON := `{"openai_api_key":"truncated-secret","auth-token":"another-secret"`
	unstructured := captureModelContent([]byte(truncatedJSON), len(truncatedJSON)+1024, 4096, capturePolicy{})
	require.NotContains(t, unstructured, "truncated-secret")
	require.NotContains(t, unstructured, "another-secret")
}

func TestModelTraceLargePayloadMemoryBound(t *testing.T) {
	prompt, response, media := boundedSizes(config.ModelTracingConfig{
		PromptMaxBytes:   int(^uint(0) >> 1),
		ResponseMaxBytes: int(^uint(0) >> 1),
		MediaMaxBytes:    int(^uint(0) >> 1),
	})
	require.Equal(t, maxCaptureBytes, prompt)
	require.Equal(t, maxCaptureBytes, response)
	require.Equal(t, maxCaptureBytes, media)

	raw := []byte(strings.Repeat("x", maxCaptureBytes+1024))
	captured := captureModelContent(raw, len(raw), prompt, capturePolicy{})
	require.LessOrEqual(t, len(captured), maxCaptureBytes+128)
	require.Contains(t, captured[len(captured)-128:], "[truncated:original_bytes=")
}

func TestModelTraceMediaURLRemovesCredentialsAndBoundsMetadata(t *testing.T) {
	secretQuery := strings.Repeat("signed-query-secret", 2048)
	raw := []byte(`{"content":[{"type":"image_url","image_url":{"url":"https://user:password@cdn.example.test/path/image.png?signature=` + secretQuery + `#fragment-secret"}}]}`)
	captured := captureModelContent(raw, len(raw), 4096, capturePolicy{})
	require.NotContains(t, captured, "password")
	require.NotContains(t, captured, "signed-query-secret")
	require.NotContains(t, captured, "fragment-secret")
	require.Contains(t, captured, "https://cdn.example.test/path/image.png")
	require.Less(t, len(captured), 1024)
}

// TestModelTraceURLNotMediaFieldRedactsCredentials covers the gap where a
// JSON string value under a non-secret, non-media key (e.g. "url" or
// "download_url") carries embedded userinfo credentials, query tokens or
// fragments. Before the fix, needsStructuredSanitization returned false for
// clean JSON, so the URL scrubber was unreachable and credentials leaked to
// Langfuse as observation input/output.
func TestModelTraceURLNotMediaFieldRedactsCredentials(t *testing.T) {
	type tc struct {
		body string
		host string
	}
	cases := []tc{
		{`{"data":[{"url":"https://user:bearer-canary@cdn.example/path?signature=canary#frag"}]}`, "cdn.example"},
		{`{"download_url":"https://user:pass@host/file?token=query-canary"}`, "host"},
		{`{"links":{"self":"https://admin:secret@api.example/v1?api_key=key-canary"}}`, "api.example"},
	}
	for _, c := range cases {
		captured := captureModelContent([]byte(c.body), len(c.body), 4096, capturePolicy{})
		require.NotContains(t, captured, "bearer-canary", c.body)
		require.NotContains(t, captured, "pass", c.body)
		require.NotContains(t, captured, "secret", c.body)
		require.NotContains(t, captured, "query-canary", c.body)
		require.NotContains(t, captured, "key-canary", c.body)
		require.NotContains(t, captured, "frag", c.body)
		// host remains observable; only credentials/query/fragment stripped
		require.Contains(t, captured, c.host, c.body)
	}
}

// TestModelTraceFormURLEncodedRedactsSecretFields covers the gap where an
// application/x-www-form-urlencoded body such as "api_key=x&model=y" bypassed
// redaction because it has no JSON quoting or colon-delimited headers.
func TestModelTraceFormURLEncodedRedactsSecretFields(t *testing.T) {
	raw := []byte("model=gpt-4&api_key=client-canary&access_token=token-canary&prompt=hello")
	captured := captureModelContentWithType(raw, len(raw), 4096, "application/x-www-form-urlencoded", capturePolicy{})
	require.NotContains(t, captured, "client-canary")
	require.NotContains(t, captured, "token-canary")
	require.Contains(t, captured, "REDACTED", "redacted value may be URL-encoded")
	require.Contains(t, captured, "gpt-4")
	require.Contains(t, captured, "hello")
}

// TestModelTraceSanitizeErrorScrubsCredentials verifies the error sanitizer
// used for both synchronous attempt status and async execution status/events.
// Transport errors may embed URLs with userinfo, query tokens, or
// authorization headers that must never reach Langfuse.
func TestModelTraceSanitizeErrorScrubsCredentials(t *testing.T) {
	Vector := "Get https://user:bearer-canary@host.example/path?api_key=query-canary: " +
		"Authorization: Bearer header-canary, Set-Cookie: session=cookie-canary: failed"
	scrubbed := sanitizeTraceError(Vector)
	require.NotContains(t, scrubbed, "bearer-canary")
	require.NotContains(t, scrubbed, "query-canary")
	require.NotContains(t, scrubbed, "header-canary")
	require.NotContains(t, scrubbed, "cookie-canary")
	require.NotContains(t, scrubbed, "user:")
	require.Contains(t, scrubbed, "host.example")
}

// TestModelTraceAttemptErrorDoesNotLeakIntoSpanStatus is an end-to-end
// regression: a raw HTTP transport error passed to AttemptResult.Err must not
// appear verbatim in the exported span Status.Message.
func TestModelTraceAttemptErrorDoesNotLeakIntoSpanStatus(t *testing.T) {
	sensitiveErr := errors.New("Get https://user:bearer-canary@host/?api_key=query-canary: failed")
	spans := runAttemptTrace(t, []byte(`{"model":"gpt-4"}`), func(c *gin.Context) {
		_, err := c.GetRawData()
		require.NoError(t, err)
		attempt := recording.BeginAttempt(c.Request.Context(), recording.AttemptMetadata{
			Provider: "anthropic", Operation: "chat", ClientModel: "gpt-4", UpstreamModel: "claude-err",
			AccountID: 63, Endpoint: "https://anthropic.example/v1/messages",
		}, []byte(`{"model":"claude-err"}`))
		attempt.End(recording.AttemptResult{Err: sensitiveErr})
		c.Data(http.StatusInternalServerError, "application/json", []byte(`{"error":"upstream failed"}`))
	})

	require.Len(t, spans, 2)
	attempt := spanNamed(t, spans, "upstream.attempt.1")
	require.Equal(t, tracepb.Status_STATUS_CODE_ERROR, attempt.Status.Code)
	require.NotContains(t, attempt.Status.Message, "bearer-canary")
	require.NotContains(t, attempt.Status.Message, "query-canary")
	require.Contains(t, attempt.Status.Message, "host")
}

// TestModelTraceAsyncEndErrorDoesNotLeakIntoSpanStatus is an end-to-end
// regression for the async execution path: a panic/error carrying auth
// material must be scrubbed before being written as span status and
// exception event.
func TestModelTraceAsyncEndErrorDoesNotLeakIntoSpanStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := newFakeOTLPServer(t)
	manager, err := NewManager(context.Background(), config.ModelTracingConfig{
		Enabled: true, Endpoint: fake.server.URL + "/api/public/otel",
		PublicKey: testPublicKey, SecretKey: testSecretKey,
		PromptMaxBytes: 4096, ResponseMaxBytes: 4096,
	})
	require.NoError(t, err)

	sensitiveErr := errors.New("panic: Authorization: Bearer async-canary, url=https://user:secret@upstream/?token=t-canary")

	continuation := recording.TraceContinuation{TraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef"}
	execution := manager.StartAsyncExecution(context.Background(), continuation, AsyncExecutionMetadata{
		Identity: servermiddleware.ResolvedIdentity{APIKeyID: 71, UserID: 73},
		TaskID:   "imgtask-err", Model: "gpt-image-err",
	}, []byte(`{"prompt":"cat"}`))
	require.NotNil(t, execution)
	execution.End("failed", []byte(`{"error":"done"}`), sensitiveErr)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, manager.Shutdown(shutdownCtx))

	requests, serverErrs := fake.snapshot()
	require.Empty(t, serverErrs)
	spans := exportedSpans(requests)

	for _, canary := range []string{"async-canary", "secret", "t-canary"} {
		require.NotContains(t, spans[0].Status.Message, canary)
	}
	for _, event := range spans[0].Events {
		for _, attr := range event.Attributes {
			serialized := fmt.Sprintf("%v", attr.Value)
			require.NotContains(t, serialized, "async-canary")
			require.NotContains(t, serialized, "t-canary")
			require.NotContains(t, serialized, "secret")
		}
	}
}

