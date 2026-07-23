package modeltrace_test

import (
	"context"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/modeltrace"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLangfusePublicBaseURL(t *testing.T) {
	t.Parallel()
	require.Equal(t, "http://localhost:3000", modeltrace.TestingLangfusePublicBaseURL("http://localhost:3000/api/public/otel"))
	require.Equal(t, "https://langfuse.example.com", modeltrace.TestingLangfusePublicBaseURL("https://langfuse.example.com/api/public/otel/"))
	require.Equal(t, "http://localhost:3000", modeltrace.TestingLangfusePublicBaseURL("http://localhost:3000"))
}

func TestLangfuseReaderListChatMessageIDs(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		require.True(t, ok)
		require.Equal(t, "pk", user)
		require.Equal(t, "sk", pass)
		switch {
		case r.URL.Path == "/api/public/traces" && r.URL.Query().Get("sessionId") == "sess-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "trace-1"}},
			})
		case r.URL.Path == "/api/public/observations" && r.URL.Query().Get("traceId") == "trace-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"name": "chat.user", "metadata": map[string]any{"message_id": "u1"}},
					{"name": "model.request", "metadata": map[string]any{"message_id": "ignore"}},
					{"name": "chat.assistant", "metadata": map[string]any{"message_id": "a1"}},
					{"name": "chat.compact", "metadata": map[string]any{}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	reader := &modeltrace.LangfuseReader{
		BaseURL:    server.URL,
		PublicKey:  "pk",
		SecretKey:  "sk",
		HTTPClient: server.Client(),
	}
	ids, err := reader.ListChatMessageIDs(context.Background(), "sess-1")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"u1": "chat.user", "a1": "chat.assistant"}, ids)
}

func TestLangfuseReaderListChatMessageIDsError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)
	reader := &modeltrace.LangfuseReader{BaseURL: server.URL, PublicKey: "pk", SecretKey: "sk", HTTPClient: server.Client()}
	_, err := reader.ListChatMessageIDs(context.Background(), "sess-1")
	require.Error(t, err)
}
