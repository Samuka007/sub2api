package modeltrace_test

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/modeltrace"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeConversationOutput_passthroughJSON(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"output":[{"type":"message","role":"assistant","id":"a1","content":[{"type":"output_text","text":"hi"}]}]}`)
	got := modeltrace.NormalizeConversationOutput(raw)
	require.JSONEq(t, string(raw), string(got))
}

func TestNormalizeConversationOutput_responsesSSECompleted(t *testing.T) {
	t.Parallel()
	sse := []byte("" +
		"event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"CX\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"id\":\"msg_a1\",\"content\":[{\"type\":\"output_text\",\"text\":\"CXREAL-A1\"}]}]}}\n\n")
	got := modeltrace.NormalizeConversationOutput(sse)
	require.True(t, gjson.ValidBytes(got))
	require.Equal(t, "msg_a1", gjson.GetBytes(got, "output.0.id").String())
	require.Equal(t, "assistant", gjson.GetBytes(got, "output.0.role").String())
	require.Contains(t, gjson.GetBytes(got, "output.0.content.0.text").String(), "CXREAL-A1")
}

func TestNormalizeConversationOutput_fallsBackToOutputItemDone(t *testing.T) {
	t.Parallel()
	// Truncated stream: completed event missing, but item.done present.
	sse := []byte("" +
		"event: response.output_item.done\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"id\":\"msg_done\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello-done\"}]}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\n")
	got := modeltrace.NormalizeConversationOutput(sse)
	require.Equal(t, "msg_done", gjson.GetBytes(got, "output.0.id").String())
	require.Equal(t, "hello-done", gjson.GetBytes(got, "output.0.content.0.text").String())
}

func TestExtractConversationDelta_fromResponsesSSE(t *testing.T) {
	t.Parallel()
	input := `[{"type":"message","role":"user","id":"u1","content":[{"type":"input_text","text":"say hi"}]}]`
	sse := []byte("" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"id\":\"a1\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi there\"}]}]}}\n\n")
	normalized := modeltrace.NormalizeConversationOutput(sse)
	events, compact := modeltrace.ExtractConversationDelta("sess", "turn", []byte(input), normalized, nil)
	require.False(t, compact)
	require.Len(t, events, 2)
	require.Equal(t, "chat.user", events[0].Name)
	require.Equal(t, "u1", events[0].MessageID)
	require.Equal(t, "chat.assistant", events[1].Name)
	require.Equal(t, "a1", events[1].MessageID)
	require.Equal(t, "hi there", events[1].Content)
}
