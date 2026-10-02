package modeltrace_test

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/modeltrace"
	"github.com/stretchr/testify/require"
)

func TestCorrelationExtractorAnthropicMessages(t *testing.T) {
	t.Parallel()
	legacy := "user_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2_account_550e8400-e29b-41d4-a716-446655440000_session_123e4567-e89b-12d3-a456-426614174000"
	tests := []struct {
		name       string
		body       string
		header     http.Header
		wantID     string
		wantSource string
	}{
		{name: "explicit session", body: `{"session_id":"body-session"}`, wantID: "body-session", wantSource: "body.session_id"},
		{name: "metadata object", body: `{"metadata":{"session_id":"metadata-session"}}`, wantID: "metadata-session", wantSource: "body.metadata.session_id"},
		{name: "metadata JSON string", body: `{"metadata":"{\"session_id\":\"string-metadata-session\"}"}`, wantID: "string-metadata-session", wantSource: "body.metadata.session_id"},
		{name: "user id JSON string", body: `{"metadata":{"user_id":"{\"device_id\":\"device-1\",\"session_id\":\"json-session\"}"}}`, wantID: "json-session", wantSource: "body.metadata.user_id.json"},
		{name: "user id object compatibility", body: `{"metadata":{"user_id":{"session_id":"object-session"}}}`, wantID: "object-session", wantSource: "body.metadata.user_id.json"},
		{name: "user id legacy", body: `{"metadata":{"user_id":"` + legacy + `"}}`, wantID: "123e4567-e89b-12d3-a456-426614174000", wantSource: "body.metadata.user_id.legacy"},
		{name: "Claude header", header: http.Header{"X-Claude-Code-Session-Id": []string{"claude-header"}}, wantID: "claude-header", wantSource: "header.x_claude_code_session_id"},
		{name: "Claude header before standard", header: http.Header{"X-Claude-Code-Session-Id": []string{"claude-header"}, "Session-Id": []string{"standard-header"}}, wantID: "claude-header", wantSource: "header.x_claude_code_session_id"},
		{name: "standard header", header: http.Header{"Session-Id": []string{"standard-header"}}, wantID: "standard-header", wantSource: "header.session_id"},
		{name: "reject loose legacy suffix", body: `{"metadata":{"user_id":"prefix_session_123e4567-e89b-12d3-a456-426614174000"}}`},
		{name: "reject malformed JSON user id", body: `{"metadata":{"user_id":"{session_id:bad}"}}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := modeltrace.ExtractCorrelation([]byte(tt.body), tt.header, "anthropic.messages", false)
			require.Equal(t, tt.wantID, got.SessionID)
			require.Equal(t, tt.wantSource, got.SessionSource)
		})
	}
}

func TestCorrelationExtractorOpenAIResponses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		body         string
		header       http.Header
		grok         bool
		wantID       string
		wantSource   string
		wantConflict bool
	}{
		{name: "client metadata", body: `{"client_metadata":{"session_id":"client-session"}}`, wantID: "client-session", wantSource: "body.client_metadata.session_id"},
		{name: "body wins conflict", body: `{"client_metadata":{"session_id":"body-session"}}`, header: http.Header{"Session-Id": []string{"header-session"}}, wantID: "body-session", wantSource: "body.client_metadata.session_id", wantConflict: true},
		{name: "same value is not conflict", body: `{"session_id":"same-session"}`, header: http.Header{"Session-Id": []string{"same-session"}}, wantID: "same-session", wantSource: "body.session_id"},
		{name: "standard header before Claude", header: http.Header{"Session-Id": []string{"standard-header"}, "X-Claude-Code-Session-Id": []string{"claude-header"}}, wantID: "standard-header", wantSource: "header.session_id"},
		{name: "Claude fallback", header: http.Header{"X-Claude-Code-Session-Id": []string{"claude-header"}}, wantID: "claude-header", wantSource: "header.x_claude_code_session_id"},
		{name: "Grok route fallback", header: http.Header{"X-Grok-Conv-Id": []string{"grok-header"}}, grok: true, wantID: "grok-header", wantSource: "header.x_grok_conv_id"},
		{name: "Grok rejected off route", header: http.Header{"X-Grok-Conv-Id": []string{"grok-header"}}},
		{name: "metadata user id is Anthropic only", body: `{"metadata":{"user_id":"{\"session_id\":\"wrong-protocol\"}"}}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := modeltrace.ExtractCorrelation([]byte(tt.body), tt.header, "openai.responses", tt.grok)
			require.Equal(t, tt.wantID, got.SessionID)
			require.Equal(t, tt.wantSource, got.SessionSource)
			require.Equal(t, tt.wantConflict, got.SessionConflict)
		})
	}
}

func TestCorrelationExtractorKeepsThreadIndependent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		body       string
		header     http.Header
		wantThread string
		wantSource string
	}{
		{name: "client metadata thread", body: `{"client_metadata":{"thread_id":"body-thread"}}`, wantThread: "body-thread", wantSource: "body.client_metadata.thread_id"},
		{name: "thread header", header: http.Header{"Thread-Id": []string{"header-thread"}}, wantThread: "header-thread", wantSource: "header.thread_id"},
		{name: "Codex turn metadata", header: http.Header{"X-Codex-Turn-Metadata": []string{`{"thread_id":"codex-thread"}`}}, wantThread: "codex-thread", wantSource: "header.x_codex_turn_metadata.thread_id"},
		{name: "body thread wins header", body: `{"client_metadata":{"thread_id":"body-thread"}}`, header: http.Header{"Thread-Id": []string{"header-thread"}}, wantThread: "body-thread", wantSource: "body.client_metadata.thread_id"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := modeltrace.ExtractCorrelation([]byte(tt.body), tt.header, "openai.responses", false)
			require.Empty(t, got.SessionID)
			require.Equal(t, tt.wantThread, got.ThreadID)
			require.Equal(t, tt.wantSource, got.ThreadSource)
		})
	}
}

func TestCorrelationExtractorExcludesInferredIdentifiersAndModelNames(t *testing.T) {
	t.Parallel()
	for _, model := range []string{"claude-sonnet-4-5", "gpt-5.4", "deepseek-v3"} {
		body := `{"model":"` + model + `","prompt_cache_key":"cache-1","previous_response_id":"resp-1","session_hash":"sticky-1"}`
		got := modeltrace.ExtractCorrelation([]byte(body), nil, "anthropic.messages", false)
		require.Empty(t, got.SessionID, model)
		require.Empty(t, got.ThreadID, model)
	}
}
