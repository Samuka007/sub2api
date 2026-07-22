package modeltrace

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
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
