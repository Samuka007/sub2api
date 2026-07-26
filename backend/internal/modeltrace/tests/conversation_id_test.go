package modeltrace_test

import (
	"github.com/Wei-Shaw/sub2api/internal/modeltrace"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractLangfuseTurnID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		body   string
		header http.Header
		want   string
	}{
		{name: "client_metadata turn_id", body: `{"client_metadata":{"turn_id":"turn-cm-1"}}`, want: "turn-cm-1"},
		{
			name:   "header x-codex-turn-metadata json",
			header: http.Header{"X-Codex-Turn-Metadata": []string{`{"turn_id":"turn-hdr-1"}`}},
			want:   "turn-hdr-1",
		},
		{
			name: "client_metadata x-codex-turn-metadata json",
			body: `{"client_metadata":{"x-codex-turn-metadata":"{\"turn_id\":\"turn-meta-1\"}"}}`,
			want: "turn-meta-1",
		},
		{
			name:   "body wins over header",
			body:   `{"client_metadata":{"turn_id":"turn-body"}}`,
			header: http.Header{"X-Codex-Turn-Metadata": []string{`{"turn_id":"turn-hdr"}`}},
			want:   "turn-body",
		},
		{name: "empty", body: `{"model":"x"}`},
		{name: "non string ignored", body: `{"client_metadata":{"turn_id":1}}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, modeltrace.ExtractLangfuseTurnID([]byte(tt.body), tt.header))
		})
	}
}

func TestMessageIDFromProtocolOrHash(t *testing.T) {
	t.Parallel()
	canonical := []byte(`{"type":"message","role":"user","content":"hi"}`)
	hashed := modeltrace.MessageIDFromProtocolOrHash("", canonical)
	require.Len(t, hashed, 64)
	require.Equal(t, hashed, modeltrace.MessageIDFromProtocolOrHash("", canonical))
	require.Equal(t, "msg_protocol_1", modeltrace.MessageIDFromProtocolOrHash("msg_protocol_1", canonical))
	require.Equal(t, "msg_protocol_1", modeltrace.MessageIDFromProtocolOrHash("  msg_protocol_1  ", canonical))
	require.NotEqual(t, hashed, modeltrace.MessageIDFromProtocolOrHash("", []byte(`{"type":"message","role":"user","content":"bye"}`)))
}
