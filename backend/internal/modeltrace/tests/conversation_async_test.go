package modeltrace_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/modeltrace"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func TestModelTraceConversationHTTPAppendsWithoutLangfusePublicAPIRead(t *testing.T) {
	manager, fake := newUsageTestManager(t)
	router := gin.New()
	router.POST("/v1/responses", manager.CandidateMiddleware(), installUsageTestIdentity(), func(c *gin.Context) {
		_, err := c.GetRawData()
		require.NoError(t, err)
		c.JSON(http.StatusOK, gin.H{"output": []gin.H{{
			"type": "message", "role": "assistant", "id": "a1",
			"content": []gin.H{{"type": "output_text", "text": "hi"}},
		}}})
	})

	body := `{"model":"gpt-test","session_id":"append-http","input":[{"type":"message","role":"user","id":"u1","content":[{"type":"input_text","text":"hello"}]}]}`
	for range 2 {
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(httptest.NewRecorder(), request)
	}
	shutdownUsageTestManager(t, manager)

	spans := exportedSpansFromFake(t, fake)
	require.Zero(t, fake.publicAPIReadCount())
	require.Equal(t, []string{"u1", "u1"}, messageIDsForSpans(t, spans, "chat.user"))
	require.Equal(t, []string{"a1", "a1"}, messageIDsForSpans(t, spans, "chat.assistant"))
}

func TestModelTraceResponsesWSTurnAppendsWithoutLangfusePublicAPIRead(t *testing.T) {
	manager, fake := newWSTurnTestManager(t)
	input := []byte(`{"type":"response.create","model":"gpt-test","input":[{"type":"message","role":"user","id":"u1","content":[{"type":"input_text","text":"hello"}]}]}`)
	output := []byte(`{"type":"response.completed","response":{"output":[{"type":"message","role":"assistant","id":"a1","content":[{"type":"output_text","text":"hi"}]}]}}`)
	for index := 1; index <= 2; index++ {
		turn := manager.StartResponsesWSTurn(context.Background(), modeltrace.ResponsesWSTurnMetadata{
			Identity:  servermiddleware.ResolvedIdentity{APIKeyID: 71, UserID: 73, GroupID: 19},
			SessionID: "append-ws", TurnRequestID: "turn", TurnIndex: index, Path: "/v1/responses", Model: "gpt-test",
		}, input)
		require.NotNil(t, turn)
		turn.ObserveClientWrite(output, nil)
		turn.End(modeltrace.TestingStreamStatusCompleted, "", nil)
	}
	shutdownWSTurnManager(t, manager)

	spans := exportedSpansFromFake(t, fake)
	require.Zero(t, fake.publicAPIReadCount())
	require.Equal(t, []string{"u1", "u1"}, messageIDsForSpans(t, spans, "chat.user"))
	require.Equal(t, []string{"a1", "a1"}, messageIDsForSpans(t, spans, "chat.assistant"))
}

func TestModelTraceConversationExplicitCompactionAndForkStayAppendOnly(t *testing.T) {
	manager, fake := newUsageTestManager(t)
	router := gin.New()
	router.POST("/v1/responses", manager.CandidateMiddleware(), installUsageTestIdentity(), func(c *gin.Context) {
		_, err := c.GetRawData()
		require.NoError(t, err)
		c.JSON(http.StatusOK, gin.H{"output": []gin.H{}})
	})

	body := `{"model":"gpt-test","session_id":"markers-http","fork_from_session_id":"parent-session","fork_from_message_id":"parent-message","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"compaction\",\"turn_id\":\"turn-markers\"}"},"input":[{"type":"message","role":"user","id":"u-marker","content":[{"type":"input_text","text":"current"}]}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(httptest.NewRecorder(), request)
	shutdownUsageTestManager(t, manager)

	spans := exportedSpansFromFake(t, fake)
	require.Zero(t, fake.publicAPIReadCount())
	require.Equal(t, 1, countSpansNamed(spans, "chat.compact"))
	require.Equal(t, 1, countSpansNamed(spans, "chat.fork"))
	fork := spanNamed(t, spans, "chat.fork")
	require.JSONEq(t, `{"session_id":"markers-http","turn_id":"turn-markers","message_id":"fork:markers-http","parent_message_id":"","seq":0,"fork_from_session_id":"parent-session","fork_from_message_id":"parent-message"}`,
		stringAttribute(t, attributesByKey(fork.Attributes), "langfuse.observation.metadata"))
}

func exportedSpansFromFake(t *testing.T, fake *fakeOTLPServer) []*tracepb.Span {
	t.Helper()
	requests, errors := fake.snapshot()
	require.Empty(t, errors)
	return exportedSpans(requests)
}

func countSpansNamed(spans []*tracepb.Span, name string) int {
	count := 0
	for _, span := range spans {
		if span.Name == name {
			count++
		}
	}
	return count
}

func messageIDsForSpans(t *testing.T, spans []*tracepb.Span, name string) []string {
	t.Helper()
	ids := make([]string, 0)
	for _, span := range spans {
		if span.Name != name {
			continue
		}
		var metadata map[string]any
		require.NoError(t, json.Unmarshal([]byte(stringAttribute(t, attributesByKey(span.Attributes), "langfuse.observation.metadata")), &metadata))
		messageID, ok := metadata["message_id"].(string)
		require.True(t, ok)
		ids = append(ids, messageID)
	}
	return ids
}
