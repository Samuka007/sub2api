package modeltrace_test

import (
	"github.com/Wei-Shaw/sub2api/internal/modeltrace"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLangfuseSessionExtractor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		body   string
		header http.Header
		grok   bool
		want   string
	}{
		{name: "session_id", body: `{"session_id":"session-1"}`, want: "session-1"},
		{name: "conversation_id", body: `{"conversation_id":"conversation-1"}`, want: "conversation-1"},
		{name: "metadata session", body: `{"metadata":{"session_id":"metadata-1"}}`, want: "metadata-1"},
		{name: "structured user id session", body: `{"metadata":{"user_id":{"session_id":"nested-1"}}}`, want: "nested-1"},
		{name: "client_metadata session", body: `{"client_metadata":{"session_id":"cm-session-1"}}`, want: "cm-session-1"},
		{name: "client_metadata thread fallback", body: `{"client_metadata":{"thread_id":"cm-thread-1"}}`, want: "cm-thread-1"},
		{name: "client_metadata session wins over thread", body: `{"client_metadata":{"session_id":"cm-session-2","thread_id":"cm-thread-2"}}`, want: "cm-session-2"},
		{name: "header session_id", header: http.Header{"Session_id": []string{"hdr-session-1"}}, want: "hdr-session-1"},
		{name: "header session-id", header: http.Header{"Session-Id": []string{"hdr-session-dash"}}, want: "hdr-session-dash"},
		{name: "header thread_id fallback", header: http.Header{"Thread_id": []string{"hdr-thread-1"}}, want: "hdr-thread-1"},
		{name: "body session wins over header", body: `{"session_id":"body-1"}`, header: http.Header{"Session_id": []string{"hdr-1"}}, want: "body-1"},
		{name: "client_metadata wins over header", body: `{"client_metadata":{"session_id":"cm-1"}}`, header: http.Header{"Session_id": []string{"hdr-1"}}, want: "cm-1"},
		{name: "grok header on grok route", header: http.Header{"X-Grok-Conv-Id": []string{"grok-1"}}, grok: true, want: "grok-1"},
		{name: "grok header rejected on non grok route", header: http.Header{"X-Grok-Conv-Id": []string{"grok-1"}}},
		{name: "prompt cache key excluded", body: `{"prompt_cache_key":"cache-1"}`},
		{name: "sticky hash excluded", body: `{"session_hash":"sticky-1","metadata":{"sticky_hash":"sticky-2"}}`},
		{name: "content fallback excluded", body: `{"input":"same content"}`},
		{name: "generated upstream session excluded", body: `{"metadata":{"upstream_session_id":"generated-1"}}`},
		{name: "non string excluded", body: `{"session_id":42,"metadata":{"session_id":true}}`},
		{name: "invalid json excluded", body: `{`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, modeltrace.ExtractLangfuseSessionID([]byte(tt.body), tt.header, tt.grok))
		})
	}
}
