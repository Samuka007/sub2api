package modeltrace_test

import (
	"github.com/Wei-Shaw/sub2api/internal/modeltrace"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractConversationDelta_newUserAndAssistant(t *testing.T) {
	t.Parallel()
	input := `[
		{"type":"message","role":"user","id":"u1","content":[{"type":"input_text","text":"hello"}]}
	]`
	output := `{"output":[{"type":"message","role":"assistant","id":"a1","content":[{"type":"output_text","text":"hi"}]}]}`
	events := modeltrace.ExtractConversationDelta("sess-1", "turn-1", []byte(input), []byte(output))
	require.Len(t, events, 2)
	require.Equal(t, "chat.user", events[0].Name)
	require.Equal(t, "u1", events[0].MessageID)
	require.Equal(t, "hello", events[0].Content)
	require.Equal(t, 0, events[0].Seq)
	require.Equal(t, "chat.assistant", events[1].Name)
	require.Equal(t, "a1", events[1].MessageID)
	require.Equal(t, "hi", events[1].Content)
	require.Equal(t, "u1", events[1].ParentMessageID)
}

func TestExtractConversationDelta_appendsRepeatedHistory(t *testing.T) {
	t.Parallel()
	input := `[
		{"type":"message","role":"user","id":"u1","content":[{"type":"input_text","text":"hello"}]},
		{"type":"message","role":"assistant","id":"a1","content":[{"type":"output_text","text":"hi"}]},
		{"type":"message","role":"user","id":"u2","content":[{"type":"input_text","text":"again"}]}
	]`
	events := modeltrace.ExtractConversationDelta("sess-1", "turn-2", []byte(input), nil)
	require.Len(t, events, 3)
	require.Equal(t, []string{"u1", "a1", "u2"}, []string{events[0].MessageID, events[1].MessageID, events[2].MessageID})
}

func TestExtractConversationDelta_tools(t *testing.T) {
	t.Parallel()
	input := `[]`
	output := `{
		"output":[
			{"type":"function_call","id":"fc1","call_id":"call_1","name":"lookup","arguments":"{\"q\":1}"},
			{"type":"function_call_output","id":"fo1","call_id":"call_1","output":"result"}
		]
	}`
	events := modeltrace.ExtractConversationDelta("sess-1", "turn-1", []byte(input), []byte(output))
	require.Len(t, events, 2)
	require.Equal(t, "chat.tool_call", events[0].Name)
	require.Equal(t, "call_1", events[0].CallID)
	require.Equal(t, "lookup", events[0].ToolName)
	require.Equal(t, "chat.tool_result", events[1].Name)
	require.Equal(t, "result", events[1].Content)
}

func TestExtractConversationDelta_doesNotInferCompactionFromRemoteHistory(t *testing.T) {
	t.Parallel()
	input := `[
		{"type":"message","role":"user","id":"u2","content":[{"type":"input_text","text":"later"}]}
	]`
	events := modeltrace.ExtractConversationDelta("sess-1", "turn-3", []byte(input), nil)
	require.Len(t, events, 1)
	require.Equal(t, "u2", events[0].MessageID)
}

func TestExtractConversationDelta_appendsItemsRegardlessOfMarkerNames(t *testing.T) {
	t.Parallel()
	input := `[
		{"type":"message","role":"user","id":"u2","content":[{"type":"input_text","text":"later"}]}
	]`
	events := modeltrace.ExtractConversationDelta("sess-child", "turn-2", []byte(input), nil)
	require.Len(t, events, 1)
	require.Equal(t, "u2", events[0].MessageID)
}

func TestExtractConversationDelta_includesDeveloperAndSystem(t *testing.T) {
	t.Parallel()
	input := `[
		{"type":"message","role":"developer","id":"d1","content":[{"type":"input_text","text":"system rules"}]},
		{"type":"message","role":"system","id":"s1","content":[{"type":"input_text","text":"policy"}]},
		{"type":"message","role":"user","id":"u1","content":[{"type":"input_text","text":"hello"}]}
	]`
	events := modeltrace.ExtractConversationDelta("sess-1", "turn-1", []byte(input), nil)
	require.Equal(t, "chat.system", events[0].Name)
	require.Equal(t, "d1", events[0].MessageID)
	require.Equal(t, "system rules", events[0].Content)
	require.Equal(t, "chat.system", events[1].Name)
	require.Equal(t, "s1", events[1].MessageID)
	require.Equal(t, "policy", events[1].Content)
	require.Equal(t, "chat.user", events[2].Name)
	require.Equal(t, "u1", events[2].MessageID)
}
