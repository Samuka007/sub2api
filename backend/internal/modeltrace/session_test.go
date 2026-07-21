package modeltrace

import (
	"net/http"
	"testing"
)

func TestLangfuseSessionExtractor(t *testing.T) {
	header := func(name, value string) http.Header {
		h := make(http.Header)
		h.Set(name, value)
		return h
	}

	tests := []struct {
		name    string
		input   string
		headers http.Header
		want    string
	}{
		{
			name:  "top-level session_id",
			input: `{"session_id":"session-direct"}`,
			want:  "session-direct",
		},
		{
			name:  "top-level conversation_id",
			input: `{"conversation_id":"conversation-direct"}`,
			want:  "conversation-direct",
		},
		{
			name:  "metadata session_id",
			input: `{"metadata":{"session_id":"session-metadata"}}`,
			want:  "session-metadata",
		},
		{
			name:  "structured metadata user_id session_id",
			input: `{"metadata":{"user_id":{"device_id":"device-1","session_id":"session-structured"}}}`,
			want:  "session-structured",
		},
		{
			name:  "JSON-encoded metadata user_id session_id",
			input: `{"metadata":{"user_id":"{\"device_id\":\"device-1\",\"session_id\":\"session-encoded\"}"}}`,
			want:  "session-encoded",
		},
		{
			name:    "session_id header",
			headers: header("session_id", "session-header"),
			want:    "session-header",
		},
		{
			name:    "conversation_id header",
			headers: header("conversation_id", "conversation-header"),
			want:    "conversation-header",
		},
		{
			name:    "Grok conversation header",
			headers: header("X-Grok-Conv-Id", "grok-conversation"),
			want:    "grok-conversation",
		},
		{
			name:  "explicit values are trimmed",
			input: `{"session_id":"  session-trimmed  "}`,
			want:  "session-trimmed",
		},
		{
			name:  "invalid typed explicit field falls through",
			input: `{"session_id":42,"conversation_id":"conversation-fallback"}`,
			want:  "conversation-fallback",
		},
		{
			name:  "prompt cache key is not a session",
			input: `{"prompt_cache_key":"cache-key"}`,
		},
		{
			name:  "sticky routing session hash is not a session",
			input: `{"session_hash":"sticky-route-hash","sticky_session_hash":"sticky-route-hash-2"}`,
		},
		{
			name:    "GenerateSessionHash output is not a session",
			headers: header("X-Session-Hash", "generated-sticky-hash"),
		},
		{
			name:  "request content does not create a fallback session",
			input: `{"model":"gpt-5","messages":[{"role":"user","content":"session_id: content-derived"}]}`,
		},
		{
			name:  "legacy metadata routing value is not parsed",
			input: `{"metadata":{"user_id":"user_device_account_account_session_generated-upstream"}}`,
		},
		{
			name:  "arbitrary nested session_id is not allowlisted",
			input: `{"routing":{"session_id":"routing-session"}}`,
		},
		{
			name:    "unlisted session-like header is ignored",
			headers: header("X-Claude-Code-Session-Id", "claude-generated-session"),
		},
		{
			name:    "malformed JSON cannot contribute a field",
			input:   `{"session_id":"partial"`,
			headers: header("X-Grok-Conv-Id", "valid-header-session"),
			want:    "valid-header-session",
		},
		{
			name:  "empty explicit fields do not create a session",
			input: `{"session_id":"  ","conversation_id":"","metadata":{"session_id":""}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractSession([]byte(tt.input), tt.headers)
			if got != tt.want {
				t.Fatalf("ExtractSession() = %q, want %q", got, tt.want)
			}
		})
	}
}
